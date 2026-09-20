package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := "a-test-key-that-is-long-enough-for-aes-256"
	vars := map[string]any{
		"url":     "https://example.com/",
		"timeout": 30,
		"nested": map[string]any{
			"key": "value",
		},
	}

	encrypted, err := EncryptVariables(key, vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if encrypted == "" {
		t.Fatal("expected non-empty ciphertext")
	}
	if !strings.HasPrefix(encrypted, "$pbkdf2$") {
		t.Fatalf("expected PBKDF2 ciphertext prefix, got %q", encrypted)
	}

	decrypted, err := DecryptVariables(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if decrypted["url"] != vars["url"] {
		t.Fatalf("expected url %q, got %q", vars["url"], decrypted["url"])
	}
	if int(decrypted["timeout"].(float64)) != vars["timeout"] {
		t.Fatalf("expected timeout %v, got %v", vars["timeout"], decrypted["timeout"])
	}
}

func TestEncryptWithEmptyKeyStoresPlainJSON(t *testing.T) {
	vars := map[string]any{"foo": "bar"}
	plain, err := EncryptVariables("", vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if plain != `{"foo":"bar"}` {
		t.Fatalf("expected plain JSON, got %q", plain)
	}

	decrypted, err := DecryptVariables("", plain)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if decrypted["foo"] != "bar" {
		t.Fatalf("expected foo bar, got %v", decrypted["foo"])
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	vars := map[string]any{"secret": "value"}
	encrypted, err := EncryptVariables("correct-key", vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = DecryptVariables("wrong-key", encrypted)
	if err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	key := "another-test-key"
	vars := map[string]any{"x": 1}
	enc1, err := EncryptVariables(key, vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	enc2, err := EncryptVariables(key, vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if enc1 == enc2 {
		t.Fatal("expected different ciphertexts due to random nonce")
	}
}

func TestEncryptDecryptRoundTripPBKDF2(t *testing.T) {
	key := "pbkdf2-test-key"
	vars := map[string]any{"token": "abc123", "count": 42}

	encrypted, err := EncryptVariables(key, vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	// M-13: format is now $pbkdf2$<iter>$<salt>$<ct> (5 parts).
	parts := strings.SplitN(encrypted, "$", 5)
	if len(parts) != 5 || parts[1] != "pbkdf2" {
		t.Fatalf("invalid PBKDF2 ciphertext format: %q", encrypted)
	}
	if iter, err := strconv.Atoi(parts[2]); err != nil || iter <= 0 {
		t.Fatalf("invalid iteration count %q: %v", parts[2], err)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[3]); err != nil {
		t.Fatalf("salt is not valid base64: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[4]); err != nil {
		t.Fatalf("payload is not valid base64: %v", err)
	}

	decrypted, err := DecryptVariables(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if decrypted["token"] != vars["token"] {
		t.Fatalf("expected token %q, got %q", vars["token"], decrypted["token"])
	}
	if int(decrypted["count"].(float64)) != vars["count"] {
		t.Fatalf("expected count %v, got %v", vars["count"], decrypted["count"])
	}
}

// TestDecryptOldSha256Ciphertext verifies that variables encrypted with the
// previous sha256-based key derivation can still be decrypted.
func TestDecryptOldSha256Ciphertext(t *testing.T) {
	key := "legacy-key"
	vars := map[string]any{"legacy": "value", "num": 7}

	legacyCiphertext, err := encryptWithOldSha256(key, vars)
	if err != nil {
		t.Fatalf("legacy encrypt failed: %v", err)
	}

	decrypted, err := DecryptVariables(key, legacyCiphertext)
	if err != nil {
		t.Fatalf("decrypt legacy ciphertext failed: %v", err)
	}
	if decrypted["legacy"] != vars["legacy"] {
		t.Fatalf("expected legacy %q, got %q", vars["legacy"], decrypted["legacy"])
	}
	if int(decrypted["num"].(float64)) != vars["num"] {
		t.Fatalf("expected num %v, got %v", vars["num"], decrypted["num"])
	}
}

func TestDecryptTamperedPbkdf2CiphertextFails(t *testing.T) {
	key := "tamper-key"
	vars := map[string]any{"secret": "value"}
	encrypted, err := EncryptVariables(key, vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Tamper with the payload portion. M-13: format is now 5 parts.
	parts := strings.SplitN(encrypted, "$", 5)
	if len(parts) != 5 {
		t.Fatal("invalid ciphertext format")
	}
	payload, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	if len(payload) > 0 {
		payload[len(payload)-1] ^= 0xFF
	}
	tampered := "$pbkdf2$" + parts[2] + "$" + parts[3] + "$" + base64.StdEncoding.EncodeToString(payload)

	_, err = DecryptVariables(key, tampered)
	if err == nil {
		t.Fatal("expected decryption of tampered ciphertext to fail")
	}
}

func TestDecryptWithWrongKeyFailsPBKDF2(t *testing.T) {
	vars := map[string]any{"secret": "value"}
	encrypted, err := EncryptVariables("correct-key", vars)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = DecryptVariables("wrong-key", encrypted)
	if err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestEncryptVariables_MarshalError(t *testing.T) {
	vars := map[string]any{"ch": make(chan int)}
	_, err := EncryptVariables("key", vars)
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestDecryptVariables_InvalidPbkdf2Format(t *testing.T) {
	cases := []string{
		"$pbkdf2$",
		"$pbkdf2$salt",
		"$pbkdf2$salt$payload$extra",
		"$md5$salt$payload",
	}
	for _, c := range cases {
		_, err := DecryptVariables("key", c)
		if err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestDecryptVariables_InvalidBase64(t *testing.T) {
	_, err := DecryptVariables("key", "$pbkdf2$!!!$payload")
	if err == nil {
		t.Fatal("expected decode salt error")
	}
	_, err = DecryptVariables("key", "$pbkdf2$c2FsdA==$!!!")
	if err == nil {
		t.Fatal("expected decode payload error")
	}
}

func TestDecryptVariables_CiphertextTooShort(t *testing.T) {
	// Valid salt but payload smaller than GCM nonce size.
	_, err := DecryptVariables("key", "$pbkdf2$c2FsdA==$c2hvcnQ=")
	if err == nil {
		t.Fatal("expected ciphertext too short error")
	}
}

func TestDecryptVariables_PlainJSONError(t *testing.T) {
	_, err := DecryptVariables("", "not-json")
	if err == nil {
		t.Fatal("expected plain JSON unmarshal error")
	}
}

func TestDecryptVariables_LegacyBase64DecodeError(t *testing.T) {
	_, err := DecryptVariables("key", "not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected legacy base64 decode error")
	}
}

func TestDecryptVariables_PlainJSONUnmarshalError(t *testing.T) {
	// Encrypt non-JSON bytes with a valid tag using the legacy SHA-256 path.
	key := "json-err-key"
	plain := []byte("not-json")
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	legacy := base64.StdEncoding.EncodeToString(ct)

	_, err = DecryptVariables(key, legacy)
	if err == nil {
		t.Fatal("expected unmarshal error for non-JSON plaintext")
	}
}

// encryptWithOldSha256 reproduces the pre-PBKDF2 encryption format.
func encryptWithOldSha256(key string, vars map[string]any) (string, error) {
	plain, err := json.Marshal(vars)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
