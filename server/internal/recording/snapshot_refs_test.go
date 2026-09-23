package recording

import (
	"encoding/json"
	"errors"
	"testing"
)

func refRecordingPayload() map[string]any {
	// Mirrors the wire shape the extension emits: a full initial snapshot
	// followed by two reference snapshots and a changed final snapshot.
	raw := `{
		"version": "2.0.0",
		"snapshots": [
			{
				"timestamp": 1, "url": "https://example.com/", "selectorMap": {"1": {"index": 1, "tagName": "a", "selector": "a.next"}},
				"phase": "initial", "sequence": 0, "actionIndex": 0,
				"capture": {"status": "complete", "nodeCount": 10, "frames": []},
				"domTree": {"type": "element", "tagName": "main", "children": [{"type": "text", "text": "Example"}]}
			},
			{
				"timestamp": 2, "url": "https://example.com/", "selectorMap": {},
				"phase": "before-action", "sequence": 1, "actionIndex": 0, "ref": 0
			},
			{
				"timestamp": 3, "url": "https://example.com/", "selectorMap": {},
				"phase": "before-action", "sequence": 2, "actionIndex": 1, "ref": 0
			},
			{
				"timestamp": 4, "url": "https://example.com/changed", "selectorMap": {"2": {"index": 2, "tagName": "b", "selector": "b.done"}},
				"phase": "final", "sequence": 3, "actionIndex": 2,
				"capture": {"status": "complete", "nodeCount": 12, "frames": []},
				"domTree": {"type": "element", "tagName": "main", "children": [{"type": "text", "text": "Changed"}]}
			}
		]
	}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		panic(err)
	}
	return payload
}

func snapshotsOf(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	rawList := payload["snapshots"].([]any)
	out := make([]map[string]any, 0, len(rawList))
	for _, raw := range rawList {
		out = append(out, raw.(map[string]any))
	}
	return out
}

func TestExpandSnapshotReferencesResolvesIdenticalAdjacentSnapshots(t *testing.T) {
	payload := refRecordingPayload()
	if err := ExpandSnapshotReferences(payload); err != nil {
		t.Fatalf("expand: %v", err)
	}
	snaps := snapshotsOf(t, payload)
	if len(snaps) != 4 {
		t.Fatalf("snapshot count changed: %d", len(snaps))
	}
	initial, refA, refB, final := snaps[0], snaps[1], snaps[2], snaps[3]
	for name, snap := range map[string]map[string]any{"refA": refA, "refB": refB} {
		if _, still := snap["ref"]; still {
			t.Fatalf("%s kept its ref pointer", name)
		}
		if snap["domTree"] == nil || snap["selectorMap"] == nil || snap["capture"] == nil {
			t.Fatalf("%s missing expanded content fields", name)
		}
		if got, want := marshal(t, snap["domTree"]), marshal(t, initial["domTree"]); got != want {
			t.Fatalf("%s domTree does not equal referenced content:\n got %s\nwant %s", name, got, want)
		}
		if got, want := marshal(t, snap["selectorMap"]), marshal(t, initial["selectorMap"]); got != want {
			t.Fatalf("%s selectorMap does not equal referenced content", name)
		}
		if got, want := marshal(t, snap["capture"]), marshal(t, initial["capture"]); got != want {
			t.Fatalf("%s capture does not equal referenced content", name)
		}
		// Positional identity stays per-snapshot.
		if snap["phase"] != "before-action" || snap["timestamp"] == initial["timestamp"] {
			t.Fatalf("%s lost its positional identity: %v", name, snap)
		}
	}
	if final["domTree"] == nil || final["ref"] != nil {
		t.Fatalf("changed final snapshot must remain a full snapshot: %v", final)
	}
	if marshal(t, final["domTree"]) == marshal(t, initial["domTree"]) {
		t.Fatalf("final snapshot content must differ from the referenced content")
	}
}

func TestExpandSnapshotReferencesExpandsBeforeSanitizeContract(t *testing.T) {
	payload := refRecordingPayload()
	expanded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	// The expanded recording must be shape-identical to a recording that
	// never used references: dropping refs keeps selector evidence and the
	// delta archive working without any downstream awareness.
	if err := ExpandSnapshotReferences(payload); err != nil {
		t.Fatalf("expand: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(expanded, &round); err != nil {
		t.Fatal(err)
	}
	_ = round
	sanitized, _ := Sanitize(payload)
	snaps, ok := sanitized["snapshots"].([]any)
	if !ok || len(snaps) != 4 {
		t.Fatalf("sanitized snapshots missing after expansion: %v", sanitized["snapshots"])
	}
	for index, raw := range snaps {
		snap := raw.(map[string]any)
		if snap["domTree"] == nil {
			t.Fatalf("sanitized snapshot %d lost domTree", index)
		}
	}
}

func TestExpandSnapshotReferencesFailsClosed(t *testing.T) {
	cases := map[string]func(payload map[string]any){
		"unknown target": func(p map[string]any) {
			p["snapshots"].([]any)[1].(map[string]any)["ref"] = float64(99)
		},
		"forward target": func(p map[string]any) {
			// Point the first ref at a LATER sequence (the final snapshot).
			p["snapshots"].([]any)[1].(map[string]any)["ref"] = float64(3)
		},
		"target without content": func(p map[string]any) {
			list := p["snapshots"].([]any)
			list[1].(map[string]any)["ref"] = float64(1) // names itself (a ref)
		},
		"malformed pointer": func(p map[string]any) {
			p["snapshots"].([]any)[1].(map[string]any)["ref"] = "0"
		},
		"negative pointer": func(p map[string]any) {
			p["snapshots"].([]any)[1].(map[string]any)["ref"] = float64(-1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			payload := refRecordingPayload()
			mutate(payload)
			err := ExpandSnapshotReferences(payload)
			if !errors.Is(err, ErrInvalidSnapshotReference) {
				t.Fatalf("expected ErrInvalidSnapshotReference, got %v", err)
			}
		})
	}
}

func TestExpandSnapshotReferencesNoopForLegacyPayloads(t *testing.T) {
	payload := map[string]any{"version": "1.0.0", "snapshots": []any{
		map[string]any{"timestamp": 1.0, "url": "https://example.com/", "selectorMap": map[string]any{}},
	}}
	if err := ExpandSnapshotReferences(payload); err != nil {
		t.Fatalf("legacy payload rejected: %v", err)
	}
	if err := ExpandSnapshotReferences(nil); err != nil {
		t.Fatalf("nil payload rejected: %v", err)
	}
	if err := ExpandSnapshotReferences(map[string]any{"version": "2.0.0"}); err != nil {
		t.Fatalf("snapshot-less payload rejected: %v", err)
	}
}

// Mirrors the wire shape the extension emits for near-identical snapshots: a
// full initial snapshot, a delta whose patch flips one rendered flag and
// replaces a text node, and a reference pointing at the delta's sequence
// (the client's exact-match baseline advances past a stored delta).
func deltaRecordingPayload() map[string]any {
	raw := `{
		"version": "2.0.0",
		"snapshots": [
			{
				"timestamp": 1, "url": "https://example.com/", "selectorMap": {"1": {"index": 1, "tagName": "a", "selector": "a.next", "boundingRect": {"x": 0, "y": 0}}},
				"phase": "initial", "sequence": 0, "actionIndex": 0,
				"capture": {"status": "complete", "nodeCount": 10, "frames": []},
				"domTree": {"type": "element", "tagName": "main", "children": [{"type": "text", "text": "Example"}, {"type": "element", "tagName": "section", "rendered": false}]}
			},
			{
				"timestamp": 2, "url": "https://example.com/", "selectorMap": {},
				"phase": "before-action", "sequence": 1, "actionIndex": 0,
				"base": 0,
				"patch": [
					{"op": "replace", "path": "/domTree/children/0/text", "value": "Mutated"},
					{"op": "replace", "path": "/domTree/children/1/rendered", "value": true},
					{"op": "replace", "path": "/selectorMap/1/boundingRect", "value": {"x": 5, "y": 700}}
				]
			},
			{
				"timestamp": 3, "url": "https://example.com/", "selectorMap": {},
				"phase": "before-action", "sequence": 2, "actionIndex": 1, "ref": 1
			}
		]
	}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		panic(err)
	}
	return payload
}

