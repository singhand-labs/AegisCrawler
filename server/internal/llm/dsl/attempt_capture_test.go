package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"go.uber.org/zap"
)

type dslBoundaryCompleter struct {
	content             string
	preparedCatalogHash string
	overrideCatalogHash string
	addLocatorHints     bool
}

func (c *dslBoundaryCompleter) Complete(ctx context.Context, request llm.CompletionRequest) (*llm.CompletionResult, error) {
	firstErr := errors.New("primary temporarily unavailable")
	if err := llm.TraceProviderError(ctx, request, "primary", request.Model, 503, "upstream_error", "temporary", firstErr); err != nil {
		return nil, err
	}
	content := testProviderSelectorEnvelope(request, c.content)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err == nil {
		c.preparedCatalogHash, _ = envelope["selectorCatalogHash"].(string)
		if c.addLocatorHints {
			rule, _ := envelope["rule"].(map[string]any)
			steps, _ := rule["steps"].([]any)
			step, _ := steps[0].(map[string]any)
			target, _ := step["target"].(map[string]any)
			target["selector"] = "#provider-copy"
			target["stableSelector"] = "#provider-stable-copy"
		}
		if c.overrideCatalogHash != "" {
			envelope["selectorCatalogHash"] = c.overrideCatalogHash
		}
		if c.addLocatorHints || c.overrideCatalogHash != "" {
			if encoded, marshalErr := json.Marshal(envelope); marshalErr == nil {
				content = string(encoded)
			}
		}
	}
	response := &llm.CompletionResponse{
		Content: content, InputTokens: 17, OutputTokens: 9,
		ResponseID: "dsl-response", FinishReason: "stop",
	}
	if err := llm.TraceProviderResponse(ctx, request, "secondary", request.Model, response, 200); err != nil {
		return nil, err
	}
	return &llm.CompletionResult{
		CompletionResponse: response, Provider: "secondary", Model: request.Model,
	}, nil
}

func dslAttemptConfig() *config.Config {
	return &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 1,
		LLMJobMaxAttempts: 1, LLMRequestTimeout: time.Second,
		RecordingMaxActions: 500, RecordingMaxDuration: time.Hour,
		RecordingMaxCompressedBytes: 1024 * 1024,
	}
}

