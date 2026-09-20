package requirement

import (
	"github.com/singhand-labs/AegisCrawler/internal/llm/timelinetrim"
)

// trimTimelineItem reduces a single timeline item so its JSON encoding fits
// budgetBytes, delegating to the shared structural trimmer. Items that already
// fit are returned unchanged.
func trimTimelineItem(item timelineItem, budgetBytes int) (timelineItem, error) {
	trimmed, err := timelinetrim.Trim(timelinetrim.Item{
		Kind: item.Kind, Index: item.Index, Position: item.Position, Value: item.Value,
	}, budgetBytes)
	if err != nil {
		return item, err
	}
	return timelineItem{
		Kind: trimmed.Kind, Index: trimmed.Index, Position: trimmed.Position, Value: trimmed.Value,
	}, nil
}
