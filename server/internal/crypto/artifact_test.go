package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestArtifactEncryptionRoundTripAndBinding(t *testing.T) {
	key := "artifact-key-long-enough-for-tests"
	plain := []byte(`{"events":[{"type":"click"}]}`)
	aad := []byte("default\x00recording-1")

	encrypted, err := EncryptArtifact(key, plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("click")) {
		t.Fatal("ciphertext exposed plaintext")
	}
	decrypted, err := DecryptArtifact(key, encrypted, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("round trip mismatch: %q", decrypted)
	}
	if _, err := DecryptArtifact(key, encrypted, []byte("other\x00recording-1")); err == nil {
		t.Fatal("expected AAD mismatch to fail")
	}
}

func TestArtifactEncryptionRequiresKeyAndRejectsDamage(t *testing.T) {
	if _, err := EncryptArtifact("", []byte("x"), nil); !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Fatalf("expected key-required error, got %v", err)
	}
	if _, err := DecryptArtifact("", []byte("x"), nil); !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Fatalf("expected key-required error, got %v", err)
	}
	if _, err := DecryptArtifact("key", []byte("not-an-envelope"), nil); err == nil {
		t.Fatal("expected invalid envelope error")
	}

	encrypted, err := EncryptArtifact("key", []byte("payload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	encrypted[len(encrypted)-1] ^= 0xff
	if _, err := DecryptArtifact("key", encrypted, nil); err == nil {
		t.Fatal("expected tampered artifact to fail")
	}
}