func TestDSLManagerCapturesFailoverProviderIRResolvedRuleAndValidation(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	generated.ID = ""
	generated.Version = ""
	generated.Name = ""
	generated.Domain = nil
	generated.Entry = ""
	completer := &dslBoundaryCompleter{
		content: ruleEnvelope(t, generated), addLocatorHints: true,
	}
	manager := NewDSLManager(
		persistence,
		cfg,
		NewDSLWorkflow(cfg, completer),
		zap.NewNop(),
	)
	_, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	attempts, err := manager.ListProviderAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("unexpected attempts: attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].Outcome != models.LLMAttemptSucceeded || attempts[0].CallCount != 2 ||
		attempts[0].BaselineHash == "" ||
		attempts[0].SelectorCatalogHash == "" ||
		attempts[0].SelectorCatalogHash != completer.preparedCatalogHash {
		t.Fatalf("unexpected attempt metadata: %+v", attempts[0])
	}
	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Calls) != 2 ||
		detail.Calls[0].CallKind != models.LLMProviderCallKindError ||
		detail.Calls[1].CallKind != models.LLMProviderCallKindResponse ||
		detail.Calls[0].ProviderAttempt != 1 || detail.Calls[1].ProviderAttempt != 2 {
		t.Fatalf("physical failover lineage was not retained: %+v", detail.Calls)
	}
	artifact, ok := detail.Artifact.(map[string]any)
	if !ok || artifact["providerIR"] == nil || artifact["resolvedRule"] == nil {
		t.Fatalf("provider and resolved forms were not retained: %#v", detail.Artifact)
	}
	providerIR, _ := artifact["providerIR"].(map[string]any)
	resolved, _ := artifact["resolvedRule"].(map[string]any)
	if artifact["providerIRSourceCallId"] != detail.Calls[1].ID {
		t.Fatalf("provider IR was not bound to its exact terminal response: artifact=%#v calls=%+v", artifact, detail.Calls)
	}
	if providerIR["id"] != "" || providerIR["entry"] != "" ||
		resolved["id"] != baseline.ID || resolved["entry"] != baseline.Entry {
		t.Fatalf("provider IR was conflated with trusted-restored rule: provider=%#v resolved=%#v", providerIR, resolved)
	}
	providerSteps, _ := providerIR["steps"].([]any)
	providerStep, _ := providerSteps[0].(map[string]any)
	providerTarget, _ := providerStep["target"].(map[string]any)
	resolvedSteps, _ := resolved["steps"].([]any)
	resolvedStep, _ := resolvedSteps[0].(map[string]any)
	resolvedTarget, _ := resolvedStep["target"].(map[string]any)
	if providerTarget["targetCandidateId"] == nil ||
		providerTarget["selector"] != "#provider-copy" ||
		providerTarget["stableSelector"] != "#provider-stable-copy" ||
		resolvedTarget["selector"] != ".name" ||
		resolvedTarget["stableSelector"] != nil ||
		resolvedTarget["targetCandidateId"] != nil {
		t.Fatalf("provider IR was not retained before opaque resolution: provider=%#v resolved=%#v", providerTarget, resolvedTarget)
	}
	if artifact["schemaVersion"] != "aegiscrawler.dsl-attempt.v2" {
		t.Fatalf("dsl attempt artifact schema was not v2: %#v", artifact["schemaVersion"])
	}
	if artifact["trustedBaseline"] == nil || artifact["selectorCatalog"] == nil {
		t.Fatalf("trusted baseline and selector catalog were not retained: %#v", artifact)
	}
	trustedBaseline, _ := artifact["trustedBaseline"].(map[string]any)
	if trustedBaseline["id"] != baseline.ID || trustedBaseline["entry"] != baseline.Entry {
		t.Fatalf("trusted baseline payload did not round-trip its identity: %#v", trustedBaseline)
	}
	selectorCatalogPayload, _ := artifact["selectorCatalog"].(map[string]any)
	if selectorCatalogPayload["catalogHash"] != attempts[0].SelectorCatalogHash {
		t.Fatalf("selector catalog payload does not bind its catalog hash: payload=%#v report=%s",
			selectorCatalogPayload["catalogHash"], attempts[0].SelectorCatalogHash)
	}
	validation, _ := json.Marshal(artifact["validation"])
	for _, phase := range []string{"provider-output-parse", "selector-candidate-resolution", "schema-and-contract-validation", "security-scan", "selector-evidence", "serialization"} {
		if !strings.Contains(string(validation), phase) {
			t.Fatalf("validation report omitted %q: %s", phase, validation)
		}
	}
	call, err := manager.GetProviderCall(ctx, job.ID, 1, detail.Calls[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(call.Artifact)
	if !strings.Contains(string(content), `"content"`) || !strings.Contains(string(content), `sendResult`) {
		t.Fatalf("provider body was not replayable: %s", content)
	}
}

func TestDSLManagerRetainsTrustedCatalogAndOpaqueIROnStaleProviderHash(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	completer := &dslBoundaryCompleter{
		content: ruleEnvelope(t, generated), overrideCatalogHash: "stale-provider-catalog",
	}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop())
	_, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Report.Outcome != models.LLMAttemptFailed ||
		detail.Report.ValidationPhase != "selector-candidate-resolution" ||
		detail.Report.SelectorCatalogHash == "" ||
		detail.Report.SelectorCatalogHash != completer.preparedCatalogHash ||
		detail.Report.SelectorCatalogHash == completer.overrideCatalogHash {
		t.Fatalf("attempt did not retain the exact trusted prepared catalog: report=%+v prepared=%q", detail.Report, completer.preparedCatalogHash)
	}
	if len(detail.Calls) != 2 {
		t.Fatalf("unexpected provider lineage: %+v", detail.Calls)
	}
	artifact, _ := detail.Artifact.(map[string]any)
	providerIR, _ := artifact["providerIR"].(map[string]any)
	steps, _ := providerIR["steps"].([]any)
	step, _ := steps[0].(map[string]any)
	target, _ := step["target"].(map[string]any)
	if artifact["providerIRSourceCallId"] != detail.Calls[1].ID ||
		artifact["resolvedRule"] != nil ||
		target["targetCandidateId"] == nil ||
		target["selector"] != nil {
		t.Fatalf("stale-hash failure lost exact opaque provider lineage: artifact=%#v", artifact)
	}
}

func TestDSLManagerCaptureSinkFailureCannotUseManualDegradedFallback(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	if !cfg.AllowDegradedFallback() {
		t.Fatal("test requires the manual degraded fallback branch to be enabled")
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.DB().Exec(`
		CREATE TRIGGER fail_dsl_provider_call_capture
		BEFORE INSERT ON llm_provider_calls
		BEGIN
			SELECT RAISE(ABORT, 'capture storage unavailable');
		END
	`); err != nil {
		t.Fatal(err)
	}

	manager.ProcessOnce(context.Background())

	failedJob, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedJob.Status != models.DSLJobFailed ||
		failedJob.ErrorCode != "ARTIFACT_CAPTURE_FAILED" ||
		failedJob.Provider == "deterministic" {
		t.Fatalf("capture failure used or was classified as degraded fallback: %+v", failedJob)
	}
	failedWorkflow, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedWorkflow.Status != models.DSLWorkflowFailed ||
		failedWorkflow.ProvisionalRule != nil ||
		failedWorkflow.ProvisionalHash != "" {
		t.Fatalf("uncaptured provider output reached the workflow: %+v", failedWorkflow)
	}
	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Report.ErrorCode != "ARTIFACT_CAPTURE_FAILED" ||
		detail.Report.Outcome != models.LLMAttemptFailed ||
		detail.Report.Replayable ||
		len(detail.Calls) != 0 {
		t.Fatalf("capture failure attempt report was unsafe: report=%+v calls=%+v", detail.Report, detail.Calls)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("capture failure unexpectedly invoked another generation path: requests=%d", len(fake.requests))
	}
}

func TestDSLManagerCapturesParseFailureAndAllowsNoProvisionalCorrection(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(`{"rule":`), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failed, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.DSLWorkflowFailed || failed.ProvisionalRule != nil {
		t.Fatalf("expected failed workflow without provisional: %+v", failed)
	}
	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Report.Outcome != models.LLMAttemptFailed ||
		detail.Report.ValidationPhase != "provider-output-parse" ||
		len(detail.Calls) != 1 || !detail.Calls[0].Replayable {
		t.Fatalf("parse failure was not durably replayable: %+v calls=%+v", detail.Report, detail.Calls)
	}
	artifact, _ := detail.Artifact.(map[string]any)
	validationJSON, _ := json.Marshal(artifact["validation"])
	if !strings.Contains(string(validationJSON), `"code":"INVALID_DSL"`) ||
		!strings.Contains(string(validationJSON), `"phase":"provider-output-parse"`) {
		t.Fatalf("parse failure omitted precise validation diagnostics: %s", validationJSON)
	}

	identityChange := dslManagerGeneratedRule(t, baseline)
	identityChange.ID = "provider-ir-must-not-be-trusted"
	if _, err := manager.Correct(ctx, workflow.ID, identityChange); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("no-provisional correction did not preserve trusted generation identity: %v", err)
	}
	stillFailed, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || stillFailed.Status != models.DSLWorkflowFailed || stillFailed.ProvisionalRule != nil {
		t.Fatalf("rejected no-provisional correction mutated workflow: workflow=%+v err=%v", stillFailed, err)
	}

	corrected := dslManagerGeneratedRule(t, baseline)
	reopened, err := manager.Correct(ctx, workflow.ID, corrected)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != models.DSLWorkflowAwaitingReplay ||
		reopened.ProvisionalRule == nil || reopened.ProvisionalHash == "" {
		t.Fatalf("no-provisional correction did not enter normal replay path: %+v", reopened)
	}
}

func TestDSLManagerCapturesCacheHitAsNonPhysicalCall(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		result := completion(ruleEnvelope(t, generated))
		result.CacheHit = true
		return result, nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Calls) != 1 ||
		detail.Calls[0].CallKind != models.LLMProviderCallKindCacheHit ||
		detail.Calls[0].ProviderAttempt != 0 || !detail.Calls[0].CacheHit {
		t.Fatalf("cache lineage was misclassified as a paid call: %+v", detail.Calls)
	}
	artifact, _ := detail.Artifact.(map[string]any)
	if artifact["providerIRSourceCallId"] != detail.Calls[0].ID {
		t.Fatalf("cache-backed provider IR source was not explicit: %#v", artifact)
	}
}

