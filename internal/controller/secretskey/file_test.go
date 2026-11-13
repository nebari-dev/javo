package secretskey

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/secrets"
)

// keyText is a new random key as 32 printable characters, so a test can
// look for it in an error's text.
func keyText(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, secrets.KEKSize/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return []byte(hex.EncodeToString(b))
}

// keyFolder is a folder only the running user can use, holding the key
// file "kek" with a new key, as an admin restoring a key makes it.
func keyFolder(t *testing.T) (dir string, key []byte) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key = keyText(t)
	if err := os.WriteFile(filepath.Join(dir, "kek"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, key
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A first start makes the folder and the key: 32 random bytes in a file
// only the running user can read; later starts get the same key.
func TestFileCreatesTheKeyOnFirstStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	src := File{Dir: dir, Name: "kek", Create: true}
	first, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("the folder has mode %04o", m)
	}
	if m := mode(t, filepath.Join(dir, "kek")); m != 0o600 {
		t.Errorf("the key file has mode %04o", m)
	}
	made, err := os.ReadFile(filepath.Join(dir, "kek"))
	if err != nil || len(made) != secrets.KEKSize {
		t.Fatalf("the key file holds %d bytes, %v", len(made), err)
	}
	again, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opensTheSameKeyset(t, first, again)
	if now, _ := os.ReadFile(filepath.Join(dir, "kek")); !bytes.Equal(now, made) {
		t.Fatal("a second start changed the key")
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("the folder holds %d files, want only the key", len(left))
	}
}

// A key file the admin made (a restored key) is used and left as it is.
func TestFileUsesAnExistingKey(t *testing.T) {
	dir, want := keyFolder(t)
	got, err := File{Dir: dir, Name: "kek", Create: true}.Bytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("not the key in the file")
	}
	if now, _ := os.ReadFile(filepath.Join(dir, "kek")); !bytes.Equal(now, want) {
		t.Fatal("the existing key file changed")
	}
}

// Without Create a missing key is an error, never a new key or folder.
func TestFileWithoutCreateOnlyReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if _, err := (File{Dir: dir, Name: "kek"}).Bytes(context.Background()); err == nil {
		t.Fatal("a missing folder gave a key")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the folder was made: %v", err)
	}
	dir, _ = keyFolder(t)
	if _, err := (File{Dir: dir, Name: "sessions"}).Bytes(context.Background()); err == nil {
		t.Fatal("a missing file gave a key")
	}
	if _, err := os.Lstat(filepath.Join(dir, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("the file was made: %v", err)
	}
}

// New makes the value of a key that is not 32 random bytes (the backup
// password).
func TestFileCreatesWhatNewMakes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	want := keyText(t)
	src := File{Dir: dir, Name: "backup-password", Create: true, New: func() ([]byte, error) { return want, nil }}
	for range 2 {
		got, err := src.Bytes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("not what New made")
		}
	}
}

// Processes starting at once all use the key of the one that made it first.
func TestFileUsesTheKeyAnotherProcessMadeFirst(t *testing.T) {
	src := File{Dir: filepath.Join(t.TempDir(), "keys"), Name: "kek", Create: true}
	const n = 8
	keys, errs := make([][]byte, n), make([]error, n)
	done := make(chan int)
	for i := range n {
		go func() {
			keys[i], errs[i] = src.Bytes(context.Background())
			done <- i
		}()
	}
	for range n {
		<-done
	}
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if len(keys[i]) != secrets.KEKSize || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("start %d got another key", i)
		}
	}
}

