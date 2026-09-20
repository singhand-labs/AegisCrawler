package llm

import (
	"crypto/sha256"
	"encoding/hex"

	"go.uber.org/zap"
)

// HashedAuditFields records correlation evidence for content that enforced
// policy forbids logging verbatim. The class must be a stable, non-content
// identifier chosen by the caller.
func HashedAuditFields(prefix, class, value string) []zap.Field {
	sum := sha256.Sum256([]byte(value))
	return []zap.Field{
		zap.String(prefix+"Class", class),
		zap.String(prefix+"Hash", hex.EncodeToString(sum[:])),
		zap.Int(prefix+"Bytes", len(value)),
	}
}