func TestProviderIRSourceCallRequiresLastTerminalResponse(t *testing.T) {
	valid := &models.LLMProviderCall{
		ID: "final-response", CallIndex: 2, Phase: llm.CompletionPhaseSynthesis,
		CallKind: models.LLMProviderCallKindResponse,
	}
	calls := []*models.LLMProviderCall{
		{ID: "analysis", CallIndex: 1, Phase: llm.CompletionPhaseAnalysis, CallKind: models.LLMProviderCallKindResponse},
		valid,
	}
	if got, err := providerIRSourceCallID(calls, map[string]any{"rule": true}); err != nil || got != valid.ID {
		t.Fatalf("valid provider IR source was rejected: id=%q err=%v", got, err)
	}
	selectorRepair := []*models.LLMProviderCall{{
		ID: "selector-repair", CallIndex: 1, Phase: llm.CompletionPhaseSelectorRepair,
		CallKind: models.LLMProviderCallKindResponse,
	}}
	if got, err := providerIRSourceCallID(selectorRepair, map[string]any{"patch": true}); err != nil ||
		got != selectorRepair[0].ID {
		t.Fatalf("valid selector repair source was rejected: id=%q err=%v", got, err)
	}
	if got, err := providerIRSourceCallID(calls, nil); err != nil || got != "" {
		t.Fatalf("nil provider IR should have no source: id=%q err=%v", got, err)
	}
	for name, invalid := range map[string][]*models.LLMProviderCall{
		"not-last-index": {
			{ID: "wrong-index", CallIndex: 1, Phase: llm.CompletionPhaseFinal, CallKind: models.LLMProviderCallKindResponse},
			{ID: "analysis", CallIndex: 2, Phase: llm.CompletionPhaseAnalysis, CallKind: models.LLMProviderCallKindResponse},
		},
		"provider-error": {
			{ID: "error", CallIndex: 1, Phase: llm.CompletionPhaseFinal, CallKind: models.LLMProviderCallKindError},
		},
		"analysis": {
			{ID: "analysis", CallIndex: 1, Phase: llm.CompletionPhaseAnalysis, CallKind: models.LLMProviderCallKindResponse},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if id, err := providerIRSourceCallID(invalid, map[string]any{"rule": true}); err == nil || id != "" {
				t.Fatalf("invalid source was accepted: id=%q err=%v calls=%+v", id, err, invalid)
			}
		})
	}
}

