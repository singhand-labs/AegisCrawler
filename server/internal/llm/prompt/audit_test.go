package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestAuditPreview_ReturnsStableHash(t *testing.T) {
	system := "system prompt"
	user := "user prompt content"
	modelID := "gpt-test"
	gotHash, gotPrefix := AuditPreview(system, user, modelID)

	h := sha256.New()
	h.Write([]byte(system + user))
	h.Write([]byte{0})
	h.Write([]byte(modelID))
	sum := h.Sum(nil)
	wantHash := hex.EncodeToString(sum)
	if gotHash != wantHash {
		t.Fatalf("expected hash %q, got %q", wantHash, gotHash)
	}
	if gotPrefix != system+user {
		t.Fatalf("expected full string as prefix for short input, got %q", gotPrefix)
	}
}

func TestAuditPreview_TruncatesLongInput(t *testing.T) {
	// 400-byte ASCII string should be truncated to 200 bytes.
	system := strings.Repeat("a", 200)
	user := strings.Repeat("b", 200)
	_, prefix := AuditPreview(system, user, "")
	if len(prefix) != 200 {
		t.Fatalf("expected prefix length 200, got %d", len(prefix))
	}
	if prefix != strings.Repeat("a", 200) {
		t.Fatalf("expected first 200 bytes, got %q", prefix)
	}
}

func TestAuditPreview_TruncatesAtValidRuneBoundary(t *testing.T) {
	// 101 two-byte runes = 202 bytes; truncating to 200 bytes must keep whole runes.
	s := strings.Repeat("é", 101)
	_, prefix := AuditPreview(s, "", "")
	if len(prefix) != 200 {
		t.Fatalf("expected 200 bytes (100 runes), got %d", len(prefix))
	}
	if strings.Contains(prefix, "\uFFFD") {
		t.Fatal("prefix contains replacement character; rune was split")
	}
}

func TestAuditPreview_TruncatesInsideMultiByteRune(t *testing.T) {
	// 199 ASCII bytes + a 3-byte rune places a continuation byte at index 200.
	s := strings.Repeat("a", 199) + "中"
	_, prefix := AuditPreview(s, "", "")
	if len(prefix) != 199 {
		t.Fatalf("expected 199 bytes, got %d", len(prefix))
	}
	if strings.Contains(prefix, "\uFFFD") {
		t.Fatal("prefix contains replacement character; rune was split")
	}
}

func TestAuditPreview_ModelIDDistinguishesHash(t *testing.T) {
	system, user := "sys", "user"
	h1, _ := AuditPreview(system, user, "model-a")
	h2, _ := AuditPreview(system, user, "model-b")
	if h1 == h2 {
		t.Fatal("expected distinct hashes for distinct model IDs")
	}
	// Same model produces the same hash.
	h1Again, _ := AuditPreview(system, user, "model-a")
	if h1 != h1Again {
		t.Fatal("expected deterministic hash for same model ID")
	}
}

func TestTruncateString_NoTruncationNeeded(t *testing.T) {
	if got := truncateString("short", 10); got != "short" {
		t.Fatalf("expected 'short', got %q", got)
	}
}
