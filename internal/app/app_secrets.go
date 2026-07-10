package app

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	appSecretPurpose        = "app-secret"
	dataKeyCheckName        = "data-encryption-key-check-v1"
	dataKeyCheckPlaintext   = "pikpak2directlink:data-key:v1"
	linuxDoClientSecretName = "linuxdo-client-secret"
)

type appSecretStore struct {
	db     *sql.DB
	cipher *SecretCipher
}

func newAppSecretStore(db *sql.DB, cipher *SecretCipher) *appSecretStore {
	return &appSecretStore{db: db, cipher: cipher}
}

func (s *appSecretStore) get(key string) (string, bool, error) {
	var ciphertext string
	err := s.db.QueryRow(`SELECT ciphertext FROM app_secrets WHERE key=?`, key).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plaintext, err := s.cipher.Decrypt(appSecretPurpose, key, ciphertext)
	if err != nil {
		return "", false, err
	}
	return string(plaintext), true, nil
}

func (s *appSecretStore) set(key, value string) error {
	ciphertext, err := s.cipher.Encrypt(appSecretPurpose, key, []byte(value))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO app_secrets(key,ciphertext,updated_at) VALUES(?,?,?)
		 ON CONFLICT(key) DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=excluded.updated_at`,
		key, ciphertext, time.Now().Unix(),
	)
	return err
}

func (s *appSecretStore) delete(key string) error {
	_, err := s.db.Exec(`DELETE FROM app_secrets WHERE key=?`, key)
	return err
}

func (s *appSecretStore) ensureKeyCheck() error {
	value, ok, err := s.get(dataKeyCheckName)
	if err != nil {
		return fmt.Errorf("verify data encryption key: %w", err)
	}
	if !ok {
		return s.set(dataKeyCheckName, dataKeyCheckPlaintext)
	}
	if value != dataKeyCheckPlaintext {
		return errors.New("data encryption key check has unexpected contents")
	}
	var ciphertext string
	if err := s.db.QueryRow(`SELECT ciphertext FROM app_secrets WHERE key=?`, dataKeyCheckName).Scan(&ciphertext); err != nil {
		return err
	}
	rotate, err := s.cipher.NeedsRotation(ciphertext)
	if err != nil {
		return err
	}
	if rotate {
		return s.set(dataKeyCheckName, dataKeyCheckPlaintext)
	}
	return nil
}

func (s *appSecretStore) rotateSecrets() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT key,ciphertext FROM app_secrets ORDER BY key`)
	if err != nil {
		return err
	}
	type update struct {
		key        string
		original   string
		ciphertext string
	}
	var updates []update
	for rows.Next() {
		var key, ciphertext string
		if err := rows.Scan(&key, &ciphertext); err != nil {
			rows.Close()
			return err
		}
		plaintext, err := s.cipher.Decrypt(appSecretPurpose, key, ciphertext)
		if err != nil {
			rows.Close()
			return fmt.Errorf("decrypt app secret %q for rotation: %w", key, err)
		}
		needsRotation, err := s.cipher.NeedsRotation(ciphertext)
		if err != nil {
			rows.Close()
			return err
		}
		if !needsRotation {
			continue
		}
		rotated, err := s.cipher.Encrypt(appSecretPurpose, key, plaintext)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{key: key, original: ciphertext, ciphertext: rotated})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range updates {
		result, err := tx.Exec(
			`UPDATE app_secrets SET ciphertext=?,updated_at=? WHERE key=? AND ciphertext=?`,
			item.ciphertext, time.Now().Unix(), item.key, item.original,
		)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("app secret %q changed during rotation", item.key)
		}
	}
	return tx.Commit()
}

func (s *appSecretStore) migrateLinuxDoSecret(settings *settingsStore) error {
	if _, exists, err := s.get(linuxDoClientSecretName); err != nil {
		return err
	} else if exists {
		return settings.delete(settingKeyLinuxDoClientSecret)
	}
	legacy := settings.getString(settingKeyLinuxDoClientSecret, "")
	if legacy == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ciphertext, err := s.cipher.Encrypt(appSecretPurpose, linuxDoClientSecretName, []byte(legacy))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO app_secrets(key,ciphertext,updated_at) VALUES(?,?,?)`,
		linuxDoClientSecretName, ciphertext, time.Now().Unix(),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM settings WHERE key=?`, settingKeyLinuxDoClientSecret); err != nil {
		return err
	}
	return tx.Commit()
}