func TestExpandSnapshotReferencesAppliesDeltas(t *testing.T) {
	payload := deltaRecordingPayload()
	if err := ExpandSnapshotReferences(payload); err != nil {
		t.Fatalf("expand: %v", err)
	}
	snaps := snapshotsOf(t, payload)
	initial, delta, ref := snaps[0], snaps[1], snaps[2]

	if _, still := delta["patch"]; still {
		t.Fatal("delta kept its patch")
	}
	if _, still := delta["base"]; still {
		t.Fatal("delta kept its base pointer")
	}
	if delta["domTree"] == nil {
		t.Fatal("delta missing expanded domTree")
	}
	tree := delta["domTree"].(map[string]any)
	children := tree["children"].([]any)
	if children[0].(map[string]any)["text"] != "Mutated" {
		t.Fatalf("patched text not applied: %v", children[0])
	}
	if children[1].(map[string]any)["rendered"] != true {
		t.Fatalf("patched rendered flag not applied: %v", children[1])
	}
	rect := delta["selectorMap"].(map[string]any)["1"].(map[string]any)["boundingRect"].(map[string]any)
	if rect["x"] != float64(5) || rect["y"] != float64(700) {
		t.Fatalf("patched boundingRect not applied: %v", rect)
	}
	// The base snapshot itself stays untouched.
	if initial["domTree"].(map[string]any)["children"].([]any)[0].(map[string]any)["text"] != "Example" {
		t.Fatal("base snapshot content was mutated by a delta expansion")
	}
	// Positional identity stays per-snapshot.
	if delta["phase"] != "before-action" || delta["timestamp"] == initial["timestamp"] {
		t.Fatalf("delta lost positional identity: %v", delta)
	}

	// A later reference naming the expanded delta's sequence resolves to it.
	if _, still := ref["ref"]; still {
		t.Fatal("ref kept its pointer")
	}
	if marshal(t, ref["domTree"]) != marshal(t, delta["domTree"]) {
		t.Fatal("ref to expanded delta does not resolve to the patched content")
	}

	// The expanded form satisfies sanitize like any full-snapshot recording.
	sanitized, _ := Sanitize(payload)
	if list, ok := sanitized["snapshots"].([]any); !ok || len(list) != 3 {
		t.Fatalf("sanitized snapshots missing after delta expansion: %v", sanitized["snapshots"])
	}
}

