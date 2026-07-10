package app

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAccountStoreRoundTripOrderAndMutations(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x71}, secretKeySize))
	store := newAccountStore(db, cipher)

	first := testAccountRecord("first", "first@example.com", "first-plain-password", 1)
	second := testAccountRecord("second", "second@example.com", "second-plain-password", 2)
	if err := store.Replace([]accountRecord{second, first}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	assertAccountRecords(t, store, []accountRecord{second, first})
	assertAccountOrder(t, db, []string{"second", "first"})
	assertEncryptedColumn(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`,
		"first", first.Password,
	)

	session := store.SessionStore(first.ID)
	if err := session.Save([]byte(`{"refresh_token":"session-plain-token"}`)); err != nil {
		t.Fatalf("Save session: %v", err)
	}

	first.Username = "renamed@example.com"
	first.Password = "updated-plain-password"
	if err := store.Replace([]accountRecord{first, second}); err != nil {
		t.Fatalf("Replace reordered accounts: %v", err)
	}
	assertAccountRecords(t, store, []accountRecord{first, second})
	assertAccountOrder(t, db, []string{"first", "second"})
	if got, err := session.Load(); err != nil || string(got) != `{"refresh_token":"session-plain-token"}` {
		t.Fatalf("session after Replace = %q, %v", got, err)
	}

	third := testAccountRecord("third", "third@example.com", "third-password", 3)
	if err := store.Insert(third); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	third.Status = AccountFailed
	third.LastError = "login failed"
	third.Password = "third-password-updated"
	if err := store.Update(third); err != nil {
		t.Fatalf("Update: %v", err)
	}
	assertAccountRecords(t, store, []accountRecord{first, second, third})

	if err := store.Delete(second.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertAccountRecords(t, store, []accountRecord{first, third})
	if err := store.Update(second); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Update missing error = %v, want sql.ErrNoRows", err)
	}
	if err := store.Delete(second.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Delete missing error = %v, want sql.ErrNoRows", err)
	}
}

func TestAccountStoreReplaceRollsBackOnInvalidRecord(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	store := newAccountStore(db, newTestSecretCipher(t, bytes.Repeat([]byte{0x72}, secretKeySize)))

	original := testAccountRecord("original", "original@example.com", "original-password", 1)
	if err := store.Insert(original); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	originalEnvelope := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, original.ID,
	)

	changed := original
	changed.Password = "must-not-be-committed"
	invalid := testAccountRecord("invalid", "invalid@example.com", "invalid-password", 2)
	invalid.TrafficUsed = -1
	if err := store.Replace([]accountRecord{changed, invalid}); err == nil {
		t.Fatal("Replace succeeded with a negative traffic counter")
	}

	assertAccountRecords(t, store, []accountRecord{original})
	if got := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, original.ID,
	); got != originalEnvelope {
		t.Fatal("Replace changed an existing envelope before rolling back")
	}
}

func TestDatabasePikPakSessionStoreEncryptsAndCascades(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x73}, secretKeySize))
	store := newAccountStore(db, cipher)
	record := testAccountRecord("session-account", "session@example.com", "account-password", 1)
	if err := store.Insert(record); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	sessions := store.SessionStore(record.ID)
	if _, err := sessions.Load(); !os.IsNotExist(err) {
		t.Fatalf("Load missing error = %v, want os.ErrNotExist", err)
	}
	plaintext := []byte(`{"access_token":"access-plain","refresh_token":"refresh-plain"}`)
	if err := sessions.Save(plaintext); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertEncryptedColumn(t, db,
		`SELECT session_encrypted FROM pikpak_account_sessions WHERE account_id = ?`,
		record.ID, string(plaintext),
	)
	got, err := sessions.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Load = %q, want %q", got, plaintext)
	}

	if err := store.SessionStore("missing-account").Save([]byte("session")); err == nil {
		t.Fatal("Save succeeded for a missing account")
	}
	if err := store.Delete(record.ID); err != nil {
		t.Fatalf("Delete account: %v", err)
	}
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pikpak_account_sessions WHERE account_id = ?`, record.ID,
	).Scan(&count); err != nil {
		t.Fatalf("count session rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("session row count after account delete = %d, want 0", count)
	}
}

func TestAccountStoreRejectsWrongAADAndKey(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x74}, secretKeySize))
	store := newAccountStore(db, cipher)
	record := testAccountRecord("aad-account", "aad@example.com", "bound-password", 1)
	if err := store.Insert(record); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := store.SessionStore(record.ID).Save([]byte("bound-session")); err != nil {
		t.Fatalf("Save session: %v", err)
	}

	passwordEnvelope := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, record.ID,
	)
	if _, err := cipher.Decrypt(accountSessionSecretPurpose, record.ID, passwordEnvelope); err == nil {
		t.Fatal("password envelope decrypted with session purpose")
	}
	if _, err := cipher.Decrypt(accountPasswordSecretPurpose, "other-account", passwordEnvelope); err == nil {
		t.Fatal("password envelope decrypted for another account")
	}

	wrongCipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x75}, secretKeySize))
	wrongStore := newAccountStore(db, wrongCipher)
	if _, err := wrongStore.List(); err == nil {
		t.Fatal("List succeeded with the wrong key")
	}
	if _, err := wrongStore.SessionStore(record.ID).Load(); err == nil {
		t.Fatal("session Load succeeded with the wrong key")
	}
}

