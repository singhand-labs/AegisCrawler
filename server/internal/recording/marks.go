package recording

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const (
	maxPageMarks         = 24
	maxPageMarkNoteChars = 200
)

type PageMarkRole = models.PageMarkRole

type PageMarkRect = models.PageMarkRect

type PageMarkElement = models.PageMarkElement

type PageMark = models.PageMark

const (
	PageMarkRoleListItem = models.PageMarkRoleListItem
	PageMarkRoleField    = models.PageMarkRoleField
	PageMarkRoleNextPage = models.PageMarkRoleNextPage
	PageMarkRoleInput    = models.PageMarkRoleInput
	PageMarkRoleExclude  = models.PageMarkRoleExclude
)

func EffectivePageMarks(recording map[string]any, override *[]PageMark) ([]PageMark, string, error) {
	if override != nil {
		return NormalizePageMarks(*override)
	}
	return ParsePageMarks(recording)
}

func ParsePageMarks(recording map[string]any) ([]PageMark, string, error) {
	if recording == nil || recording["marks"] == nil {
		return NormalizePageMarks(nil)
	}
	encoded, err := json.Marshal(recording["marks"])
	if err != nil {
		return nil, "", fmt.Errorf("invalid page marks: %w", err)
	}
	var marks []PageMark
	if err := json.Unmarshal(encoded, &marks); err != nil {
		return nil, "", fmt.Errorf("invalid page marks: %w", err)
	}
	return NormalizePageMarks(marks)
}

func NormalizePageMarks(input []PageMark) ([]PageMark, string, error) {
	if len(input) > maxPageMarks {
		return nil, "", fmt.Errorf("invalid page marks: maximum mark count of %d exceeded", maxPageMarks)
	}
	out := make([]PageMark, 0, len(input))
	seen := map[string]int{}
	for _, mark := range input {
		normalized, err := normalizePageMark(mark)
		if err != nil {
			return nil, "", err
		}
		key := normalized.CanonicalID
		if prior, ok := seen[key]; ok {
			out[prior] = normalized
			continue
		}
		seen[key] = len(out)
		out = append(out, normalized)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Timestamp == out[j].Timestamp {
			return out[i].CanonicalID < out[j].CanonicalID
		}
		return out[i].Timestamp < out[j].Timestamp
	})
	digest, err := pageMarksDigest(out)
	if err != nil {
		return nil, "", err
	}
	return out, digest, nil
}

func normalizePageMark(mark PageMark) (PageMark, error) {
	mark.ID = strings.TrimSpace(mark.ID)
	mark.URL = strings.TrimSpace(mark.URL)
	mark.State = strings.TrimSpace(mark.State)
	mark.Note = strings.TrimSpace(mark.Note)
	mark.Element.Selector = strings.TrimSpace(mark.Element.Selector)
	mark.Element.StableSelector = strings.TrimSpace(mark.Element.StableSelector)
	mark.Element.TagName = strings.ToLower(strings.TrimSpace(mark.Element.TagName))
	if mark.ID == "" {
		return PageMark{}, fmt.Errorf("invalid page mark: id is required")
	}
	if !validPageMarkRole(mark.Role) {
		return PageMark{}, fmt.Errorf("invalid page mark: unsupported role %q", mark.Role)
	}
	if len([]rune(mark.Note)) > maxPageMarkNoteChars {
		return PageMark{}, fmt.Errorf("invalid page mark: note exceeds %d characters", maxPageMarkNoteChars)
	}
	if mark.Element.Selector == "" {
		return PageMark{}, fmt.Errorf("invalid page mark: selector is required")
	}
	if mark.State == "" {
		mark.State = mark.URL
	}
	mark.CanonicalID = canonicalPageMarkID(mark)
	return mark, nil
}

func validPageMarkRole(role PageMarkRole) bool {
	switch role {
	case PageMarkRoleListItem, PageMarkRoleField, PageMarkRoleNextPage, PageMarkRoleInput, PageMarkRoleExclude:
		return true
	default:
		return false
	}
}

func canonicalPageMarkID(mark PageMark) string {
	sequence := ""
	if mark.SnapshotSequence != nil {
		sequence = fmt.Sprintf("%d", *mark.SnapshotSequence)
	}
	material := strings.Join([]string{mark.State, mark.Element.Selector, string(mark.Role), sequence}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return "m_" + base64.RawURLEncoding.EncodeToString(sum[:9])
}

func pageMarksDigest(marks []PageMark) (string, error) {
	encoded, err := json.Marshal(struct {
		Version string     `json:"version"`
		Marks   []PageMark `json:"marks"`
	}{Version: "page-marks-v1", Marks: marks})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