func TestExpandSnapshotDeltaFailsClosed(t *testing.T) {
	mutateDelta := func(edit func(delta map[string]any)) func(map[string]any) {
		return func(p map[string]any) {
			edit(p["snapshots"].([]any)[1].(map[string]any))
		}
	}
	cases := map[string]func(payload map[string]any){
		"unknown base": mutateDelta(func(d map[string]any) { d["base"] = float64(99) }),
		"forward base": mutateDelta(func(d map[string]any) { d["base"] = float64(2) }),
		"negative base": mutateDelta(func(d map[string]any) { d["base"] = float64(-1) }),
		"base without content": mutateDelta(func(d map[string]any) { d["base"] = float64(1) }),
		"patch without base": mutateDelta(func(d map[string]any) { delete(d, "base") }),
		"empty patch": mutateDelta(func(d map[string]any) { d["patch"] = []any{} }),
		"patch not array": mutateDelta(func(d map[string]any) { d["patch"] = "nope" }),
		"unknown op": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "copy", "path": "/domTree", "value": 1}}
		}),
		"unrooted path": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "replace", "path": "domTree", "value": 1}}
		}),
		"root path": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "replace", "path": "/", "value": 1}}
		}),
		"missing path": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "replace", "path": "/domTree/missing", "value": 1}}
		}),
		"missing value": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "replace", "path": "/domTree/children/0/text"}}
		}),
		"array index out of bounds": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "remove", "path": "/domTree/children/9"}}
		}),
		"append token": mutateDelta(func(d map[string]any) {
			d["patch"] = []any{map[string]any{"op": "add", "path": "/domTree/children/-", "value": 1}}
		}),
		"op ceiling": mutateDelta(func(d map[string]any) {
			ops := make([]any, maxSnapshotPatchOps+1)
			for i := range ops {
				ops[i] = map[string]any{"op": "replace", "path": "/domTree/children/0/text", "value": "x"}
			}
			d["patch"] = ops
		}),
		"both ref and patch": mutateDelta(func(d map[string]any) { d["ref"] = float64(0) }),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			payload := deltaRecordingPayload()
			mutate(payload)
			err := ExpandSnapshotReferences(payload)
			if !errors.Is(err, ErrInvalidSnapshotDelta) && !errors.Is(err, ErrInvalidSnapshotReference) {
				t.Fatalf("expected a snapshot delta/reference error, got %v", err)
			}
		})
	}
}

func marshal(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