func TestDSLSemanticJSONHashMatchesTypedAndDecodedIR(t *testing.T) {
	rule := &models.Rule{
		ID: "rule", Version: "1", Name: "Rule",
		Domain: models.JSON(`"example.test"`),
		Steps:  models.JSON(`[{"action":"sendResult","payload":{"name":"value"}}]`),
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	typedHash := dslSemanticJSONHash(rule)
	if typedHash == "" || typedHash != dslSemanticJSONHash(decoded) {
		t.Fatalf("semantic hash changed across typed/decoded IR: typed=%s decoded=%s",
			typedHash, dslSemanticJSONHash(decoded))
	}
	decoded["name"] = "Tampered"
	if typedHash == dslSemanticJSONHash(decoded) {
		t.Fatal("semantic hash did not detect provider IR tampering")
	}
}

func TestDSLCompletionCaptureSurvivesRequestCancellation(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	manager := NewDSLManager(
		persistence,
		cfg,
		NewDSLWorkflow(cfg, &fakeWorkflowCompleter{}),
		zap.NewNop(),
	)
	_, pending, err := manager.Submit(ctx, requirement.ID, "profile", dslWorkflowBaseline())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := persistence.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil || claimed.ID != pending.ID {
		t.Fatalf("claim dsl job: job=%+v err=%v", claimed, err)
	}
	recorder := newDSLAttemptRecorder(persistence, claimed, "recording-hash", requirement.ContentHash, "baseline-hash", "catalog-hash")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	request := llm.CompletionRequest{Model: "fake-model", User: "bounded request", JSONMode: true}
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(`{"rule":{}}`), nil
	}}
	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(cancelled, recorder),
		fake,
		request,
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if err != nil || result == nil {
		t.Fatalf("cancelled context lost terminal provider response: result=%+v err=%v", result, err)
	}
	calls, err := persistence.ListLLMProviderCalls(ctx, models.LLMJobTypeDSL, claimed.ID, claimed.AttemptCount)
	if err != nil || len(calls) != 1 || calls[0].CallKind != models.LLMProviderCallKindResponse {
		t.Fatalf("cancelled capture was not persisted: calls=%+v err=%v", calls, err)
	}
}

