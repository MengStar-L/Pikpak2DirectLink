package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSecretCipherEncryptDecrypt(t *testing.T) {
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x11}, secretKeySize))
	plaintext := []byte("pikpak password")

	first, err := cipher.Encrypt("pikpak-account-password", "account-1", plaintext)
	if err != nil {
		t.Fatalf("encrypt first: %v", err)
	}
	second, err := cipher.Encrypt("pikpak-account-password", "account-1", plaintext)
	if err != nil {
		t.Fatalf("encrypt second: %v", err)
	}
	if first == second {
		t.Fatal("encrypting the same value must use a fresh nonce")
	}
	if !strings.HasPrefix(first, "v1."+cipher.CurrentKeyID()+".") {
		t.Fatalf("unexpected envelope: %q", first)
	}

	got, err := cipher.Decrypt("pikpak-account-password", "account-1", first)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt = %q, want %q", got, plaintext)
	}
	rotation, err := cipher.NeedsRotation(first)
	if err != nil {
		t.Fatalf("rotation check: %v", err)
	}
	if rotation {
		t.Fatal("current-key envelope must not need rotation")
	}
}

func TestSecretCipherBindsAADToPurposeAndRecord(t *testing.T) {
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x22}, secretKeySize))
	envelope, err := cipher.Encrypt("session", "account-1", []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	for _, tc := range []struct {
		name     string
		purpose  string
		recordID string
	}{
		{name: "different purpose", purpose: "password", recordID: "account-1"},
		{name: "different record", purpose: "session", recordID: "account-2"},
		{name: "missing purpose", purpose: "", recordID: "account-1"},
		{name: "missing record", purpose: "session", recordID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cipher.Decrypt(tc.purpose, tc.recordID, envelope); err == nil {
				t.Fatal("decrypt unexpectedly succeeded")
			}
		})
	}
}

func TestSecretCipherReadsPreviousKeyAndWritesCurrent(t *testing.T) {
	previousKey := bytes.Repeat([]byte{0x33}, secretKeySize)
	currentKey := bytes.Repeat([]byte{0x44}, secretKeySize)
	previous := newTestSecretCipher(t, previousKey)
	oldEnvelope, err := previous.Encrypt("session", "account-1", []byte("old session"))
	if err != nil {
		t.Fatalf("encrypt old value: %v", err)
	}

	rotated, err := NewSecretCipher(encodeTestKey(currentKey), []string{encodeTestKey(previousKey)})
	if err != nil {
		t.Fatalf("construct rotated cipher: %v", err)
	}
	wantsRotation, err := rotated.NeedsRotation(oldEnvelope)
	if err != nil {
		t.Fatalf("rotation check: %v", err)
	}
	if !wantsRotation {
		t.Fatal("previous-key envelope must need rotation")
	}
	plaintext, err := rotated.Decrypt("session", "account-1", oldEnvelope)
	if err != nil {
		t.Fatalf("decrypt previous-key value: %v", err)
	}
	if string(plaintext) != "old session" {
		t.Fatalf("plaintext = %q", plaintext)
	}

	newEnvelope, err := rotated.Encrypt("session", "account-1", plaintext)
	if err != nil {
		t.Fatalf("encrypt rotated value: %v", err)
	}
	if !strings.HasPrefix(newEnvelope, "v1."+rotated.CurrentKeyID()+".") {
		t.Fatalf("new envelope does not use current key: %q", newEnvelope)
	}
}

func TestSecretCipherKeyValidation(t *testing.T) {
	validKey := encodeTestKey(bytes.Repeat([]byte{0x55}, secretKeySize))
	tests := []struct {
		name string
		key  string
	}{
		{name: "missing"},
		{name: "short decoded key", key: base64.StdEncoding.EncodeToString([]byte("too short"))},
		{name: "raw standard base64", key: strings.TrimRight(validKey, "=")},
		{name: "URL base64", key: base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, secretKeySize))},
		{name: "leading whitespace", key: " " + validKey},
		{name: "embedded newline", key: validKey[:8] + "\n" + validKey[8:]},
		{name: "invalid alphabet", key: strings.Repeat("!", len(validKey))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSecretCipher(tc.key, nil); err == nil {
				t.Fatal("invalid key was accepted")
			}
		})
	}

	if _, err := NewSecretCipher(validKey, []string{validKey}); err == nil {
		t.Fatal("current key duplicated as previous key was accepted")
	}
	if _, err := NewSecretCipher(validKey, []string{""}); err == nil {
		t.Fatal("empty previous key was accepted")
	}
}

