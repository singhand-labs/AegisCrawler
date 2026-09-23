package recording

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxSnapshotPatchOps bounds the number of operations in one snapshot delta.
// The client caps emission at a lower bound; this is the server-side ceiling
// that fails closed before any patch work happens.
const maxSnapshotPatchOps = 4096

var errInvalidPatch = errors.New("invalid snapshot patch")

// applySnapshotPatch applies an RFC 6902 subset (add / replace / remove with
// rooted JSON pointers) to the content map in place. Anything the client
// emitter never produces — unknown ops, unrooted or missing paths, root-level
// operations, out-of-bounds array indices, the RFC "-" append token — is an
// error, so malformed patches fail closed instead of half-applying.
func applySnapshotPatch(content map[string]any, rawOps []any) error {
	if len(rawOps) == 0 {
		return fmt.Errorf("%w: empty patch", errInvalidPatch)
	}
	if len(rawOps) > maxSnapshotPatchOps {
		return fmt.Errorf("%w: %d ops exceed the ceiling of %d", errInvalidPatch, len(rawOps), maxSnapshotPatchOps)
	}
	var container any = content
	for index, rawOp := range rawOps {
		op, ok := rawOp.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: op %d is not an object", errInvalidPatch, index)
		}
		kind, _ := op["op"].(string)
		path, ok := op["path"].(string)
		if !ok || !strings.HasPrefix(path, "/") || len(path) < 1 {
			return fmt.Errorf("%w: op %d path must be a rooted JSON pointer", errInvalidPatch, index)
		}
		tokens := splitPointer(path)
		if len(tokens) == 0 || tokens[0] == "" {
			return fmt.Errorf("%w: op %d must not address the document root", errInvalidPatch, index)
		}
		updated, err := applyOp(container, tokens, kind, op)
		if err != nil {
			return fmt.Errorf("%w: op %d (%s %s): %v", errInvalidPatch, index, kind, path, err)
		}
		container = updated
	}
	// The root container is the content map itself; a well-formed patch never
	// replaces it, but keep the invariant explicit for callers.
	if _, ok := container.(map[string]any); !ok {
		return fmt.Errorf("%w: patch replaced the content root", errInvalidPatch)
	}
	return nil
}

// applyOp resolves the pointer's parent inside container, applies the
// operation, and returns the (possibly rebuilt) container: slice inserts and
// removals change length, which must propagate back up.
func applyOp(container any, tokens []string, kind string, op map[string]any) (any, error) {
	last := tokens[len(tokens)-1]
	parent := tokens[:len(tokens)-1]
	holder, err := descend(container, parent)
	if err != nil {
		return nil, err
	}
	switch typed := holder.(type) {
	case map[string]any:
		switch kind {
		case "add":
			value, present := op["value"]
			if !present {
				return nil, fmt.Errorf("op requires a value")
			}
			typed[last] = value
			return container, nil
		case "replace":
			value, present := op["value"]
			if !present {
				return nil, fmt.Errorf("op requires a value")
			}
			if _, exists := typed[last]; !exists {
				return nil, fmt.Errorf("path does not exist")
			}
			typed[last] = value
			return container, nil
		case "remove":
			if _, exists := typed[last]; !exists {
				return nil, fmt.Errorf("path does not exist")
			}
			delete(typed, last)
			return container, nil
		default:
			return nil, fmt.Errorf("unsupported op %q", kind)
		}
	case []any:
		index, err := arrayIndex(last, len(typed), kind == "add")
		if err != nil {
			return nil, err
		}
		switch kind {
		case "add":
			value, present := op["value"]
			if !present {
				return nil, fmt.Errorf("op requires a value")
			}
			typed = append(typed, nil)
			copy(typed[index+1:], typed[index:])
			typed[index] = value
		case "replace":
			value, present := op["value"]
			if !present {
				return nil, fmt.Errorf("op requires a value")
			}
			typed[index] = value
		case "remove":
			typed = append(typed[:index], typed[index+1:]...)
		default:
			return nil, fmt.Errorf("unsupported op %q", kind)
		}
		return rebuild(container, parent, typed), nil
	default:
		return nil, fmt.Errorf("path addresses a non-container")
	}
}

// descend walks the pointer's parent tokens down from container.
func descend(container any, tokens []string) (any, error) {
	current := container
	for _, token := range tokens {
		switch typed := current.(type) {
		case map[string]any:
			child, exists := typed[token]
			if !exists {
				return nil, fmt.Errorf("path does not exist")
			}
			current = child
		case []any:
			index, err := arrayIndex(token, len(typed), false)
			if err != nil {
				return nil, err
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("path traverses a non-container")
		}
	}
	return current, nil
}

// rebuild writes a modified slice back into its parent after descent. The
// client emitter only patches arrays nested inside the content object, never
// the content root, so the rebuild chain terminates at a map or slice field.
func rebuild(container any, parent []string, updated []any) any {
	if len(parent) == 0 {
		return updated
	}
	holder, err := descend(container, parent[:len(parent)-1])
	if err != nil {
		return container
	}
	last := parent[len(parent)-1]
	switch typed := holder.(type) {
	case map[string]any:
		typed[last] = updated
	case []any:
		if index, err := arrayIndex(last, len(typed), false); err == nil {
			typed[index] = updated
		}
	}
	return container
}

func arrayIndex(token string, length int, allowAppend bool) (int, error) {
	if token == "-" {
		return 0, fmt.Errorf("the '-' append token is not accepted")
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 || strconv.Itoa(index) != token {
		return 0, fmt.Errorf("array index must be a non-negative integer, got %q", token)
	}
	max := length - 1
	if allowAppend {
		max = length
	}
	if index > max {
		return 0, fmt.Errorf("array index %d out of bounds (length %d)", index, length)
	}
	return index, nil
}

func splitPointer(path string) []string {
	rest := strings.TrimPrefix(path, "/")
	if rest == "" {
		return []string{""}
	}
	tokens := strings.Split(rest, "/")
	for i, token := range tokens {
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")
		tokens[i] = token
	}
	return tokens
}
