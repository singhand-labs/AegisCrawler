package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestArchiveReconstructsEverySnapshotLosslessly(t *testing.T) {
	recording := map[string]any{
		"version": "2",
		"events":  []any{map[string]any{"type": "click", "index": 2}},
		"snapshots": []any{
			map[string]any{"url": "https://example.com", "tree": map[string]any{"tag": "body", "text": "one"}},
			map[string]any{"url": "https://example.com", "tree": map[string]any{"tag": "body", "text": "two", "open": true}},
			map[string]any{"url": "https://example.com/next", "tree": map[string]any{"tag": "body", "text": "three"}},
		},
	}
	archive, stats, err := BuildArchive(recording)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ActionCount != 1 || stats.SnapshotCount != 3 || len(archive.SnapshotDeltas) != 2 {
		t.Fatalf("unexpected archive stats: %+v archive=%+v", stats, archive)
	}
	reconstructed, err := archive.Reconstruct()
	if err != nil {
		t.Fatal(err)
	}
	var canonicalInput map[string]any
	data, _ := json.Marshal(recording)
	_ = json.Unmarshal(data, &canonicalInput)
	if !reflect.DeepEqual(reconstructed, canonicalInput) {
		t.Fatalf("reconstructed recording differs:\nwant=%s\ngot=%s", marshalCanonical(canonicalInput), marshalCanonical(reconstructed))
	}
}

