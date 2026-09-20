package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

func TestCompletionExecutionPolicyPreservesDefaultHashAndBindsAtMostOnce(t *testing.T) {
	request := CompletionRequest{
		Model: "model", System: "system", User: "user",
		Temperature: 0.25, JSONMode: true,
	}
	legacy, _ := json.Marshal(struct {
		Model       string
		System      string
		User        string
		Temperature float64
		JSONMode    bool
	}{
		request.Model, request.System, request.User, request.Temperature, request.JSONMode,
	})
	digest := sha256.Sum256(legacy)
	if got, want := CanonicalCompletionRequestHash(request), fmt.Sprintf("%x", digest); got != want {
		t.Fatalf("default execution policy changed historical request hash: got=%s want=%s json=%s", got, want, legacy)
	}
	request.ExecutionPolicy = CompletionExecutionAtMostOnce
	if CanonicalCompletionRequestHash(request) == fmt.Sprintf("%x", digest) {
		t.Fatal("at-most-once execution policy was not bound into the request hash")
	}

	request.ExecutionPolicy = CompletionExecutionDefault
	request.MaxOutputTokens = 4096
	if CanonicalCompletionRequestHash(request) == fmt.Sprintf("%x", digest) {
		t.Fatal("positive max output tokens were not bound into the request hash")
	}
	request.MaxOutputTokens = 0
	if got, want := CanonicalCompletionRequestHash(request), fmt.Sprintf("%x", digest); got != want {
		t.Fatalf("zero max output tokens changed historical request hash: got=%s want=%s", got, want)
	}
}

type traceTestCompleter struct {
	result *CompletionResult
	err    error
}

func (c traceTestCompleter) Complete(context.Context, CompletionRequest) (*CompletionResult, error) {
	return c.result, c.err
}

type traceTestSink struct {
	event          CompletionTraceEvent
	events         []CompletionTraceEvent
	err            error
	ctx            context.Context
	inspectContext func(context.Context)
}

func (s *traceTestSink) CaptureCompletion(ctx context.Context, event CompletionTraceEvent) error {
	s.ctx = ctx
	if s.inspectContext != nil {
		s.inspectContext(ctx)
	}
	s.event = event
	s.events = append(s.events, event)
	return s.err
}

func TestCanonicalCompletionRequestHashIsStableAndComplete(t *testing.T) {
	request := CompletionRequest{Model: "m", System: "system", User: "user", Temperature: 0.2, JSONMode: true}
	first := CanonicalCompletionRequestHash(request)
	second := CanonicalCompletionRequestHash(request)
	if first == "" || first != second {
		t.Fatalf("canonical request hash is unstable: %q %q", first, second)
	}
	request.User = "different"
	if CanonicalCompletionRequestHash(request) == first {
		t.Fatal("request hash ignored prompt content")
	}
	request.User = "user"
	request.StructuredOutput = &StructuredOutput{
		Name:   "submit_rule",
		Schema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
	}
	structured := CanonicalCompletionRequestHash(request)
	if structured == first {
		t.Fatal("request hash ignored strict structured output")
	}
	request.StructuredOutput.Schema = json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"],"additionalProperties":false}`)
	if CanonicalCompletionRequestHash(request) == structured {
		t.Fatal("request hash ignored strict structured output schema content")
	}
}

func TestCompleteWithTraceCapturesBeforeReturningAndFailsClosed(t *testing.T) {
	request := CompletionRequest{Model: "m", System: "system", User: "user", JSONMode: true}
	result := &CompletionResult{
		CompletionResponse: &CompletionResponse{Content: `{"ok":true}`},
		Provider:           "fake", Model: "m",
	}
	chunkIndex := 0
	sink := &traceTestSink{}
	ctx := WithCompletionTraceSink(context.Background(), sink)
	got, err := CompleteWithTrace(ctx, traceTestCompleter{result: result}, request, CompletionTraceMetadata{
		Phase: CompletionPhaseAnalysis, ChunkIndex: &chunkIndex, ChunkCount: 2,
	})
	if err != nil || got != result {
		t.Fatalf("traced completion failed: result=%+v err=%v", got, err)
	}
	if sink.event.Result != result || sink.event.RequestHash != CanonicalCompletionRequestHash(request) ||
		sink.event.Metadata.Phase != CompletionPhaseAnalysis ||
		!sink.event.OriginalBytesExact {
		t.Fatalf("unexpected trace event: %+v", sink.event)
	}

	sink.err = errors.New("database unavailable")
	got, err = CompleteWithTrace(ctx, traceTestCompleter{result: result}, request, CompletionTraceMetadata{Phase: CompletionPhaseFinal})
	if got != nil || !errors.Is(err, ErrCompletionCapture) {
		t.Fatalf("capture failure released provider response: result=%+v err=%v", got, err)
	}
}

