package models

type PageMarkRole string

const (
	PageMarkRoleListItem PageMarkRole = "listItem"
	PageMarkRoleField    PageMarkRole = "field"
	PageMarkRoleNextPage PageMarkRole = "nextPage"
	PageMarkRoleInput    PageMarkRole = "input"
	PageMarkRoleExclude  PageMarkRole = "exclude"
)

type PageMarkRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type PageMarkElement struct {
	Index          int          `json:"index,omitempty"`
	TagName        string       `json:"tagName,omitempty"`
	Selector       string       `json:"selector"`
	StableSelector string       `json:"stableSelector,omitempty"`
	Text           string       `json:"text,omitempty"`
	AriaLabel      string       `json:"ariaLabel,omitempty"`
	Role           string       `json:"role,omitempty"`
	Placeholder    string       `json:"placeholder,omitempty"`
	Name           string       `json:"name,omitempty"`
	BoundingRect   PageMarkRect `json:"boundingRect,omitempty"`
}

type PageMark struct {
	ID               string          `json:"id"`
	CanonicalID      string          `json:"canonicalId,omitempty"`
	Timestamp        int64           `json:"timestamp"`
	URL              string          `json:"url"`
	Role             PageMarkRole    `json:"role"`
	Note             string          `json:"note"`
	Element          PageMarkElement `json:"element"`
	ActionIndex      *int            `json:"actionIndex,omitempty"`
	SnapshotSequence *int            `json:"snapshotSequence,omitempty"`
	State            string          `json:"state,omitempty"`
}
