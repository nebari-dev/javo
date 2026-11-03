// Package secrets seals and opens the values of people's secrets with Google
// Tink. Nothing here is hand-rolled cryptography: every primitive, nonce and
// key format is Tink's.
//
// The scheme is envelope encryption with a public-key data keyset:
//
//   - The data keyset is an HPKE keyset (RFC 9180: X25519, HKDF-SHA256,
//     AES-256-GCM). Each value is sealed under a fresh key that HPKE derives
//     for that value alone and encrypted with AES-256-GCM.
//   - Its private half is stored in the database only encrypted by the
//     key-encryption key (KEK), which never touches the database. Its public
//     half is stored in clear: it can seal but never open.
//   - So the gateway, which takes values from people, seals with the public
//     half and holds no key that opens anything; only the controller, which
//     has the KEK, opens values, to hand them to an app's pod.
//   - Each ciphertext is bound to its row (the secret's id, owner and name, as
//     HPKE context info), so a ciphertext copied onto another row does not open.
//   - Rotation adds a new primary key: new values use it and older ones still
//     open until they are sealed again.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/tink-crypto/tink-go/v2/aead/aesgcm"
	"github.com/tink-crypto/tink-go/v2/hybrid"
	"github.com/tink-crypto/tink-go/v2/insecuresecretdataaccess"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/secretdata"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// KEK is the key-encryption key: it wraps the private half of the data
// keyset. KEKFromFile reads one from a file and KEKFromBytes takes one read
// elsewhere (the controller reads it from a Kubernetes Secret); a cloud KMS
// AEAD (Tink's KMS clients) satisfies the same interface, which is the place
// to add one.
type KEK = tink.AEAD

// KEKSize is the length of a KEK file: an AES-256 key.
const KEKSize = 32

// keysetAD binds the wrapped keyset to this use of the KEK.
var keysetAD = []byte("javo secrets data keyset v1")

// KEKFromFile reads a 32-byte AES-256-GCM key-encryption key. Its errors
// never contain key material.
func KEKFromFile(path string) (KEK, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the secrets key: %w", err)
	}
	kek, err := KEKFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("the secrets key in %s: %w", path, err)
	}
	return kek, nil
}

// KEKFromBytes makes a key-encryption key of b, which must be 32 random
// bytes. Its errors never contain key material.
func KEKFromBytes(b []byte) (KEK, error) {
	if len(b) != KEKSize {
		return nil, fmt.Errorf("it is %d bytes; it must be %d random bytes", len(b), KEKSize)
	}
	params, err := aesgcm.NewParameters(aesgcm.ParametersOpts{
		KeySizeInBytes: KEKSize, IVSizeInBytes: 12, TagSizeInBytes: 16, Variant: aesgcm.VariantNoPrefix,
	})
	if err != nil {
		return nil, err
	}
	key, err := aesgcm.NewKey(secretdata.NewBytesFromData(b, insecuresecretdataaccess.Token{}), 0, params)
	if err != nil {
		return nil, err
	}
	return aesgcm.NewAEAD(key)
}

// NewKey makes a random AES-256-GCM key of the kind KEKFromBytes makes,
// kept only in memory.
func NewKey() (tink.AEAD, error) {
	b, err := secretdata.NewBytesFromRand(KEKSize)
	if err != nil {
		return nil, err
	}
	return KEKFromBytes(b.Data(insecuresecretdataaccess.Token{}))
}

// Keyset is the data keyset as stored: Wrapped is the private half encrypted
// by the KEK, Public the public half in clear.
type Keyset struct {
	Wrapped, Public []byte
}

// AD is the row a ciphertext belongs to.
type AD struct {
	ID, Owner, Name string
}

// NebiTokenAD is the row an app's sealed Nebi token belongs to
// (store.NebiTokens): the app's id and owner. Its name is not a valid secret
// name, so no person's secret shares it.
func NebiTokenAD(appID, owner string) AD {
	return AD{ID: appID, Owner: owner, Name: "nebi-token"}
}

// bytes encodes the row unambiguously (a JSON array), so no two rows share
// an encoding.
func (a AD) bytes() []byte {
	b, _ := json.Marshal([]string{"javo secret v1", a.ID, a.Owner, a.Name})
	return b
}

// template is the data key type: HPKE with X25519, HKDF-SHA256 and
// AES-256-GCM, with Tink's key id prefix so a rotated keyset finds the key.
var template = hybrid.DHKEM_X25519_HKDF_SHA256_HKDF_SHA256_AES_256_GCM_Key_Template