func TestAccountStoreRotatesPasswordsAndSessions(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	oldKey := bytes.Repeat([]byte{0x76}, secretKeySize)
	newKey := bytes.Repeat([]byte{0x77}, secretKeySize)
	oldStore := newAccountStore(db, newTestSecretCipher(t, oldKey))
	record := testAccountRecord("rotate-account", "rotate@example.com", "rotate-password", 1)
	if err := oldStore.Insert(record); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := oldStore.SessionStore(record.ID).Save([]byte("rotate-session")); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	oldPasswordEnvelope := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, record.ID,
	)
	oldSessionEnvelope := queryString(t, db,
		`SELECT session_encrypted FROM pikpak_account_sessions WHERE account_id = ?`, record.ID,
	)

	rotatingCipher, err := NewSecretCipher(testEncodedKey(newKey), []string{testEncodedKey(oldKey)})
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	rotatingStore := newAccountStore(db, rotatingCipher)
	if err := rotatingStore.RotateSecrets(); err != nil {
		t.Fatalf("RotateSecrets: %v", err)
	}

	newPasswordEnvelope := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, record.ID,
	)
	newSessionEnvelope := queryString(t, db,
		`SELECT session_encrypted FROM pikpak_account_sessions WHERE account_id = ?`, record.ID,
	)
	if newPasswordEnvelope == oldPasswordEnvelope || newSessionEnvelope == oldSessionEnvelope {
		t.Fatal("rotation did not replace every old envelope")
	}
	for name, envelope := range map[string]string{
		"password": newPasswordEnvelope,
		"session":  newSessionEnvelope,
	} {
		needsRotation, err := rotatingCipher.NeedsRotation(envelope)
		if err != nil {
			t.Fatalf("NeedsRotation(%s): %v", name, err)
		}
		if needsRotation {
			t.Fatalf("%s still needs rotation", name)
		}
	}
	assertAccountRecords(t, rotatingStore, []accountRecord{record})
	if got, err := rotatingStore.SessionStore(record.ID).Load(); err != nil || string(got) != "rotate-session" {
		t.Fatalf("rotated session = %q, %v", got, err)
	}
}

