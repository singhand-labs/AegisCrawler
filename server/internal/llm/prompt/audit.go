package prompt

import (
	"crypto/sha256"
	"encoding/hex"
)

// AuditPreview returns a SHA-256 hash and a prefix of the full prompt built
// from the system and user messages plus the model identifier. The prefix is
// truncated to at most maxPrefixBytes (200) while keeping valid UTF-8. The
// full prompt is never returned, so audit logs can correlate prompts without
// leaking sensitive DOM content. The model identifier is folded into the hash
// so that the same prompt sent to two different models produces distinct hashes
// (closes SC-17): prompt reuse is ambiguous across models without it.
func AuditPreview(system, user, modelID string) (hash, prefix string) {
	full := system + user
	h := sha256.New()
	h.Write([]byte(full))
	h.Write([]byte{0})
	h.Write([]byte(modelID))
	sum := h.Sum(nil)
	hash = hex.EncodeToString(sum)
	prefix = truncateString(full, maxPrefixBytes)
	return
}

const maxPrefixBytes = 200

// truncateString returns s truncated to a valid UTF-8 boundary not exceeding n
// bytes.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s)
	// Walk backwards from the byte limit to the start of the current UTF-8
	// rune so we do not split a multi-byte character.
	for n > 0 && n < len(b) && b[n]&0xC0 == 0x80 {
		n--
	}
	return string(b[:n])
}
