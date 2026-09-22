package recording

import (
	"testing"
)

func TestEffectivePageMarksUsesRecordingMarksByDefault(t *testing.T) {
	recording := map[string]any{
		"marks": []any{map[string]any{
			"id": "m1", "timestamp": float64(1), "url": "https://example.test/list",
			"role": "field", "note": "price", "actionIndex": float64(2), "snapshotSequence": float64(3),
			"element": map[string]any{
				"index": float64(4), "tagName": "span", "selector": ".price",
				"boundingRect": map[string]any{"x": float64(1), "y": float64(2), "width": float64(3), "height": float64(4)},
			},
		}},
	}
	marks, digest, err := EffectivePageMarks(recording, nil)
	if err != nil {
		t.Fatalf("EffectivePageMarks returned error: %v", err)
	}
	if len(marks) != 1 || marks[0].Role != PageMarkRoleField || marks[0].CanonicalID == "" || digest == "" {
		t.Fatalf("unexpected marks/digest: %#v %q", marks, digest)
	}
}

func TestEffectivePageMarksOverrideCanClearRecordingMarks(t *testing.T) {
	recording := map[string]any{"marks": []any{map[string]any{"id": "m1", "role": "field", "note": "x", "url": "u", "element": map[string]any{"selector": ".x"}}}}
	override := []PageMark{}
	marks, digest, err := EffectivePageMarks(recording, &override)
	if err != nil {
		t.Fatalf("EffectivePageMarks returned error: %v", err)
	}
	if len(marks) != 0 || digest == "" {
		t.Fatalf("empty override must clear marks and still produce digest, got %#v %q", marks, digest)
	}
}

func TestPageMarkCanonicalIDExcludesNoteButDigestIncludesNote(t *testing.T) {
	left := []PageMark{testPageMark("first note")}
	right := []PageMark{testPageMark("second note")}
	leftMarks, leftDigest, err := NormalizePageMarks(left)
	if err != nil {
		t.Fatalf("NormalizePageMarks left: %v", err)
	}
	rightMarks, rightDigest, err := NormalizePageMarks(right)
	if err != nil {
		t.Fatalf("NormalizePageMarks right: %v", err)
	}
	if leftMarks[0].CanonicalID != rightMarks[0].CanonicalID {
		t.Fatalf("canonical id must not include note: %q != %q", leftMarks[0].CanonicalID, rightMarks[0].CanonicalID)
	}
	if leftDigest == rightDigest {
		t.Fatalf("digest must include note changes")
	}
}

func TestNormalizePageMarksRejectsInvalidOrOverLimitMarks(t *testing.T) {
	tooMany := make([]PageMark, maxPageMarks+1)
	for i := range tooMany {
		tooMany[i] = testPageMark("ok")
		tooMany[i].ID = string(rune('a' + i))
	}
	if _, _, err := NormalizePageMarks(tooMany); err == nil {
		t.Fatalf("expected max mark count error")
	}
	invalid := testPageMark("x")
	invalid.Note = string(make([]byte, maxPageMarkNoteChars+1))
	if _, _, err := NormalizePageMarks([]PageMark{invalid}); err == nil {
		t.Fatalf("expected note length error")
	}
}

func testPageMark(note string) PageMark {
	return PageMark{
		ID: "m1", Timestamp: 1, URL: "https://example.test/list", Role: PageMarkRoleField, Note: note,
		ActionIndex: intPtr(2), SnapshotSequence: intPtr(3), State: "https://example.test/list",
		Element: PageMarkElement{Index: 4, TagName: "span", Selector: ".price", StableSelector: ".price", Text: "$10", BoundingRect: PageMarkRect{X: 1, Y: 2, Width: 3, Height: 4}},
	}
}

func intPtr(value int) *int { return &value }
