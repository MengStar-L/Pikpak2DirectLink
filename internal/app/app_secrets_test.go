package app

import (
	"bytes"
	"testing"
)

func TestAppSecretStoreRotatesAllSecretsAtomically(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	oldKey := bytes.Repeat([]byte{0x31}, secretKeySize)
	newKey := bytes.Repeat([]byte{0x32}, secretKeySize)
	oldStore := newAppSecretStore(db, newTestSecretCipher(t, oldKey))
	if err := oldStore.set("first", "first-value"); err != nil {
		t.Fatalf("set first: %v", err)
	}
	if err := oldStore.set("second", "second-value"); err != nil {
		t.Fatalf("set second: %v", err)
	}

	rotatingCipher, err := NewSecretCipher(testEncodedKey(newKey), []string{testEncodedKey(oldKey)})
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	rotatingStore := newAppSecretStore(db, rotatingCipher)
	if err := rotatingStore.rotateSecrets(); err != nil {
		t.Fatalf("rotateSecrets: %v", err)
	}
	for _, key := range []string{"first", "second"} {
		ciphertext := queryString(t, db, `SELECT ciphertext FROM app_secrets WHERE key=?`, key)
		needsRotation, err := rotatingCipher.NeedsRotation(ciphertext)
		if err != nil {
			t.Fatalf("NeedsRotation(%s): %v", key, err)
		}
		if needsRotation {
			t.Fatalf("secret %q still uses the previous key", key)
		}
	}

	if err := oldStore.set("first", "first-value-old-again"); err != nil {
		t.Fatalf("reset first with old key: %v", err)
	}
	if err := oldStore.set("second", "second-value-old-again"); err != nil {
		t.Fatalf("reset second with old key: %v", err)
	}
	firstBeforeFailure := queryString(t, db, `SELECT ciphertext FROM app_secrets WHERE key='first'`)
	if _, err := db.Exec(`UPDATE app_secrets SET ciphertext=ciphertext || 'tampered' WHERE key='second'`); err != nil {
		t.Fatalf("tamper second: %v", err)
	}
	if err := rotatingStore.rotateSecrets(); err == nil {
		t.Fatal("rotateSecrets succeeded with a corrupt secret")
	}
	if got := queryString(t, db, `SELECT ciphertext FROM app_secrets WHERE key='first'`); got != firstBeforeFailure {
		t.Fatal("rotation changed a valid secret before detecting corruption")
	}
}
