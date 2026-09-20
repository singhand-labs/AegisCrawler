package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

// ErrLoopbackResponseMissing is returned when no archived response matches the
// canonical request hash. Loopback never falls back to a network call, so a
// miss is always terminal.
var ErrLoopbackResponseMissing = errors.New("loopback response missing for request hash")

// loopbackMaxArchiveBytes bounds the on-disk archive the loopback provider will
// read into memory. It is an unexported var so focused tests can lower it
// temporarily without writing a multi-mebibyte fixture.
var loopbackMaxArchiveBytes = 16 << 20

var loopbackRequestHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

type archivedResponse struct {
	RequestHash  string `json:"requestHash"`
	Content      string `json:"content"`
	ResponseID   string `json:"responseId"`
	FinishReason string `json:"finishReason"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
}

type loopbackArchiveEnvelope struct {
	Responses []archivedResponse `json:"responses"`
}

type loopbackProvider struct {
	name    string
	model   string
	archive map[string]*archivedResponse
}

// NewLoopbackProvider loads an exact-match response archive from archivePath and
// returns a provider that serves responses by canonical request hash. It never
// performs network I/O, never retries, and never fuzzy-matches: an unknown
// request hash is a terminal failure.
func NewLoopbackProvider(name, archivePath, model string) (*loopbackProvider, error) {
	if name == "" {
		name = "loopback"
	}
	if archivePath == "" {
		return nil, errors.New("loopback provider requires an archive path")
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("loopback provider cannot open archive: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(loopbackMaxArchiveBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("loopback provider cannot read archive: %w", err)
	}
	if len(raw) > loopbackMaxArchiveBytes {
		return nil, fmt.Errorf("loopback archive exceeds %d bytes", loopbackMaxArchiveBytes)
	}
	var envelope loopbackArchiveEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("loopback provider cannot decode archive: %w", err)
	}
	archive := make(map[string]*archivedResponse, len(envelope.Responses))
	for i := range envelope.Responses {
		entry := envelope.Responses[i]
		if !loopbackRequestHashRe.MatchString(entry.RequestHash) {
			return nil, fmt.Errorf("loopback archive entry has invalid requestHash %q", entry.RequestHash)
		}
		if entry.InputTokens < 0 {
			return nil, fmt.Errorf("loopback archive entry %s has negative inputTokens", entry.RequestHash)
		}
		if entry.OutputTokens < 0 {
			return nil, fmt.Errorf("loopback archive entry %s has negative outputTokens", entry.RequestHash)
		}
		if _, exists := archive[entry.RequestHash]; exists {
			return nil, fmt.Errorf("loopback archive has duplicate requestHash %s", entry.RequestHash)
		}
		entryCopy := entry
		archive[entry.RequestHash] = &entryCopy
	}
	return &loopbackProvider{name: name, model: model, archive: archive}, nil
}

func (p *loopbackProvider) Name() string { return p.name }

func (p *loopbackProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	hash := llm.CanonicalCompletionRequestHash(req)
	entry, ok := p.archive[hash]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrLoopbackResponseMissing, hash)
	}
	return &llm.CompletionResponse{
		Content:      entry.Content,
		InputTokens:  entry.InputTokens,
		OutputTokens: entry.OutputTokens,
		ResponseID:   entry.ResponseID,
		FinishReason: entry.FinishReason,
	}, nil
}
