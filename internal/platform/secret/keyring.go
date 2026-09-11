package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Keyring encrypts user credentials with versioned AES-256-GCM keys. The
// version is stored beside the ciphertext so old rows remain decryptable while
// the active key is rotated.
type Keyring struct {
	active string
	keys   map[string][]byte
}

func Parse(encoded, active string) (*Keyring, error) {
	active = strings.TrimSpace(active)
	if active == "" {
		return nil, errors.New("AI credential active key version is required")
	}
	keys := make(map[string][]byte)
	for _, entry := range strings.Split(encoded, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(parts) != 2 || !validVersion(strings.TrimSpace(parts[0])) {
			return nil, errors.New("invalid AI credential keyring")
		}
		version := strings.TrimSpace(parts[0])
		if _, exists := keys[version]; exists {
			return nil, errors.New("duplicate AI credential key version")
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil || len(key) != 32 {
			return nil, errors.New("AI credential keys must be base64 encoded 32-byte values")
		}
		keys[version] = key
	}
	if _, exists := keys[active]; !exists {
		return nil, errors.New("AI credential active key version is not loaded")
	}
	return &Keyring{active: active, keys: keys}, nil
}

func validVersion(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func (k *Keyring) ActiveVersion() string { return k.active }

func (k *Keyring) Encrypt(plaintext []byte, aad string) (ciphertext, nonce []byte, version string, err error) {
	if k == nil || len(plaintext) == 0 {
		return nil, nil, "", errors.New("empty credential")
	}
	block, err := aes.NewCipher(k.keys[k.active])
	if err != nil {
		return nil, nil, "", errors.New("initialize credential encryption")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, "", errors.New("initialize credential encryption")
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", errors.New("generate credential nonce")
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, []byte(aad))
	return ciphertext, nonce, k.active, nil
}

func (k *Keyring) Decrypt(ciphertext, nonce []byte, version, aad string) ([]byte, error) {
	if k == nil {
		return nil, errors.New("credential keyring unavailable")
	}
	key, ok := k.keys[version]
	if !ok {
		return nil, errors.New("credential master key version unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize credential decryption")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid credential envelope")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, errors.New("decrypt credential")
	}
	return plaintext, nil
}

func AAD(userID uint64, provider, model string, version uint64) string {
	return fmt.Sprintf("signalwatch-ai-credential/v1/%d/%s/%s/%d", userID, provider, model, version)
}
