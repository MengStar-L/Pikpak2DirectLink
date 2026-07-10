package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
)

const (
	secretEnvelopeVersion = "v1"
	secretKeySize         = 32
	secretAADPrefix       = "pikpak2directlink:secret:v1"
)

var (
	errInvalidEncryptionKey = errors.New("invalid data encryption key")
	errInvalidSecretContext = errors.New("invalid secret context")
	errMalformedEnvelope    = errors.New("malformed secret envelope")
	errUnknownSecretKey     = errors.New("unknown secret encryption key")
)

// SecretCipher encrypts application secrets using the current key and can
// decrypt secrets written with the current or any configured previous key.
type SecretCipher struct {
	currentKeyID string
	current      cipher.AEAD
	keys         map[string]cipher.AEAD
}

// NewSecretCipher builds a cipher from standard Base64-encoded 32-byte keys.
// The current key is mandatory. Previous keys are read-only and exist solely
// to support online key rotation.
func NewSecretCipher(currentKey string, previousKeys []string) (*SecretCipher, error) {
	currentID, current, err := makeSecretAEAD(currentKey)
	if err != nil {
		return nil, fmt.Errorf("current key: %w", err)
	}

	keys := map[string]cipher.AEAD{currentID: current}
	for i, encoded := range previousKeys {
		keyID, aead, err := makeSecretAEAD(encoded)
		if err != nil {
			return nil, fmt.Errorf("previous key %d: %w", i+1, err)
		}
		if _, exists := keys[keyID]; exists {
			return nil, fmt.Errorf("previous key %d: %w: duplicate key", i+1, errInvalidEncryptionKey)
		}
		keys[keyID] = aead
	}

	return &SecretCipher{
		currentKeyID: currentID,
		current:      current,
		keys:         keys,
	}, nil
}

// CurrentKeyID returns the deterministic identifier embedded in newly written
// envelopes. It contains no key material.
func (c *SecretCipher) CurrentKeyID() string {
	if c == nil {
		return ""
	}
	return c.currentKeyID
}

// Encrypt returns a v1 envelope authenticated against both purpose and recordID.
func (c *SecretCipher) Encrypt(purpose, recordID string, plaintext []byte) (string, error) {
	if c == nil || c.current == nil {
		return "", errors.New("secret cipher is not initialized")
	}
	aad, err := secretAAD(purpose, recordID)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, c.current.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := c.current.Seal(nil, nonce, plaintext, aad)

	return strings.Join([]string{
		secretEnvelopeVersion,
		c.currentKeyID,
		base64.RawURLEncoding.EncodeToString(nonce),
		base64.RawURLEncoding.EncodeToString(ciphertext),
	}, "."), nil
}

// Decrypt opens an envelope using a configured current or previous key.
func (c *SecretCipher) Decrypt(purpose, recordID, envelope string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("secret cipher is not initialized")
	}
	aad, err := secretAAD(purpose, recordID)
	if err != nil {
		return nil, err
	}
	parsed, aead, err := c.parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, parsed.nonce, parsed.ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: authentication failed: %w", err)
	}
	return plaintext, nil
}

// NeedsRotation reports whether an envelope was written by a configured
// previous key. It validates the complete envelope shape and key identifier;
// authentication still occurs in Decrypt because it requires the AAD context.
func (c *SecretCipher) NeedsRotation(envelope string) (bool, error) {
	if c == nil {
		return false, errors.New("secret cipher is not initialized")
	}
	parsed, _, err := c.parseEnvelope(envelope)
	if err != nil {
		return false, err
	}
	return parsed.keyID != c.currentKeyID, nil
}

type parsedSecretEnvelope struct {
	keyID      string
	nonce      []byte
	ciphertext []byte
}

func (c *SecretCipher) parseEnvelope(envelope string) (parsedSecretEnvelope, cipher.AEAD, error) {
	parts := strings.Split(envelope, ".")
	if len(parts) != 4 {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: expected 4 fields", errMalformedEnvelope)
	}
	if parts[0] != secretEnvelopeVersion {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: unsupported version", errMalformedEnvelope)
	}
	if !validSecretKeyID(parts[1]) {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: invalid key id", errMalformedEnvelope)
	}
	aead, ok := c.keys[parts[1]]
	if !ok {
		return parsedSecretEnvelope{}, nil, errUnknownSecretKey
	}

	nonce, err := decodeRawURLField(parts[2])
	if err != nil {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: invalid nonce", errMalformedEnvelope)
	}
	if len(nonce) != aead.NonceSize() {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: invalid nonce length", errMalformedEnvelope)
	}
	ciphertext, err := decodeRawURLField(parts[3])
	if err != nil {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: invalid ciphertext", errMalformedEnvelope)
	}
	if len(ciphertext) < aead.Overhead() {
		return parsedSecretEnvelope{}, nil, fmt.Errorf("%w: ciphertext is too short", errMalformedEnvelope)
	}

	return parsedSecretEnvelope{
		keyID:      parts[1],
		nonce:      nonce,
		ciphertext: ciphertext,
	}, aead, nil
}

func makeSecretAEAD(encoded string) (string, cipher.AEAD, error) {
	key, err := decodeEncryptionKey(encoded)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(key)
	keyID := hex.EncodeToString(sum[:])

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, fmt.Errorf("%w: initialize AES: %v", errInvalidEncryptionKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, fmt.Errorf("%w: initialize GCM: %v", errInvalidEncryptionKey, err)
	}
	return keyID, aead, nil
}

func decodeEncryptionKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, fmt.Errorf("%w: value is required", errInvalidEncryptionKey)
	}
	if strings.IndexFunc(encoded, unicode.IsSpace) >= 0 {
		return nil, fmt.Errorf("%w: whitespace is not allowed", errInvalidEncryptionKey)
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: expected standard Base64", errInvalidEncryptionKey)
	}
	if len(key) != secretKeySize {
		return nil, fmt.Errorf("%w: decoded length must be %d bytes", errInvalidEncryptionKey, secretKeySize)
	}
	if base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, fmt.Errorf("%w: non-canonical Base64", errInvalidEncryptionKey)
	}
	return key, nil
}

func validSecretKeyID(keyID string) bool {
	if len(keyID) != sha256.Size*2 {
		return false
	}
	for _, ch := range keyID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func decodeRawURLField(value string) ([]byte, error) {
	if value == "" || strings.Contains(value, "=") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return nil, errors.New("invalid raw URL Base64")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, err
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("non-canonical raw URL Base64")
	}
	return decoded, nil
}

func secretAAD(purpose, recordID string) ([]byte, error) {
	if purpose == "" || recordID == "" {
		return nil, fmt.Errorf("%w: purpose and record id are required", errInvalidSecretContext)
	}
	if uint64(len(purpose)) > math.MaxUint32 || uint64(len(recordID)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: purpose or record id is too long", errInvalidSecretContext)
	}

	aad := make([]byte, 0, len(secretAADPrefix)+8+len(purpose)+len(recordID))
	aad = append(aad, secretAADPrefix...)
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(purpose)))
	aad = append(aad, purpose...)
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(recordID)))
	aad = append(aad, recordID...)
	return aad, nil
}
