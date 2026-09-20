package models

// blockingSealingFlags is the closed set of safety-flag keys that block
// ApproveDSLWorkflow until an admin explicitly overrides them.
//
// Generation-time ScanSafety hits (risky submit actions on financial/payment
// keywords) continue to fail hard via ErrUnsafeGeneratedRule and never reach
// sealing. The keys here are produced by ScanSealingFlags for concerns that
// are NOT generation-fatal but should require human review before the rule
// becomes an immutable version.
//
// Add new blocking keys deliberately: every entry here is a human-review
// requirement for operators.
var blockingSealingFlags = map[string]struct{}{
	"external-resource-load":  {},
	"script-tag-in-selector":  {},
	"iframe-embed":            {},
	"event-handler-attribute": {},
}

// BlockingFlags returns the subset of `flags` that are in the blocking
// taxonomy. Input order is preserved. Unknown flags are treated as advisory.
func BlockingFlags(flags []string) []string {
	var out []string
	for _, f := range flags {
		if _, ok := blockingSealingFlags[f]; ok {
			out = append(out, f)
		}
	}
	return out
}