// NewKeyset makes a data keyset with one key.
func NewKeyset(kek KEK) (Keyset, error) {
	h, err := keyset.NewHandle(template())
	if err != nil {
		return Keyset{}, err
	}
	return wrap(h, kek)
}

// Rotate adds a new key to ks and makes it the primary: values sealed from
// then on use it, and values sealed before still open.
func Rotate(ks Keyset, kek KEK) (Keyset, error) {
	h, err := unwrap(ks, kek)
	if err != nil {
		return Keyset{}, err
	}
	m := keyset.NewManagerFromHandle(h)
	id, err := m.Add(template())
	if err != nil {
		return Keyset{}, err
	}
	if err := m.SetPrimary(id); err != nil {
		return Keyset{}, err
	}
	if h, err = m.Handle(); err != nil {
		return Keyset{}, err
	}
	return wrap(h, kek)
}

func wrap(h *keyset.Handle, kek KEK) (Keyset, error) {
	var wrapped, public bytes.Buffer
	if err := h.WriteWithAssociatedData(keyset.NewBinaryWriter(&wrapped), kek, keysetAD); err != nil {
		return Keyset{}, fmt.Errorf("wrap the data keyset: %w", err)
	}
	pub, err := h.Public()
	if err != nil {
		return Keyset{}, err
	}
	if err := pub.WriteWithNoSecrets(keyset.NewBinaryWriter(&public)); err != nil {
		return Keyset{}, err
	}
	return Keyset{Wrapped: wrapped.Bytes(), Public: public.Bytes()}, nil
}

// errKeyset hides why the keyset did not open: Tink's text is no help to an
// admin, and the cause is almost always another KEK.
var errKeyset = errors.New("the secrets keyset does not open with this key-encryption key")

func unwrap(ks Keyset, kek KEK) (*keyset.Handle, error) {
	h, err := keyset.ReadWithAssociatedData(keyset.NewBinaryReader(bytes.NewReader(ks.Wrapped)), kek, keysetAD)
	if err != nil {
		return nil, errKeyset
	}
	return h, nil
}

// Sealer encrypts values with the public half of the data keyset. It holds
// no key that opens anything.
type Sealer struct{ enc tink.HybridEncrypt }

// NewSealer reads the public half of a data keyset.
func NewSealer(public []byte) (*Sealer, error) {
	h, err := keyset.ReadWithNoSecrets(keyset.NewBinaryReader(bytes.NewReader(public)))
	if err != nil {
		return nil, fmt.Errorf("read the public secrets keyset: %w", err)
	}
	enc, err := hybrid.NewHybridEncrypt(h)
	if err != nil {
		return nil, err
	}
	return &Sealer{enc}, nil
}

// Seal encrypts value for the row ad names.
func (s *Sealer) Seal(value []byte, ad AD) ([]byte, error) {
	return s.enc.Encrypt(value, ad.bytes())
}

// Opener decrypts values; only the controller makes one.
type Opener struct {
	dec    tink.HybridDecrypt
	sealer *Sealer
}

// NewOpener unwraps the data keyset with kek.
func NewOpener(ks Keyset, kek KEK) (*Opener, error) {
	h, err := unwrap(ks, kek)
	if err != nil {
		return nil, err
	}
	dec, err := hybrid.NewHybridDecrypt(h)
	if err != nil {
		return nil, err
	}
	s, err := NewSealer(ks.Public)
	if err != nil {
		return nil, err
	}
	return &Opener{dec: dec, sealer: s}, nil
}

// ErrOpen is every failure to open a value; it carries no detail, so no
// error text can hold any part of a value.
var ErrOpen = errors.New("the secret's value does not open: it was not sealed for this row with this keyset")

// Open decrypts ciphertext sealed for the row ad names.
func (o *Opener) Open(ciphertext []byte, ad AD) ([]byte, error) {
	v, err := o.dec.Decrypt(ciphertext, ad.bytes())
	if err != nil {
		return nil, ErrOpen
	}
	return v, nil
}

// Reseal opens ciphertext and seals the value again with the primary key,
// for rotation.
func (o *Opener) Reseal(ciphertext []byte, ad AD) ([]byte, error) {
	v, err := o.Open(ciphertext, ad)
	if err != nil {
		return nil, err
	}
	defer clear(v)
	return o.sealer.Seal(v, ad)
}
