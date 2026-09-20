package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	pbkdf2Prefix = "$pbkdf2$"
	// M-13: PBKDF2 iteration counts. New encryptions use pbkdf2IterCurrent
	// (600k per OWASP 2023 guidance for PBKDF2-SHA256). The previous default
	// (100k) is retained as pbkdf2IterLegacy for backward-compatible
	// decryption of blobs that predate the iteration-count-in-format change.
	pbkdf2IterCurrent = 600_000
	pbkdf2IterLegacy  = 100_000
	keyLen            = 32
	saltLen           = 16
)

// EncryptVariables serializes vars to JSON and encrypts the result with AES-256-GCM.
// If key is empty, variables are returned as plain JSON (backward-compatible mode).
func EncryptVariables(key string, vars map[string]any) (string, error) {
	plain, err := json.Marshal(vars)
	if err != nil {
		return "", fmt.Errorf("marshal variables: %w", err)
	}
	if key == "" {
		return string(plain), nil
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	block, err := aes.NewCipher(deriveKeyPBKDF2(key, salt, pbkdf2IterCurrent))
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	// M-13: include iteration count in the format so future bumps don't
	// break decryption of existing blobs. Format: $pbkdf2$<iter>$<salt>$<ct>
	return fmt.Sprintf(
		"$pbkdf2$%d$%s$%s",
		pbkdf2IterCurrent,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(ciphertext),
	), nil
}

// DecryptVariables decrypts a ciphertext produced by EncryptVariables and returns the variables map.
// If key is empty, the ciphertext is treated as plain JSON.
func DecryptVariables(key string, ciphertext string) (map[string]any, error) {
	if key == "" {
		var vars map[string]any
		if err := json.Unmarshal([]byte(ciphertext), &vars); err != nil {
			return nil, fmt.Errorf("unmarshal plain variables: %w", err)
		}
		return vars, nil
	}

	var (
		data []byte
		dErr error
	)

	var keyBytes []byte
	if strings.HasPrefix(ciphertext, pbkdf2Prefix) {
		// M-13: support both old format ($pbkdf2$<salt>$<ct>) and new
		// format ($pbkdf2$<iter>$<salt>$<ct>). Old format defaults to
		// pbkdf2IterLegacy (100k) for backward compatibility.
		parts := strings.SplitN(ciphertext, "$", 5)
		var iter int
		var saltB64, ctB64 string
		if len(parts) == 5 && parts[1] == "pbkdf2" {
			// New format: $pbkdf2$<iter>$<salt>$<ct>
			var err error
			iter, err = strconv.Atoi(parts[2])
			if err != nil || iter <= 0 {
				return nil, fmt.Errorf("invalid pbkdf2 iteration count: %q", parts[2])
			}
			saltB64, ctB64 = parts[3], parts[4]
		} else if len(parts) == 4 && parts[1] == "pbkdf2" {
			// Old format: $pbkdf2$<salt>$<ct> — default to legacy iterations.
			iter = pbkdf2IterLegacy
			saltB64, ctB64 = parts[2], parts[3]
		} else {
			return nil, fmt.Errorf("invalid pbkdf2 ciphertext format")
		}
		salt, err := base64.StdEncoding.DecodeString(saltB64)
		if err != nil {
			return nil, fmt.Errorf("decode salt: %w", err)
		}
		data, dErr = base64.StdEncoding.DecodeString(ctB64)
		if dErr != nil {
			return nil, fmt.Errorf("decode ciphertext: %w", dErr)
		}
		keyBytes = deriveKeyPBKDF2(key, salt, iter)
	} else {
		// M-10: legacy single-SHA256 derivation — no salt, no iterations.
		// Acceptable for backward-compatible decryption but log a warning
		// so operators know to re-encrypt these variables.
		log.Printf("[WARN] crypto: decrypting variable with legacy single-SHA256 key derivation (no PBKDF2); consider re-encrypting")
		data, dErr = base64.StdEncoding.DecodeString(ciphertext)
		if dErr != nil {
			return nil, fmt.Errorf("decode ciphertext: %w", dErr)
		}
		keyBytes = deriveKey(key)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, enc := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, enc, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt variables: %w", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(plain, &vars); err != nil {
		return nil, fmt.Errorf("unmarshal variables: %w", err)
	}
	return vars, nil
}

func deriveKeyPBKDF2(password string, salt []byte, iter int) []byte {
	return pbkdf2.Key([]byte(password), salt, iter, keyLen, sha256.New)
}

func deriveKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}