func TestArchiveSupportsPreprocessedSnapshotFieldAndRejectsInvalidDeltas(t *testing.T) {
	archive, _, err := BuildArchive(map[string]any{
		"events":       []any{},
		"domSnapshots": []any{map[string]any{"n": 1}, map[string]any{"n": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := archive.Reconstruct()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reconstructed["domSnapshots"]; !ok {
		t.Fatal("expected domSnapshots field to be preserved")
	}
	archive.SnapshotDeltas[0].PrefixBytes = 1_000
	if _, err := archive.Reconstruct(); err == nil {
		t.Fatal("expected invalid delta to fail")
	}
	if _, _, err := BuildArchive(map[string]any{"snapshots": []any{map[string]any{"only": true}}}); !errors.Is(err, ErrSnapshotsRequired) {
		t.Fatalf("expected snapshot requirement error, got %v", err)
	}
}

func TestSanitizeRemovesSecretsAndUnsafeMarkupWithoutMutation(t *testing.T) {
	input := map[string]any{
		"events": []any{map[string]any{"type": "input", "value": "token=live-secret", "password": "hunter2"}},
		"snapshots": []any{
			map[string]any{"type": "password", "value": "plain-password", "outerHTML": "<input>", "onclick": "steal()"},
			map[string]any{"type": "text", "text": "alice@example.com", "style": "display:none"},
		},
	}
	out, report := Sanitize(input)
	serialized := string(marshalCanonical(out))
	for _, secret := range []string{"live-secret", "hunter2", "plain-password", "alice@example.com", "<input>", "steal()", "display:none"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("sanitized payload retained %q: %s", secret, serialized)
		}
	}
	if report.RedactedValues < 4 || report.RemovedFields != 3 {
		t.Fatalf("unexpected sanitization report: %+v", report)
	}
	if input["events"].([]any)[0].(map[string]any)["password"] != "hunter2" {
		t.Fatal("sanitize mutated its input")
	}
}

func TestSanitizeUnderstandsSemanticDOMAttributeArrays(t *testing.T) {
	input := map[string]any{
		"events": []any{},
		"snapshots": []any{
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "body", "children": []any{
					map[string]any{
						"type": "element", "tagName": "script", "children": []any{
							map[string]any{"type": "text", "text": "server-script-sentinel"},
						},
					},
					map[string]any{
						"type": "element", "tagName": "input",
						"attributes": []any{
							map[string]any{"name": "type", "value": "password"},
							map[string]any{"name": "value", "value": "semantic-password"},
							map[string]any{"name": "onclick", "value": "steal()"},
						},
					},
					map[string]any{
						"type": "element", "tagName": "input",
						"attributes": []any{
							map[string]any{"name": "name", "value": "username"},
							map[string]any{"name": "value", "value": "server-account-alias"},
						},
					},
					map[string]any{
						"type": "element", "tagName": "a",
						"rendered": false,
						"attributes": []any{
							map[string]any{"name": "href", "value": "data:text/html,unsafe"},
						},
					},
					map[string]any{
						"type": "element", "tagName": "form",
						"attributes": []any{
							map[string]any{"name": "action", "value": "https://user:pass@example.com/submit?token=url-secret"},
						},
					},
					map[string]any{
						"type": "element", "tagName": "div",
						"attributes": []any{
							map[string]any{"name": "id", "value": "unsafe-markup"},
						},
						"innerHTML": "<span>server-innerhtml-sentinel</span>",
					},
				},
			}},
			map[string]any{"domTree": map[string]any{"type": "element", "tagName": "html"}},
		},
	}

	out, report := Sanitize(input)
	serialized, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(serialized)
	for _, forbidden := range []string{"server-script-sentinel", "server-innerhtml-sentinel", "semantic-password", "server-account-alias", "steal()", "data:text/html,unsafe", "user:pass", "url-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("semantic sanitizer retained %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, Redacted) || !strings.Contains(text, RemovedURL) {
		t.Fatalf("semantic sanitizer did not retain explicit redaction markers: %s", text)
	}
	if !strings.Contains(text, `"rendered":false`) {
		t.Fatalf("semantic sanitizer discarded rendered-state evidence: %s", text)
	}
	firstTree := out["snapshots"].([]any)[0].(map[string]any)["domTree"].(map[string]any)
	bodyProvenance := firstTree["sanitization"].(map[string]any)
	if bodyProvenance["markupAltered"] != true || bodyProvenance["contentOmitted"] != true {
		t.Fatalf("removed semantic child lacked local provenance: %#v", bodyProvenance)
	}
	passwordInput := firstTree["children"].([]any)[0].(map[string]any)
	passwordProvenance := passwordInput["sanitization"].(map[string]any)
	if passwordProvenance["markupAltered"] != true ||
		!reflect.DeepEqual(passwordProvenance["alteredAttributes"], []string{"value"}) {
		t.Fatalf("redacted/removed semantic attributes lacked bounded provenance: %#v", passwordProvenance)
	}
	unsafeMarkup := firstTree["children"].([]any)[4].(map[string]any)
	unsafeMarkupProvenance := unsafeMarkup["sanitization"].(map[string]any)
	if unsafeMarkupProvenance["markupAltered"] != true ||
		unsafeMarkupProvenance["contentOmitted"] != true {
		t.Fatalf("removed content-bearing markup lacked content provenance: %#v", unsafeMarkupProvenance)
	}
	if report.RedactedValues < 2 || report.RemovedFields < 1 {
		t.Fatalf("unexpected semantic sanitization report: %+v", report)
	}
	if got := input["snapshots"].([]any)[0].(map[string]any)["domTree"].(map[string]any)["children"].([]any)[1].(map[string]any)["attributes"].([]any)[1].(map[string]any)["value"]; got != "semantic-password" {
		t.Fatalf("sanitize mutated semantic input: %v", got)
	}
	archive, _, err := BuildArchive(out)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := archive.Reconstruct()
	if err != nil {
		t.Fatal(err)
	}
	reconstructedTree := reconstructed["snapshots"].([]any)[0].(map[string]any)["domTree"].(map[string]any)
	if !reflect.DeepEqual(reconstructedTree["sanitization"], bodyProvenance) {
		t.Fatalf("archive lost sanitization provenance: %#v", reconstructedTree["sanitization"])
	}
}

func TestSanitizeMergesProvenanceMonotonicallyAndBoundsMalformedInput(t *testing.T) {
	tooMany := make([]any, maxSanitizationAttributeNames+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("data-%d", index)
	}
	input := map[string]any{
		"snapshots": []any{
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "main",
				"sanitization": map[string]any{
					"markupAltered": true, "alteredAttributes": []any{"class"},
				},
				"children": []any{
					map[string]any{
						"type": "element", "tagName": "script",
						"children": []any{map[string]any{"type": "text", "text": "secret=removed"}},
					},
				},
			}},
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "section",
				"sanitization": map[string]any{
					"markupAltered": false, "alteredAttributes": tooMany,
				},
			}},
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "div",
				"attributes": []any{
					map[string]any{"name": "token=server-secret", "value": "safe"},
				},
			}},
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "aside",
				"sanitization": nil,
			}},
			map[string]any{"domTree": map[string]any{
				"type": "element", "tagName": "article",
				"children": []any{
					"untyped child content",
					map[string]any{"type": "element", "tagName": ""},
					map[string]any{"type": "text", "text": "hidden text", "rendered": false},
				},
			}},
		},
	}

	out, _ := Sanitize(input)
	snapshots := out["snapshots"].([]any)
	first := snapshots[0].(map[string]any)["domTree"].(map[string]any)["sanitization"].(map[string]any)
	if first["markupAltered"] != true || first["contentOmitted"] != true ||
		!reflect.DeepEqual(first["alteredAttributes"], []string{"class"}) {
		t.Fatalf("derived provenance did not merge monotonically: %#v", first)
	}
	second := snapshots[1].(map[string]any)["domTree"].(map[string]any)["sanitization"].(map[string]any)
	if second["markupAltered"] != true || second["contentOmitted"] != true ||
		!reflect.DeepEqual(second["alteredAttributes"], []string{"*"}) {
		t.Fatalf("malformed or oversized provenance did not fail closed: %#v", second)
	}
	third := snapshots[2].(map[string]any)["domTree"].(map[string]any)["sanitization"].(map[string]any)
	if third["markupAltered"] != true ||
		!reflect.DeepEqual(third["alteredAttributes"], []string{"*"}) {
		t.Fatalf("sanitized attribute identity did not fail closed for every attr: %#v", third)
	}
	fourth := snapshots[3].(map[string]any)["domTree"].(map[string]any)["sanitization"].(map[string]any)
	if fourth["markupAltered"] != true || fourth["contentOmitted"] != true ||
		!reflect.DeepEqual(fourth["alteredAttributes"], []string{"*"}) {
		t.Fatalf("explicit null provenance was treated as clean absence: %#v", fourth)
	}
	fifthTree := snapshots[4].(map[string]any)["domTree"].(map[string]any)
	fifth := fifthTree["sanitization"].(map[string]any)
	if fifth["markupAltered"] != true || fifth["contentOmitted"] != true {
		t.Fatalf("invalid semantic children were dropped without parent provenance: %#v", fifth)
	}
	children := fifthTree["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("invalid semantic children were retained: %#v", children)
	}
	textEvidence := children[0].(map[string]any)["sanitization"].(map[string]any)
	if textEvidence["markupAltered"] != true || textEvidence["contentOmitted"] != true {
		t.Fatalf("text-node rendered marker was discarded without provenance: %#v", textEvidence)
	}
}
