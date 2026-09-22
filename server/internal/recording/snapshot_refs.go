package recording

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidSnapshotReference marks a recording whose reference snapshots
// cannot be resolved. References are a size optimization emitted by the
// extension: a snapshot whose content (url, selectorMap, domTree, capture)
// is exactly identical to an earlier snapshot is uploaded as a pointer to
// that snapshot's sequence instead of a duplicate payload. Identical content
// is equivalent per-action evidence, so ingest expands references back into
// full snapshots and every downstream consumer (sanitize, delta archive,
// selector evidence, LLM workflows) keeps seeing today's shape.
var ErrInvalidSnapshotReference = errors.New("invalid snapshot reference")

// ExpandSnapshotReferences resolves reference snapshots in place. A valid
// reference names the sequence of an EARLIER snapshot that carries a domTree
// in the same recording; its content fields (domTree, selectorMap, capture)
// are deep-copied into the referencing snapshot and the pointer is removed.
// Anything else — unknown target, forward target, target without content,
// malformed pointer — fails closed with ErrInvalidSnapshotReference.
func ExpandSnapshotReferences(recording map[string]any) error {
	if recording == nil {
		return nil
	}
	rawSnapshots, ok := recording["snapshots"].([]any)
	if !ok || len(rawSnapshots) == 0 {
		return nil
	}
	content := map[float64]map[string]any{}
	for index, raw := range rawSnapshots {
		snapshot, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawRef, referenced := snapshot["ref"]
		if !referenced {
			if _, hasTree := snapshot["domTree"]; hasTree {
				if sequence, ok := numericSequence(snapshot["sequence"]); ok {
					content[sequence] = snapshot
				}
			}
			continue
		}
		ref, err := referenceSequence(rawRef)
		if err != nil {
			return fmt.Errorf("%w: snapshots[%d].ref: %v", ErrInvalidSnapshotReference, index, err)
		}
		target, found := content[ref]
		if !found {
			return fmt.Errorf(
				"%w: snapshots[%d].ref %d does not name an earlier content snapshot",
				ErrInvalidSnapshotReference, index, int64(ref))
		}
		for _, field := range []string{"domTree", "selectorMap", "capture"} {
			value, present := target[field]
			if !present {
				continue
			}
			copied, err := deepCopyJSON(value)
			if err != nil {
				return fmt.Errorf("%w: snapshots[%d].ref %d: copy %s: %v",
					ErrInvalidSnapshotReference, index, int64(ref), field, err)
			}
			snapshot[field] = copied
		}
		delete(snapshot, "ref")
	}
	return nil
}

func numericSequence(raw any) (float64, bool) {
	sequence, ok := raw.(float64)
	return sequence, ok
}

func referenceSequence(raw any) (float64, error) {
	ref, ok := raw.(float64)
	if !ok {
		return 0, errors.New("must be an integer sequence number")
	}
	if ref < 0 || ref != float64(int64(ref)) {
		return 0, fmt.Errorf("must be a non-negative integer, got %v", raw)
	}
	return ref, nil
}

func deepCopyJSON(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