func TestSecretCipherRejectsMalformedOrTamperedEnvelope(t *testing.T) {
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x66}, secretKeySize))
	envelope, err := cipher.Encrypt("password", "account-1", []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	parts := strings.Split(envelope, ".")
	unknownID := strings.Repeat("a", sha256.Size*2)
	if unknownID == cipher.CurrentKeyID() {
		unknownID = strings.Repeat("b", sha256.Size*2)
	}

	malformed := []string{
		"",
		"v1.too.few",
		strings.Join([]string{"v2", parts[1], parts[2], parts[3]}, "."),
		strings.Join([]string{"v1", "NOT-HEX", parts[2], parts[3]}, "."),
		strings.Join([]string{"v1", unknownID, parts[2], parts[3]}, "."),
		strings.Join([]string{"v1", parts[1], "AA", parts[3]}, "."),
		strings.Join([]string{"v1", parts[1], parts[2] + "=", parts[3]}, "."),
		strings.Join([]string{"v1", parts[1], parts[2], "AA"}, "."),
	}
	for i, value := range malformed {
		if _, err := cipher.NeedsRotation(value); err == nil {
			t.Errorf("malformed envelope %d passed rotation check", i)
		}
		if _, err := cipher.Decrypt("password", "account-1", value); err == nil {
			t.Errorf("malformed envelope %d decrypted", i)
		}
	}

	tampered, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	tampered[0] ^= 0x01
	tamperedEnvelope := strings.Join([]string{
		parts[0],
		parts[1],
		parts[2],
		base64.RawURLEncoding.EncodeToString(tampered),
	}, ".")
	if _, err := cipher.Decrypt("password", "account-1", tamperedEnvelope); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}

func TestSecretCipherKeyIDIsDeterministic(t *testing.T) {
	key := bytes.Repeat([]byte{0x77}, secretKeySize)
	first := newTestSecretCipher(t, key)
	second := newTestSecretCipher(t, key)
	if first.CurrentKeyID() == "" || first.CurrentKeyID() != second.CurrentKeyID() {
		t.Fatalf("key IDs differ: %q and %q", first.CurrentKeyID(), second.CurrentKeyID())
	}
}

func TestLoadConfigReadsEncryptionKeys(t *testing.T) {
	current := encodeTestKey(bytes.Repeat([]byte{0x88}, secretKeySize))
	previousOne := encodeTestKey(bytes.Repeat([]byte{0x89}, secretKeySize))
	previousTwo := encodeTestKey(bytes.Repeat([]byte{0x8a}, secretKeySize))
	t.Setenv("DATA_ENCRYPTION_KEY", current)
	t.Setenv("DATA_ENCRYPTION_PREVIOUS_KEYS", previousOne+","+previousTwo)

	cfg := LoadConfig()
	if cfg.DataEncryptionKey != current {
		t.Fatalf("current key = %q", cfg.DataEncryptionKey)
	}
	if len(cfg.DataEncryptionPreviousKeys) != 2 ||
		cfg.DataEncryptionPreviousKeys[0] != previousOne ||
		cfg.DataEncryptionPreviousKeys[1] != previousTwo {
		t.Fatalf("previous keys = %#v", cfg.DataEncryptionPreviousKeys)
	}
}

func newTestSecretCipher(t *testing.T, key []byte) *SecretCipher {
	t.Helper()
	cipher, err := NewSecretCipher(encodeTestKey(key), nil)
	if err != nil {
		t.Fatalf("construct secret cipher: %v", err)
	}
	return cipher
}

func encodeTestKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}
