package providers

import (
	"encoding/json"
	"sort"
)

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func safeIntSum(left, right int) (int, bool) {
	if right > 0 && left > int(^uint(0)>>1)-right {
		return 0, false
	}
	minInt := -int(^uint(0)>>1) - 1
	if right < 0 && left < minInt-right {
		return 0, false
	}
	return left + right, true
}

// unknownUsageCategories returns top-level usage members that an adapter has
// not proved are represented by the normalized v1 price vocabulary. The main
// response decode has already validated the JSON envelope; a defensive decode
// failure here leaves the list empty and the missing presence bits still force
// conservative settlement.
func unknownUsageCategories(payload []byte, allowed map[string]struct{}) []string {
	var envelope struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	unknown := make([]string, 0)
	for category := range envelope.Usage {
		if _, ok := allowed[category]; !ok {
			unknown = append(unknown, category)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// unknownNestedUsageCategories applies the same fail-closed vocabulary check
// to an object nested directly under usage. The returned names include their
// container so metrics and durable classifications remain bounded while
// operators can distinguish the unsupported pricing partition.
func unknownNestedUsageCategories(
	payload []byte,
	container string,
	allowed map[string]struct{},
) []string {
	var envelope struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	raw, ok := envelope.Usage[container]
	if !ok || string(raw) == "null" {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil
	}
	unknown := make([]string, 0)
	for category := range members {
		if _, ok := allowed[category]; !ok {
			unknown = append(unknown, container+"."+category)
		}
	}
	sort.Strings(unknown)
	return unknown
}