const testSelectorCatalogHash = "dGVzdC1jYXRhbG9nLWhhc2gtYmFzZTY0dXJsLXZhbHVl"

func newBuildReportRecorder() *dslAttemptRecorder {
	return newDSLAttemptRecorder(nil, &models.DSLJob{
		ID: "job-1", AttemptCount: 1, PromptVersion: "dsl-workflow-test",
	}, "recording-hash", "requirement-hash", "", "")
}

func TestDSLAttemptArtifactRetainsCompleteBaselineAndCatalog(t *testing.T) {
	baseline := dslWorkflowBaseline()
	recorder := newBuildReportRecorder()
	recorder.baseline = baseline
	recorder.baselineHash = dslJSONHash(baseline)
	recorder.selectorCatalogPrompt = `{"version":"selector-catalog-v1","catalogHash":"` + testSelectorCatalogHash + `","candidates":[]}`
	recorder.selectorCatalogHash = testSelectorCatalogHash

	report, artifact, err := recorder.buildReport(nil, nil,
		[]DSLAttemptValidation{{Phase: "complete", Status: "passed"}}, nil)
	if err != nil {
		t.Fatalf("buildReport returned error: %v", err)
	}
	if report.BaselineHash != recorder.baselineHash {
		t.Fatalf("baseline hash identity was not preserved on the report: %q vs %q",
			report.BaselineHash, recorder.baselineHash)
	}
	if report.SelectorCatalogHash != testSelectorCatalogHash {
		t.Fatalf("selector catalog hash identity was not preserved on the report: %q",
			report.SelectorCatalogHash)
	}
	complete, ok := artifact.(DSLAttemptArtifact)
	if !ok {
		t.Fatalf("expected complete dsl attempt artifact, got %T", artifact)
	}
	if complete.SchemaVersion != "aegiscrawler.dsl-attempt.v2" {
		t.Fatalf("dsl attempt artifact schema version was not bumped: %q", complete.SchemaVersion)
	}
	if complete.TrustedBaseline == nil ||
		complete.TrustedBaseline.ID != baseline.ID ||
		complete.TrustedBaseline.Entry != baseline.Entry {
		t.Fatalf("trusted baseline payload was not retained verbatim: %#v", complete.TrustedBaseline)
	}
	catalog, ok := complete.SelectorCatalog.(map[string]any)
	if !ok || catalog["catalogHash"] != testSelectorCatalogHash {
		t.Fatalf("selector catalog payload was not retained with its catalog hash binding: %#v",
			complete.SelectorCatalog)
	}
}