func TestCompleteWithTracePersistsWithBoundedContextAfterCancellation(t *testing.T) {
	type contextKey struct{}
	parent := context.WithValue(context.Background(), contextKey{}, "workspace-value")
	cancelled, cancel := context.WithCancel(parent)
	cancel()
	var captureErr error
	var captureValue any
	var captureDeadline time.Time
	var captureHasDeadline bool
	sink := &traceTestSink{inspectContext: func(ctx context.Context) {
		captureErr = ctx.Err()
		captureValue = ctx.Value(contextKey{})
		captureDeadline, captureHasDeadline = ctx.Deadline()
	}}
	result := &CompletionResult{
		CompletionResponse: &CompletionResponse{Content: `{"ok":true}`},
		Provider:           "fake",
		Model:              "model",
	}

	got, err := CompleteWithTrace(
		WithCompletionTraceSink(cancelled, sink),
		traceTestCompleter{result: result},
		CompletionRequest{Model: "model", User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil || got != result {
		t.Fatalf("cancelled parent prevented durable capture: result=%+v err=%v", got, err)
	}
	if sink.ctx == nil || captureErr != nil || captureValue != "workspace-value" {
		t.Fatalf("capture context did not retain values without cancellation: err=%v value=%v", captureErr, captureValue)
	}
	if !captureHasDeadline {
		t.Fatal("capture context did not have a bounded deadline")
	}
	remaining := time.Until(captureDeadline)
	if remaining <= 0 || remaining > completionCaptureTimeout {
		t.Fatalf("unexpected capture deadline: remaining=%v limit=%v", remaining, completionCaptureTimeout)
	}
}

func TestCompleteWithTraceRedactsErrorMetadataWithoutSynthesizingProviderBody(t *testing.T) {
	request := CompletionRequest{Model: "m", User: "user"}
	rawError := errors.New("token:x")
	sink := &traceTestSink{}
	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		traceTestCompleter{err: rawError},
		request,
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if result != nil || !errors.Is(err, rawError) {
		t.Fatalf("unexpected traced error result: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.event.OriginalBytes != 0 ||
		!sink.event.OriginalBytesExact ||
		sink.event.Result.Content != "" ||
		!sink.event.ErrorRedacted ||
		sink.event.ErrorMessage != "[REDACTED]" {
		t.Fatalf("provider error metadata was not safely separated from body content: %+v", sink.event)
	}
}

func TestCompleteWithTraceRejectsEmptyResponseWithoutSink(t *testing.T) {
	for _, result := range []*CompletionResult{
		nil,
		{},
	} {
		got, err := CompleteWithTrace(
			context.Background(),
			traceTestCompleter{result: result},
			CompletionRequest{User: "user"},
			CompletionTraceMetadata{Phase: CompletionPhaseFinal},
		)
		if got != nil || err == nil {
			t.Fatalf("empty untraced completion was accepted: result=%+v err=%v", got, err)
		}
	}
}

func TestCompletionTraceFailsClosedOnDispatchRequestHashMismatch(t *testing.T) {
	sink := &traceTestSink{}
	state := &completionTraceState{
		sink:     sink,
		metadata: CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	}
	ctx := context.WithValue(context.Background(), completionTraceStateContextKey{}, state)
	ctx = withCompletionDispatchLineage(ctx, PreparedCall{
		Route:             "primary",
		PolicyFingerprint: "policy-a",
		PriceRevision:     "revision-a",
		RequestHash: CanonicalCompletionRequestHash(
			CompletionRequest{Model: "model", User: "admitted"},
		),
	}, budget.DispatchIdentity{
		WorkspaceID:     "default",
		OperationKind:   budget.OperationDSL,
		OperationID:     "job-1:final",
		LogicalAttempt:  1,
		PhysicalOrdinal: budget.OrdinalPrimary,
	})

	err := TraceProviderResponse(
		ctx,
		CompletionRequest{Model: "model", User: "different"},
		"provider",
		"model",
		&CompletionResponse{Content: "must not escape"},
		200,
	)
	if !errors.Is(err, ErrCompletionCapture) {
		t.Fatalf("request-hash mismatch returned %v, want ErrCompletionCapture", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("request-hash mismatch reached the durable sink: %+v", sink.events)
	}
}

func TestSanitizeCompletionMetadataRedactsAndBoundsProviderValues(t *testing.T) {
	got := SanitizeCompletionMetadata("token: provider-secret")
	if strings.Contains(got, "provider-secret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("provider metadata was not redacted: %q", got)
	}
	got = SanitizeCompletionMetadata(strings.Repeat("界", 200))
	if len(got) > 256 {
		t.Fatalf("provider metadata exceeded byte bound: %d", len(got))
	}
	if !strings.HasPrefix(strings.Repeat("界", 200), got) {
		t.Fatal("provider metadata bound did not preserve a valid prefix")
	}
}