func TestAccountStoreRotationValidatesAllSecretsBeforeWriting(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	oldKey := bytes.Repeat([]byte{0x78}, secretKeySize)
	newKey := bytes.Repeat([]byte{0x79}, secretKeySize)
	oldStore := newAccountStore(db, newTestSecretCipher(t, oldKey))
	first := testAccountRecord("first", "first@example.com", "first-password", 1)
	second := testAccountRecord("second", "second@example.com", "second-password", 2)
	if err := oldStore.Replace([]accountRecord{first, second}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := oldStore.SessionStore(second.ID).Save([]byte("second-session")); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	firstEnvelope := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, first.ID,
	)
	if _, err := db.Exec(
		`UPDATE pikpak_account_sessions SET session_encrypted = session_encrypted || 'tampered' WHERE account_id = ?`,
		second.ID,
	); err != nil {
		t.Fatalf("tamper session: %v", err)
	}

	rotatingCipher, err := NewSecretCipher(testEncodedKey(newKey), []string{testEncodedKey(oldKey)})
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	if err := newAccountStore(db, rotatingCipher).RotateSecrets(); err == nil {
		t.Fatal("RotateSecrets succeeded with a corrupt session")
	}
	if got := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, first.ID,
	); got != firstEnvelope {
		t.Fatal("rotation rewrote a valid password before detecting a corrupt session")
	}

	unknownKeyStore := newAccountStore(db, newTestSecretCipher(t, newKey))
	if err := unknownKeyStore.RotateSecrets(); err == nil {
		t.Fatal("RotateSecrets succeeded without the old key")
	}
	if got := queryString(t, db,
		`SELECT password_encrypted FROM pikpak_accounts WHERE id = ?`, first.ID,
	); got != firstEnvelope {
		t.Fatal("wrong-key rotation changed stored data")
	}
}

func TestStagingSessionStoreCopiesAndDeletesData(t *testing.T) {
	store := newStagingSessionStore()
	if _, err := store.Load(); !os.IsNotExist(err) {
		t.Fatalf("Load missing error = %v, want os.ErrNotExist", err)
	}
	original := []byte("temporary-session")
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	original[0] = 'X'
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded) != "temporary-session" {
		t.Fatalf("Load = %q", loaded)
	}
	loaded[0] = 'Y'
	loadedAgain, err := store.Load()
	if err != nil || string(loadedAgain) != "temporary-session" {
		t.Fatalf("second Load = %q, %v", loadedAgain, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load(); !os.IsNotExist(err) {
		t.Fatalf("Load after Delete error = %v, want os.ErrNotExist", err)
	}
}

func openAccountStoreTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testAccountRecord(id, username, password string, day int) accountRecord {
	createdAt := time.Date(2026, time.July, day, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(4 * time.Hour)
	return accountRecord{
		ID:                    id,
		Username:              username,
		Password:              password,
		SessionFile:           "legacy/" + id + ".json",
		Status:                AccountAvailable,
		Premium:               day%2 == 0,
		PremiumType:           "vip",
		PremiumUntil:          "2027-07-01T00:00:00Z",
		PremiumError:          "premium error",
		PremiumCheckedAt:      "2026-07-01T00:00:00Z",
		TrafficLimit:          700 * bytesPerGB,
		TrafficUsed:           int64(day) * bytesPerGB,
		TrafficPeriod:         "2026-07",
		LastError:             "last error",
		LastFailedAt:          "2026-06-30T00:00:00Z",
		CredentialCheckedAt:   "2026-07-01T00:00:00Z",
		CredentialNextCheckAt: "2026-07-02T00:00:00Z",
		CredentialCheckError:  "credential error",
		ParseErrors: []ParseError{{
			Time:    "2026-07-01T01:00:00Z",
			JobID:   "job-" + id,
			Message: "parse error",
		}},
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

func assertAccountRecords(t *testing.T, store *accountStore, want []accountRecord) {
	t.Helper()
	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %#v, want %#v", got, want)
	}
}

func assertAccountOrder(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT id, sort_order FROM pikpak_accounts ORDER BY sort_order`)
	if err != nil {
		t.Fatalf("query account order: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var (
			id    string
			order int
		)
		if err := rows.Scan(&id, &order); err != nil {
			t.Fatalf("scan account order: %v", err)
		}
		if order != len(got) {
			t.Fatalf("sort_order for %q = %d, want %d", id, order, len(got))
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account order: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("account order = %v, want %v", got, want)
	}
}

func assertEncryptedColumn(t *testing.T, db *sql.DB, query, id, plaintext string) {
	t.Helper()
	envelope := queryString(t, db, query, id)
	if envelope == plaintext || strings.Contains(envelope, plaintext) {
		t.Fatalf("encrypted database column contains plaintext %q", plaintext)
	}
	if !strings.HasPrefix(envelope, secretEnvelopeVersion+".") {
		t.Fatalf("database value is not a secret envelope: %q", envelope)
	}
}

func queryString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query string: %v", err)
	}
	return value
}

func testEncodedKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}
