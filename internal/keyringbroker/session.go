package keyringbroker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/godbus/dbus/v5"
)

// randReader is the broker's randomness (a test var).
var randReader io.Reader = rand.Reader

const (
	algPlain = "plain"
	algDH    = "dh-ietf1024-sha256-aes128-cbc-pkcs7"
)

// session is one client's Secret Service session: how secrets are sent to
// it. plain sends them as they are (the socket is the jail's own); the DH
// algorithm, which libsecret prefers, encrypts each under a key agreed with
// the client.
type session struct {
	key []byte // AES-128 key; nil for plain
}

// ietf1024 is the 1024-bit MODP group of RFC 2409 §6.2, which the Secret
// Service's DH algorithm uses (generator 2).
var ietf1024, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1"+
		"29024E088A67CC74020BBEA63B139B22514A08798E3404DD"+
		"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245"+
		"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"+
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381"+
		"FFFFFFFFFFFFFFFF", 16)

func newSession(alg string, in dbus.Variant) (*session, dbus.Variant, error) {
	switch alg {
	case algPlain:
		return &session{}, dbus.MakeVariant(""), nil
	case algDH:
		peerBytes, ok := in.Value().([]byte)
		if !ok {
			return nil, dbus.Variant{}, errors.New("the DH algorithm takes the client's public key (ay)")
		}
		peer := new(big.Int).SetBytes(peerBytes)
		one := big.NewInt(1)
		pm1 := new(big.Int).Sub(ietf1024, one)
		if peer.Cmp(one) <= 0 || peer.Cmp(pm1) >= 0 {
			return nil, dbus.Variant{}, errors.New("the client's public key is out of range")
		}
		x, err := rand.Int(randReader, pm1)
		if err != nil {
			return nil, dbus.Variant{}, err
		}
		x.Add(x, one)
		pub := new(big.Int).Exp(big.NewInt(2), x, ietf1024)
		shared := new(big.Int).Exp(peer, x, ietf1024).FillBytes(make([]byte, 128))
		key, err := hkdf.Key(sha256.New, shared, nil, "", 16)
		if err != nil {
			return nil, dbus.Variant{}, err
		}
		return &session{key: key}, dbus.MakeVariant(pub.FillBytes(make([]byte, 128))), nil
	}
	return nil, dbus.Variant{}, fmt.Errorf("algorithm %q is not supported (plain, %s are)", alg, algDH)
}

// encode is v as the Secret struct this session sends.
func (s *session) encode(path dbus.ObjectPath, v plain) (secret, error) {
	ct := v.contentType
	if ct == "" {
		ct = "text/plain"
	}
	if s.key == nil {
		return secret{Session: path, Parameters: []byte{}, Value: v.value, ContentType: ct}, nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return secret{}, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(randReader, iv); err != nil {
		return secret{}, err
	}
	pad := aes.BlockSize - len(v.value)%aes.BlockSize
	buf := make([]byte, len(v.value)+pad)
	copy(buf, v.value)
	for i := len(v.value); i < len(buf); i++ {
		buf[i] = byte(pad)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf, buf)
	return secret{Session: path, Parameters: iv, Value: buf, ContentType: ct}, nil
}
