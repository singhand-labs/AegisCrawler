package recording

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ArchiveVersion = 1

var ErrSnapshotsRequired = errors.New("recording requires at least an initial and final snapshot")

// SnapshotDelta is a lossless byte delta between canonical JSON snapshots.
// Applying PrefixBytes + Insert + the unchanged suffix to the preceding
// snapshot reconstructs the next point-in-time snapshot exactly.
type SnapshotDelta struct {
	PrefixBytes  int    `json:"prefixBytes"`
	RemovedBytes int    `json:"removedBytes"`
	Insert       string `json:"insert"`
}

// Archive separates the initial full snapshot from lossless deltas while
// retaining all other sanitized recording fields unchanged.
type Archive struct {
	Version         int             `json:"version"`
	SnapshotField   string          `json:"snapshotField"`
	Base            map[string]any  `json:"base"`
	InitialSnapshot json.RawMessage `json:"initialSnapshot"`
	SnapshotDeltas  []SnapshotDelta `json:"snapshotDeltas"`
}

type ArchiveStats struct {
	ActionCount   int
	SnapshotCount int
}

func BuildArchive(input map[string]any) (*Archive, ArchiveStats, error) {
	base := deepCopyMap(input)
	snapshotField := "snapshots"
	rawSnapshots, ok := base[snapshotField]
	if !ok {
		snapshotField = "domSnapshots"
		rawSnapshots, ok = base[snapshotField]
	}
	snapshots, ok := rawSnapshots.([]any)
	if !ok || len(snapshots) < 2 {
		return nil, ArchiveStats{}, ErrSnapshotsRequired
	}
	delete(base, snapshotField)

	encoded := make([][]byte, len(snapshots))
	for i, snapshot := range snapshots {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, ArchiveStats{}, fmt.Errorf("marshal snapshot %d: %w", i, err)
		}
		encoded[i] = data
	}
	deltas := make([]SnapshotDelta, 0, len(encoded)-1)
	for i := 1; i < len(encoded); i++ {
		deltas = append(deltas, makeDelta(encoded[i-1], encoded[i]))
	}

	actionCount := 0
	if events, ok := base["events"].([]any); ok {
		actionCount = len(events)
	}
	return &Archive{
		Version:         ArchiveVersion,
		SnapshotField:   snapshotField,
		Base:            base,
		InitialSnapshot: append(json.RawMessage(nil), encoded[0]...),
		SnapshotDeltas:  deltas,
	}, ArchiveStats{ActionCount: actionCount, SnapshotCount: len(snapshots)}, nil
}

func (a *Archive) Reconstruct() (map[string]any, error) {
	if a == nil || a.Version != ArchiveVersion || len(a.InitialSnapshot) == 0 {
		return nil, errors.New("invalid recording archive")
	}
	out := deepCopyMap(a.Base)
	encoded := append([]byte(nil), a.InitialSnapshot...)
	snapshots := make([]any, 0, len(a.SnapshotDeltas)+1)
	decode := func(data []byte) error {
		var snapshot any
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return err
		}
		snapshots = append(snapshots, snapshot)
		return nil
	}
	if err := decode(encoded); err != nil {
		return nil, fmt.Errorf("decode initial snapshot: %w", err)
	}
	for i, delta := range a.SnapshotDeltas {
		var err error
		encoded, err = applyDelta(encoded, delta)
		if err != nil {
			return nil, fmt.Errorf("apply snapshot delta %d: %w", i, err)
		}
		if err := decode(encoded); err != nil {
			return nil, fmt.Errorf("decode snapshot delta %d: %w", i, err)
		}
	}
	field := a.SnapshotField
	if field == "" {
		field = "snapshots"
	}
	out[field] = snapshots
	return out, nil
}

func makeDelta(previous, current []byte) SnapshotDelta {
	prefix := 0
	for prefix < len(previous) && prefix < len(current) && previous[prefix] == current[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(previous)-prefix && suffix < len(current)-prefix && previous[len(previous)-1-suffix] == current[len(current)-1-suffix] {
		suffix++
	}
	return SnapshotDelta{
		PrefixBytes:  prefix,
		RemovedBytes: len(previous) - prefix - suffix,
		Insert:       string(current[prefix : len(current)-suffix]),
	}
}

func applyDelta(previous []byte, delta SnapshotDelta) ([]byte, error) {
	if delta.PrefixBytes < 0 || delta.RemovedBytes < 0 || delta.PrefixBytes+delta.RemovedBytes > len(previous) {
		return nil, errors.New("invalid snapshot delta bounds")
	}
	suffixStart := delta.PrefixBytes + delta.RemovedBytes
	out := make([]byte, 0, delta.PrefixBytes+len(delta.Insert)+len(previous)-suffixStart)
	out = append(out, previous[:delta.PrefixBytes]...)
	out = append(out, delta.Insert...)
	out = append(out, previous[suffixStart:]...)
	return out, nil
}

func deepCopyMap(input map[string]any) map[string]any {
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}