// A key that anyone but the running user could have read or replaced is
// refused, with or without Create, and neither it nor its folder is
// changed. The error names the fault and never holds the key.
func TestFileRefusesAKeyOthersCanReach(t *testing.T) {
	for _, c := range []struct {
		name string
		// harm makes the folder dir, holding the key file "kek", unsafe
		// and returns the folder to load from.
		harm func(t *testing.T, dir string) string
		want string
	}{
		{"file readable by its group", func(t *testing.T, dir string) string {
			chmod(t, filepath.Join(dir, "kek"), 0o640)
			return dir
		}, "0640"},
		{"file readable by others", func(t *testing.T, dir string) string {
			chmod(t, filepath.Join(dir, "kek"), 0o604)
			return dir
		}, "0604"},
		{"file writable by its group", func(t *testing.T, dir string) string {
			chmod(t, filepath.Join(dir, "kek"), 0o620)
			return dir
		}, "0620"},
		{"folder readable by its group", func(t *testing.T, dir string) string {
			chmod(t, dir, 0o750)
			return dir
		}, "0750"},
		{"folder writable by others", func(t *testing.T, dir string) string {
			chmod(t, dir, 0o702)
			return dir
		}, "0702"},
		{"file is a symbolic link", func(t *testing.T, dir string) string {
			rename(t, filepath.Join(dir, "kek"), filepath.Join(dir, "real"))
			symlink(t, "real", filepath.Join(dir, "kek"))
			return dir
		}, "symbolic link"},
		{"file is a symbolic link out of the folder", func(t *testing.T, dir string) string {
			outside := filepath.Join(filepath.Dir(dir), "real")
			rename(t, filepath.Join(dir, "kek"), outside)
			symlink(t, outside, filepath.Join(dir, "kek"))
			return dir
		}, "symbolic link"},
		{"folder is a symbolic link", func(t *testing.T, dir string) string {
			link := filepath.Join(filepath.Dir(dir), "link")
			symlink(t, dir, link)
			return link
		}, "symbolic link"},
		{"file is a folder", func(t *testing.T, dir string) string {
			rename(t, filepath.Join(dir, "kek"), filepath.Join(dir, "real"))
			if err := os.Mkdir(filepath.Join(dir, "kek"), 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "not a regular file"},
		{"owned by another user", func(t *testing.T, dir string) string {
			me := euid
			euid = func() int { return me() + 1 }
			t.Cleanup(func() { euid = me })
			return dir
		}, "not owned"},
	} {
		for _, create := range []bool{false, true} {
			t.Run(c.name, func(t *testing.T) {
				dir, key := keyFolder(t)
				from := c.harm(t, dir)
				before := tree(t, dir)
				src := File{Dir: from, Name: "kek", Create: create}
				b, err := src.Bytes(context.Background())
				if err == nil || b != nil {
					t.Fatalf("Bytes gave a key (%d bytes), %v", len(b), err)
				}
				if _, lerr := src.Load(context.Background()); lerr == nil {
					t.Fatal("Load gave a key")
				}
				if !strings.Contains(err.Error(), c.want) {
					t.Errorf("the error does not say %q: %v", c.want, err)
				}
				if strings.Contains(err.Error(), string(key)) {
					t.Error("the error holds the key")
				}
				if after := tree(t, dir); after != before {
					t.Errorf("the folder changed:\n%s\nwas\n%s", after, before)
				}
			})
		}
	}
}

// A key of the wrong length is refused without its text in the error.
func TestFileErrorsNeverHoldTheKey(t *testing.T) {
	dir, key := keyFolder(t)
	short := key[:len(key)-1]
	if err := os.WriteFile(filepath.Join(dir, "kek"), short, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := File{Dir: dir, Name: "kek"}.Load(context.Background())
	if err == nil {
		t.Fatal("a 31-byte key was accepted")
	}
	if strings.Contains(err.Error(), string(short)) {
		t.Error("the error holds the key")
	}
}

// A name that is a path could read a key out of the folder.
func TestFileNameIsNotAPath(t *testing.T) {
	dir, _ := keyFolder(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../kek", "sub/../kek", filepath.Join(dir, "kek"), "", ".", ".."} {
		if b, err := (File{Dir: sub, Name: name, Create: true}).Bytes(context.Background()); err == nil || b != nil {
			t.Errorf("name %q gave a key", name)
		}
	}
}

func chmod(t *testing.T, path string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, m); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// tree lists dir itself and what is in it, with types, modes and sizes.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path + " " + fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
