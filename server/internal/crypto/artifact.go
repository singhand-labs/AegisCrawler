package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

var ErrEncryptionKeyRequired = errors.New("encryption key is required")

var artifactMagic = []byte("AEGIS1")

// EncryptArtifact encrypts arbitrary bytes with a fresh PBKDF2 salt and
// AES-256-GCM nonce. aad binds the ciphertext to its workspace and resource ID
// so copying a blob to another row does not make it decryptable there.
func EncryptArtifact(key string, plaintext, aad []byte) ([]byte, error) {
	if key == "" {
		return nil, ErrEncryptionKeyRequired
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate artifact salt: %w", err)
	}
	block, err := aes.NewCipher(deriveKeyPBKDF2(key, salt, pbkdf2IterCurrent))
	if err != nil {
		return nil, fmt.Errorf("create artifact cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create artifact gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate artifact nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	out := make([]byte, 0, len(artifactMagic)+len(salt)+len(nonce)+len(ciphertext))
	out = append(out, artifactMagic...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptArtifact authenticates and decrypts data produced by EncryptArtifact.
func DecryptArtifact(key string, envelope, aad []byte) ([]byte, error) {
	if key == "" {
		return nil, ErrEncryptionKeyRequired
	}
	minimum := len(artifactMagic) + saltLen
	if len(envelope) < minimum || string(envelope[:len(artifactMagic)]) != string(artifactMagic) {
		return nil, errors.New("invalid artifact envelope")
	}
	salt := envelope[len(artifactMagic):minimum]
	block, err := aes.NewCipher(deriveKeyPBKDF2(key, salt, pbkdf2IterCurrent))
	if err != nil {
		return nil, fmt.Errorf("create artifact cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create artifact gcm: %w", err)
	}
	if len(envelope) < minimum+gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("artifact envelope is too short")
	}
	nonce := envelope[minimum : minimum+gcm.NonceSize()]
	ciphertext := envelope[minimum+gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt artifact: %w", err)
	}
	return plain, nil
}
