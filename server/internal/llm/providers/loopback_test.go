package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

func providerConfigWithArchive(path string) config.ProviderConfig {
	return config.ProviderConfig{
		Provider:            "loopback",
		LoopbackArchivePath: path,
		Model:               "m",
	}
}

func writeLoopbackArchive(t *testing.T, envelope loopbackArchiveEnvelope) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.json")
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}

func sampleRequest() llm.CompletionRequest {
	return llm.CompletionRequest{
		Model:    "m",
		System:   "s",
		User:     "u",
		JSONMode: true,
	}
}

func TestLoopbackProvider_ExactMatch(t *testing.T) {
	req := sampleRequest()
	hash := llm.CanonicalCompletionRequestHash(req)
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{
				RequestHash:  hash,
				Content:      "{\"ok\":true}",
				ResponseID:   "resp-1",
				FinishReason: "stop",
				InputTokens:  11,
				OutputTokens: 22,
			},
		},
	})
	p, err := NewLoopbackProvider("exact", path, "m")
	if err != nil {
		t.Fatalf("new loopback provider: %v", err)
	}
	if got := p.Name(); got != "exact" {
		t.Fatalf("name = %q, want %q", got, "exact")
	}
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content != "{\"ok\":true}" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.ResponseID != "resp-1" {
		t.Fatalf("responseId = %q", resp.ResponseID)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finishReason = %q", resp.FinishReason)
	}
	if resp.InputTokens != 11 {
		t.Fatalf("inputTokens = %d", resp.InputTokens)
	}
	if resp.OutputTokens != 22 {
		t.Fatalf("outputTokens = %d", resp.OutputTokens)
	}
}

func TestLoopbackProvider_DefaultName(t *testing.T) {
	req := sampleRequest()
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{{
			RequestHash: llm.CanonicalCompletionRequestHash(req),
			Content:     "cached",
		}},
	})
	p, err := NewLoopbackProvider("", path, "m")
	if err != nil {
		t.Fatalf("new loopback provider: %v", err)
	}
	if got := p.Name(); got != "loopback" {
		t.Fatalf("name = %q, want loopback", got)
	}
}

func TestLoopbackProvider_Miss(t *testing.T) {
	req := sampleRequest()
	hash := llm.CanonicalCompletionRequestHash(req)
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{RequestHash: hash, Content: "cached"},
		},
	})
	p, err := NewLoopbackProvider("miss", path, "m")
	if err != nil {
		t.Fatalf("new loopback provider: %v", err)
	}
	other := sampleRequest()
	other.User = "different-prompt"
	_, err = p.Complete(context.Background(), other)
	if !errors.Is(err, ErrLoopbackResponseMissing) {
		t.Fatalf("err = %v, want ErrLoopbackResponseMissing", err)
	}
	if !strings.Contains(err.Error(), llm.CanonicalCompletionRequestHash(other)) {
		t.Fatalf("error message %q does not mention the request hash", err.Error())
	}
}

func TestLoopbackProvider_BindsExactStructuredOutputSchema(t *testing.T) {
	request := sampleRequest()
	request.StructuredOutput = &llm.StructuredOutput{
		Name: "submit_rule",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"rule":{"type":"string"}},
			"required":["rule"],
			"additionalProperties":false
		}`),
	}
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{{
			RequestHash: llm.CanonicalCompletionRequestHash(request),
			Content:     `{"rule":"exact"}`,
		}},
	})
	provider, err := NewLoopbackProvider("strict", path, "m")
	if err != nil {
		t.Fatal(err)
	}
	if response, err := provider.Complete(context.Background(), request); err != nil ||
		response.Content != `{"rule":"exact"}` {
		t.Fatalf("exact structured request missed archive: response=%+v err=%v", response, err)
	}

	changed := request
	changed.StructuredOutput = &llm.StructuredOutput{
		Name: "submit_rule",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"rule":{"type":"string"},"hash":{"type":"string"}},
			"required":["hash","rule"],
			"additionalProperties":false
		}`),
	}
	if _, err := provider.Complete(context.Background(), changed); !errors.Is(err, ErrLoopbackResponseMissing) {
		t.Fatalf("changed strict schema reused an archived response: %v", err)
	}
}

func TestLoopbackProvider_DuplicateHash(t *testing.T) {
	req := sampleRequest()
	hash := llm.CanonicalCompletionRequestHash(req)
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{RequestHash: hash, Content: "a"},
			{RequestHash: hash, Content: "b"},
		},
	})
	if _, err := NewLoopbackProvider("dup", path, "m"); err == nil {
		t.Fatal("expected error for duplicate requestHash")
	}
}

func TestLoopbackProvider_EmptyArchivePath(t *testing.T) {
	if _, err := NewLoopbackProvider("empty", "", "m"); err == nil {
		t.Fatal("expected error for empty archive path")
	}
}

func TestLoopbackProvider_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if _, err := NewLoopbackProvider("malformed", path, "m"); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestLoopbackProvider_InvalidRequestHash(t *testing.T) {
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{RequestHash: "ZZZ", Content: "c"},
		},
	})
	if _, err := NewLoopbackProvider("invalid", path, "m"); err == nil {
		t.Fatal("expected error for invalid requestHash")
	}
}

func TestLoopbackProvider_NegativeTokens(t *testing.T) {
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{RequestHash: strings.Repeat("a", 64), Content: "c", InputTokens: -1},
		},
	})
	if _, err := NewLoopbackProvider("neg", path, "m"); err == nil {
		t.Fatal("expected error for negative tokens")
	}
}

func TestLoopbackProvider_OversizedArchive(t *testing.T) {
	prev := loopbackMaxArchiveBytes
	loopbackMaxArchiveBytes = 16
	t.Cleanup(func() { loopbackMaxArchiveBytes = prev })
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{
			{RequestHash: strings.Repeat("a", 64), Content: strings.Repeat("x", 64)},
		},
	})
	if _, err := NewLoopbackProvider("oversize", path, "m"); err == nil {
		t.Fatal("expected error for oversized archive")
	}
}

func TestLoopbackProvider_CancelledContext(t *testing.T) {
	req := sampleRequest()
	hash := llm.CanonicalCompletionRequestHash(req)
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{{RequestHash: hash, Content: "c"}},
	})
	p, err := NewLoopbackProvider("cancel", path, "m")
	if err != nil {
		t.Fatalf("new loopback provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Complete(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLoopbackProvider_MissingFile(t *testing.T) {
	if _, err := NewLoopbackProvider("missing", filepath.Join(t.TempDir(), "nope.json"), "m"); err == nil {
		t.Fatal("expected error for missing archive file")
	}
}

func TestBuild_Loopback(t *testing.T) {
	req := sampleRequest()
	hash := llm.CanonicalCompletionRequestHash(req)
	path := writeLoopbackArchive(t, loopbackArchiveEnvelope{
		Responses: []archivedResponse{{RequestHash: hash, Content: "c"}},
	})
	p, err := Build("loop", providerConfigWithArchive(path))
	if err != nil {
		t.Fatalf("build loopback: %v", err)
	}
	if p.Name() != "loop" {
		t.Fatalf("name = %q", p.Name())
	}
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content != "c" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestBuild_LoopbackRequiresArchivePath(t *testing.T) {
	if _, err := Build("loop", providerConfigWithArchive("")); err == nil {
		t.Fatal("expected error for missing archive path via factory")
	}
}
