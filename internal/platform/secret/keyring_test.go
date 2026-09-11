package secret

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func encoded(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestKeyringEncryptionUsesRandomNonceAndAAD(t *testing.T) {
	k, err := Parse("v1:"+encoded(1), "v1")
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD(7, "glm", "glm-4.7-flash", 1)
	c1, n1, v, err := k.Encrypt([]byte("sentinel-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	c2, n2, _, err := k.Encrypt([]byte("sentinel-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(n1, n2) || bytes.Equal(c1, c2) {
		t.Fatal("credential envelopes were reused")
	}
	plain, err := k.Decrypt(c1, n1, v, aad)
	if err != nil || string(plain) != "sentinel-secret" {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
	if _, err := k.Decrypt(c1, n1, v, AAD(8, "glm", "glm-4.7-flash", 1)); err == nil {
		t.Fatal("cross-user decrypt succeeded")
	}
	c1[0] ^= 1
	if _, err := k.Decrypt(c1, n1, v, aad); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}

func TestKeyringRotationAndValidation(t *testing.T) {
	old, _ := Parse("v1:"+encoded(1), "v1")
	aad := AAD(1, "glm", "glm-4.7-flash", 1)
	ciphertext, nonce, version, _ := old.Encrypt([]byte("key"), aad)
	rotated, err := Parse("v1:"+encoded(1)+",v2:"+encoded(2), "v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Decrypt(ciphertext, nonce, version, aad); err != nil {
		t.Fatal(err)
	}
	_, _, active, _ := rotated.Encrypt([]byte("new"), aad)
	if active != "v2" {
		t.Fatalf("active=%q", active)
	}
	for _, raw := range []string{"", "v1:bad", "v1:" + encoded(1) + ",v1:" + encoded(2)} {
		if _, err := Parse(raw, "v1"); err == nil || strings.Contains(err.Error(), encoded(1)) {
			t.Fatalf("unsafe validation for %q: %v", raw, err)
		}
	}
}

func TestKeyringRejectsUnsafeVersions(t *testing.T) {
	for _, version := range []string{"unsafe/version", "with space", strings.Repeat("v", 33)} {
		if _, err := Parse(version+":"+encoded(1), version); err == nil {
			t.Fatalf("accepted unsafe key version %q", version)
		}
	}
}