func TestDSLAttemptArtifactRejectsMismatchedCatalogHashBinding(t *testing.T) {
	baseline := dslWorkflowBaseline()
	recorder := newBuildReportRecorder()
	recorder.baseline = baseline
	recorder.baselineHash = dslJSONHash(baseline)
	recorder.selectorCatalogPrompt = `{"version":"selector-catalog-v1","catalogHash":"wrong-catalog-hash","candidates":[]}`
	recorder.selectorCatalogHash = testSelectorCatalogHash

	report, _, err := recorder.buildReport(nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("mismatched catalog hash binding was accepted")
	}
	if report.Outcome != models.LLMAttemptFailed || report.Replayable || report.ErrorCode == "" {
		t.Fatalf("mismatched catalog hash binding did not fail closed: %+v", report)
	}
}

func TestDSLAttemptArtifactFallbackOmitsBaselineAndCatalog(t *testing.T) {
	baseline := dslWorkflowBaseline()
	recorder := newBuildReportRecorder()
	recorder.baseline = baseline
	recorder.baselineHash = dslJSONHash(baseline)
	// Exceed store.MaxLLMAttemptReportArtifactBytes (4 MiB) so the complete
	// artifact overflows and buildReport must return the bounded fallback.
	huge := strings.Repeat("a", (4<<20)+(1<<20))
	recorder.selectorCatalogPrompt = `{"version":"selector-catalog-v1","catalogHash":"` +
		testSelectorCatalogHash + `","candidates":[{"payload":"` + huge + `"}]}`
	recorder.selectorCatalogHash = testSelectorCatalogHash

	report, artifact, err := recorder.buildReport(nil, nil, nil, nil)
	if err == nil {
		t.Fatalf("oversized artifact did not trigger the bounded fallback")
	}
	fallback, ok := artifact.(dslAttemptFallbackArtifact)
	if !ok {
		t.Fatalf("expected bounded fallback artifact, got %T", artifact)
	}
	if fallback.SelectorCatalog["omitted"] != true ||
		fallback.SelectorCatalog["hash"] != testSelectorCatalogHash {
		t.Fatalf("fallback did not omit the selector catalog payload: %#v", fallback.SelectorCatalog)
	}
	if fallback.TrustedBaseline["omitted"] != true ||
		fallback.TrustedBaseline["hash"] != recorder.baselineHash {
		t.Fatalf("fallback did not omit the trusted baseline payload: %#v", fallback.TrustedBaseline)
	}
	if report.Outcome != models.LLMAttemptFailed ||
		report.ErrorCode != "ARTIFACT_CAPTURE_FAILED" || report.Replayable {
		t.Fatalf("oversized artifact report was unsafe: %+v", report)
	}
}

func TestFailedDSLAttemptValidationExportsStructuredOutputFeedback(t *testing.T) {
	feedback := "strict structured output arguments do not match schema: " +
		"path=root -> /properties/rule/properties/steps; keyword=required; " +
		"object shapes: $ keys=[rule selectorCatalogHash]; structural fragment: " +
		`{"rule":{"steps":[{"action":"<string>"}]}}`
	validation := failedDSLAttemptValidation("provider-output", fmt.Errorf(
		"synthesize chunks: %w",
		structuredOutputTestError{code: "structured_output_invalid_arguments", feedback: feedback},
	))
	encoded, err := json.Marshal(validation)
	if err != nil {
		t.Fatal(err)
	}
	artifact := string(encoded)
	for _, expected := range []string{
		`"phase":"provider-output"`,
		`"code":"INVALID_DSL"`,
		"/properties/rule/properties/steps",
		"structural fragment:",
	} {
		if !strings.Contains(artifact, expected) {
			t.Fatalf("exported validation omitted %q: %s", expected, artifact)
		}
	}
}
