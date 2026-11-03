// Package restictest gives tests restic repositories in local folders that
// are cheap to open.
//
// restic derives the key of a repository from its password with scrypt,
// tuned at init to take about half a second, and does it again on every
// command; init itself takes seconds. A test that runs ten commands spends
// most of its time there. The repositories here are copies of one made by
// restic init, with a key of the same kind at a low scrypt cost (restic
// stores the cost in the key file), so no test waits for init and every
// command opens the repository in about a millisecond. Everything else is
// restic's own: format, encryption, compression, packs, locks.
package restictest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/crypto/poly1305" //nolint:staticcheck // restic's MAC is Poly1305-AES; this is the package that computes it.
	"golang.org/x/crypto/scrypt"

	"github.com/nebari-dev/javo/internal/backup/restic"
)

// Bin is the restic the tests run: $JAVO_TEST_RESTIC, else the one
// `make bin/restic` downloads (scripts/build/fetch-restic.sh). A missing
// restic fails the test, like a missing Postgres does.
func Bin(t testing.TB) string {
	t.Helper()
	if b := os.Getenv("JAVO_TEST_RESTIC"); b != "" {
		return b
	}
	_, here, _, _ := runtime.Caller(0)
	b := filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "bin", "restic")
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("no restic at %s: run make bin/restic or set JAVO_TEST_RESTIC", b)
	}
	return b
}

// New is an initialised repository in a folder of the test's own, opened
// with password. Every repository New makes is a copy of one template, so
// they share a repository id and master key; they share no data.
func New(t testing.TB, password string) restic.Repo {
	t.Helper()
	b := Bin(t)
	dir := filepath.Join(t.TempDir(), "repo")
	for _, d := range layout() {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config, err := templateFS.ReadFile("testdata/repo/config")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := fs.Glob(templateFS, "testdata/repo/keys/*")
	if err != nil || len(keys) != 1 {
		t.Fatalf("template keys: %v, %v", keys, err)
	}
	raw, err := templateFS.ReadFile(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	master, err := openKey(raw, templatePassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKey(dir, master, password); err != nil {
		t.Fatal(err)
	}
	return restic.Repo{Bin: b, URL: dir, Env: []string{"RESTIC_PASSWORD=" + password}}
}

// Cheapen replaces the key of the repository restic made in dir with one of
// the same password that is cheap to open.
func Cheapen(t testing.TB, dir, password string) {
	t.Helper()
	keys, err := filepath.Glob(filepath.Join(dir, "keys", "*"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys of %s: %v, %v", dir, keys, err)
	}
	master, err := readKey(keys[0], password)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keys[0]); err != nil {
		t.Fatal(err)
	}
	if err := writeKey(dir, master, password); err != nil {
		t.Fatal(err)
	}
}

// template is the repository New copies, made by restic 0.19.1 with
// `restic init` and the password "template", then given a cheap key with
// Cheapen. Its config is restic's own, so repositories from New are what
// restic init makes.
//
//go:embed testdata/repo
var templateFS embed.FS

const templatePassword = "template"

// layout is the folders restic init makes.
func layout() []string {
	dirs := []string{"index", "keys", "locks", "snapshots"}
	for i := range 256 {
		dirs = append(dirs, fmt.Sprintf("data/%02x", i))
	}
	return dirs
}

// keyFile is restic's key file (restic.readthedocs.io, "Keys, Encryption
// and MAC"): the master key sealed with a key scrypt derives from the
// password.
type keyFile struct {
	Created  time.Time `json:"created"`
	Username string    `json:"username"`
	Hostname string    `json:"hostname"`
	KDF      string    `json:"kdf"`
	N        int       `json:"N"`
	R        int       `json:"r"`
	P        int       `json:"p"`
	Salt     []byte    `json:"salt"`
	Data     []byte    `json:"data"`
}

// The scrypt cost of the keys written here: about a millisecond.
const cheapN, cheapR, cheapP = 1024, 1, 1

func readKey(path, password string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return openKey(raw, password)
}

// openKey is the master key a key file seals with password.
func openKey(raw []byte, password string) ([]byte, error) {
	var k keyFile
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, err
	}
	enc, mac, err := derive(password, k.Salt, k.N, k.R, k.P)
	if err != nil {
		return nil, err
	}
	if len(k.Data) < 32 {
		return nil, errors.New("key data too short")
	}
	nonce, ct, tag := k.Data[:16], k.Data[16:len(k.Data)-16], k.Data[len(k.Data)-16:]
	want, err := poly1305AES(mac, nonce, ct)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(want, tag) {
		return nil, errors.New("wrong password or not a restic key")
	}
	return ctr(enc, nonce, ct)
}

func writeKey(dir string, master []byte, password string) error {
	salt := make([]byte, 64)
	nonce := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	enc, mac, err := derive(password, salt, cheapN, cheapR, cheapP)
	if err != nil {
		return err
	}
	ct, err := ctr(enc, nonce, master)
	if err != nil {
		return err
	}
	tag, err := poly1305AES(mac, nonce, ct)
	if err != nil {
		return err
	}
	data := append(append(append([]byte{}, nonce...), ct...), tag...)
	raw, err := json.Marshal(keyFile{Created: time.Now(), Username: "test", Hostname: "test", KDF: "scrypt", N: cheapN, R: cheapR, P: cheapP, Salt: salt, Data: data})
	if err != nil {
		return err
	}
	// A key file is named by the SHA-256 of its content, like every file
	// in a restic repository.
	id := sha256.Sum256(raw)
	return os.WriteFile(filepath.Join(dir, "keys", hex.EncodeToString(id[:])), raw, 0o600)
}

// derive is restic's KDF: 32 bytes of AES-256 key, then 32 of MAC key
// (16 for AES, 16 for Poly1305's r).
func derive(password string, salt []byte, n, r, p int) (enc, mac []byte, err error) {
	kb, err := scrypt.Key([]byte(password), salt, n, r, p, 64)
	if err != nil {
		return nil, nil, err
	}
	return kb[:32], kb[32:], nil
}

// poly1305AES is restic's MAC: Poly1305 keyed with r and AES_k(nonce).
func poly1305AES(mac, nonce, msg []byte) ([]byte, error) {
	block, err := aes.NewCipher(mac[:16])
	if err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:16], mac[16:])
	block.Encrypt(key[16:], nonce)
	var out [16]byte
	poly1305.Sum(&out, msg, &key)
	return out[:], nil
}

func ctr(key, iv, in []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(in))
	cipher.NewCTR(block, iv).XORKeyStream(out, in)
	return out, nil
}
