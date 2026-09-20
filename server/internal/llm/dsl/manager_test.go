package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	platformrule "github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestClassifyDSLDispatchErrorsAsTerminalStableCodes(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{&budget.DeniedError{Scope: budget.ScopeWorkspace, Limit: budget.LimitRequest}, budget.CodeBudgetExceeded},
		{budget.ErrLedgerUnavailable, budget.CodeLedgerUnavailable},
		{&llm.RouteUnavailableError{Route: "primary", Reason: "usage_contract_violation"}, llm.CodeProviderUnavailable},
	}
	for _, tc := range tests {
		wrapped := workflowCompletionError("generate", tc.err)
		terminal, code, _ := classifyDSLJobError(wrapped)
		if !terminal || code != tc.code {
			t.Fatalf("classify(%v) = terminal %v code %q, want true %q", tc.err, terminal, code, tc.code)
		}
	}
}

// TestClassifyWorkflowBudgetExceededIsTerminalAndStable ensures the per-workflow
// envelope denial surfaces a distinct code so operators can tell envelope
// exhaustion from the global/workspace cap.
func TestClassifyWorkflowBudgetExceededIsTerminalAndStable(t *testing.T) {
	denied := &budget.DeniedError{
		Scope: "workflow", Limit: "envelope",
		Requested: 750, Remaining: 0,
		Reason: "workflow budget exhausted",
		Code:   budget.CodeWorkflowBudgetExceeded,
	}
	terminal, code, message := classifyDSLJobError(workflowCompletionError("repair", denied))
	if !terminal {
		t.Fatalf("expected terminal classification for workflow budget denial")
	}
	if code != budget.CodeWorkflowBudgetExceeded {
		t.Fatalf("expected code %q, got %q", budget.CodeWorkflowBudgetExceeded, code)
	}
	if !strings.Contains(message, "workflow budget") {
		t.Fatalf("expected message to mention workflow budget, got %q", message)
	}
	// Sanity: CodeString round-trips the override.
	if got := denied.CodeString(); got != budget.CodeWorkflowBudgetExceeded {
		t.Fatalf("CodeString = %q, want %q", got, budget.CodeWorkflowBudgetExceeded)
	}
	// And an empty Code still falls back to the historical default for callers
	// that built DeniedError before the override existed.
	legacy := &budget.DeniedError{Scope: budget.ScopeGlobal, Limit: budget.LimitDaily}
	if got := legacy.CodeString(); got != budget.CodeBudgetExceeded {
		t.Fatalf("legacy CodeString = %q, want %q", got, budget.CodeBudgetExceeded)
	}
}

func TestValidateBaselineDomainsAgainstRecording(t *testing.T) {
	recording := &models.Recording{Payload: map[string]any{
		"meta": map[string]any{"startUrl": "https://www.bing.com/"},
		"events": []any{
			map[string]any{"type": "navigate", "url": "https://science.nasa.gov/eclipses"},
			map[string]any{"type": "navigate", "url": "http://insecure.example/path"},
			map[string]any{"type": "navigate", "url": "http://127.0.0.1:43123/fixture"},
		},
		"snapshots": []any{map[string]any{"url": "https://www.bing.com/search?q=eclipse"}},
	}}
	rule := dslWorkflowBaseline()
	rule.Domain = models.JSON(`["www.bing.com","science.nasa.gov"]`)
	if err := validateBaselineDomainsAgainstRecording(rule, recording); err != nil {
		t.Fatalf("expected exact recorded HTTPS domains to pass: %v", err)
	}
	rule.Domain = models.JSON(`"127.0.0.1"`)
	if err := validateBaselineDomainsAgainstRecording(rule, recording); err != nil {
		t.Fatalf("expected exact recorded loopback HTTP domain to pass: %v", err)
	}

	for name, domain := range map[string]string{
		"unrecorded":        `"evil.example"`,
		"parent broadening": `"nasa.gov"`,
		"insecure evidence": `"insecure.example"`,
	} {
		t.Run(name, func(t *testing.T) {
			candidate := dslWorkflowBaseline()
			candidate.Domain = models.JSON(domain)
			if err := validateBaselineDomainsAgainstRecording(candidate, recording); err == nil {
				t.Fatal("expected baseline domain evidence rejection")
			}
		})
	}
}

func TestDSLManagerSubmitRejectsUnrecordedBaselineDomain(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		RecordingMaxActions: 500, RecordingMaxDuration: time.Hour,
		RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, nil), zap.NewNop())
	baseline := dslWorkflowBaseline()
	baseline.Domain = models.JSON(`["example.com","evil.example"]`)

	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if !errors.Is(err, ErrInvalidWorkflowInput) || workflow != nil || job != nil ||
		!strings.Contains(err.Error(), "not evidenced by an exact persisted HTTPS recording URL") {
		t.Fatalf("unrecorded client domain was not rejected before workflow creation: workflow=%+v job=%+v err=%v", workflow, job, err)
	}
	if phase := WorkflowInputPhase(err); phase != "recording-domain-evidence" {
		t.Fatalf("unexpected safe workflow-input phase %q", phase)
	}
}

func TestWorkflowInputPhaseDoesNotExposeRawErrors(t *testing.T) {
	err := workflowInputError("baseline-security-scan", errors.New("secret URL and selector"))
	if phase := WorkflowInputPhase(err); phase != "baseline-security-scan" || strings.Contains(phase, "secret") {
		t.Fatalf("unsafe workflow-input phase %q", phase)
	}
	if phase := WorkflowInputPhase(fmt.Errorf("%w: raw detail", ErrInvalidWorkflowInput)); phase != "workflow-input" {
		t.Fatalf("untyped error exposed unexpected phase %q", phase)
	}
}

func TestWorkflowBaselineValidationPhaseIsBoundedAndSafe(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("steps[4]: action %q does not match the DSL schema: private selector", "click"), "baseline-action-schema"},
		{fmt.Errorf("%w: steps[4] navigates outside approved domains", platformrule.ErrUnsafeProvisionalRule), "baseline-navigation-policy"},
		{fmt.Errorf("%w: required fields are missing: steps", platformrule.ErrInvalidProvisionalRule), "baseline-required-fields"},
		{fmt.Errorf("%w: entry is outside approved domains", platformrule.ErrUnsafeProvisionalRule), "baseline-navigation-policy"},
		{fmt.Errorf("%w: unknown private detail", platformrule.ErrInvalidProvisionalRule), "baseline-structure"},
	}
	for _, test := range tests {
		if got := workflowBaselineValidationPhase(test.err); got != test.want || strings.Contains(got, "private") {
			t.Fatalf("workflowBaselineValidationPhase(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func newDSLManagerStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	persistence, err := store.New(filepath.Join(t.TempDir(), "dsl-manager.db"), "dsl-manager-test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "alice", WorkspaceID: authz.DefaultWorkspaceID,
		Roles: []authz.Role{authz.RoleAdmin}, Kind: authz.PrincipalAdmin,
	})
	return persistence, ctx
}

func createDSLManagerRequirement(t *testing.T, persistence *store.Store, ctx context.Context, cfg *config.Config, source models.RequirementSource) *models.CollectionRequirement {
	t.Helper()
	return createDSLManagerRequirementWithSpec(t, persistence, ctx, cfg, source, dslWorkflowRequirement())
}

func createDSLManagerRequirementWithSpec(
	t *testing.T,
	persistence *store.Store,
	ctx context.Context,
	cfg *config.Config,
	source models.RequirementSource,
	spec models.CollectionRequirementSpec,
) *models.CollectionRequirement {
	t.Helper()
	recordingService := platformrecording.NewService(persistence, cfg)
	now := time.Now().UTC()
	recording, err := recordingService.Create(ctx, platformrecording.CreateInput{
		StartedAt: now.Add(-time.Minute), EndedAt: now,
		Payload: map[string]any{
			"version": "2",
			"meta":    map[string]any{"sanitizationVersion": "extension-v2"},
			"events":  []any{map[string]any{"type": "click", "target": map[string]any{"selector": ".product"}}},
			"snapshots": []any{
				map[string]any{"phase": "initial", "sequence": 0, "actionIndex": 0, "url": "https://example.com/products", "capture": map[string]any{"status": "complete"}, "domTree": dslManagerSemanticDOM()},
				map[string]any{"phase": "final", "sequence": 1, "actionIndex": 1, "url": "https://example.com/products", "capture": map[string]any{"status": "complete"}, "domTree": dslManagerSemanticDOM()},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement := &models.CollectionRequirement{
		ID: "requirement-" + string(source), RecordingID: recording.ID, Source: source,
		Requirement: spec, Owner: "alice",
	}
	if err := persistence.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	confirmed, err := persistence.ConfirmCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	return confirmed
}

func TestDSLManagerProcessOnceLeavesPendingJobsUnclaimedWhenLLMDisabled(t *testing.T) {
	for _, mode := range []string{"legacy", "enforced"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LLM_ENABLED", "false")
			t.Setenv("LLM_POLICY_MODE", mode)
			cfg := config.Load()
			persistence, ctx := newDSLManagerStore(t)
			requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
			manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, nil), zap.NewNop())
			workflow, job, err := manager.Submit(ctx, requirement.ID, "disabled-profile", dslWorkflowBaseline())
			if err != nil {
				t.Fatal(err)
			}

			manager.ProcessOnce(context.Background())

			pending, err := manager.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			current, err := manager.GetWorkflow(ctx, workflow.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending.Status != models.DSLJobPending || pending.AttemptCount != 0 {
				t.Fatalf("disabled worker mutated pending job: %+v", pending)
			}
			if current.Status != models.DSLWorkflowGenerating {
				t.Fatalf("disabled worker mutated workflow: %+v", current)
			}
		})
	}
}

func TestDSLManagerEnforcedFailureLogHashesProviderValidationError(t *testing.T) {
	const sentinel = "dsl-provider-selector-secret-sentinel"

	cfg := loadEnforcedGeneratorConfig(t)
	persistence, ctx := newDSLManagerStore(t)
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	core, observed := observer.New(zap.WarnLevel)
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, nil), zap.New(core))
	_, job, err := manager.Submit(ctx, requirement.ID, "enforced-profile", dslWorkflowBaseline())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := persistence.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("claimed job %q, want %q", claimed.ID, job.ID)
	}
	providerErr := fmt.Errorf("%w: selector %s", ErrInvalidLLMRule, sentinel)

	manager.failJob(context.Background(), claimed, providerErr)

	entries := observed.FilterMessage("dsl workflow job failed").All()
	if len(entries) != 1 {
		t.Fatalf("failure logs = %d, want 1", len(entries))
	}
	entry := entries[0]
	for key, value := range entry.ContextMap() {
		if strings.Contains(fmt.Sprint(value), sentinel) {
			t.Fatalf("log field %q exposed provider validation content: %v", key, value)
		}
	}
	fields := entry.ContextMap()
	if fields["code"] != "INVALID_DSL" || fields["errorClass"] != "INVALID_DSL" {
		t.Fatalf("unexpected stable failure fields: %+v", fields)
	}
	if fields["errorHash"] == "" || fields["errorBytes"] != int64(len(providerErr.Error())) {
		t.Fatalf("missing hashed error evidence: %+v", fields)
	}
	if _, ok := fields["error"]; ok {
		t.Fatal("enforced failure log must not contain a raw error field")
	}
}

func dslManagerSemanticDOM() map[string]any {
	card := func(classes string) map[string]any {
		return map[string]any{
			"type": "element", "tagName": "article",
			"attributes": []any{map[string]any{"name": "class", "value": classes}},
			"children": []any{map[string]any{
				"type": "element", "tagName": "h2",
				"attributes": []any{map[string]any{"name": "class", "value": "card-title"}},
				"children":   []any{map[string]any{"type": "text", "text": "Result title"}},
			}},
		}
	}
	name := map[string]any{
		"type": "element", "tagName": "span",
		"attributes": []any{map[string]any{"name": "class", "value": "name human-corrected-name"}},
		"children":   []any{map[string]any{"type": "text", "text": "Example"}},
	}
	results := map[string]any{
		"type": "element", "tagName": "main",
		"attributes": []any{map[string]any{"name": "id", "value": "results"}},
		"children":   []any{card("card featured"), card("card")},
	}
	decoys := map[string]any{
		"type": "element", "tagName": "aside",
		"attributes": []any{map[string]any{"name": "id", "value": "decoys"}},
		"children": []any{
			card("decoy"), card("decoy"),
		},
	}
	return map[string]any{
		"type": "element", "tagName": "html", "children": []any{
			map[string]any{"type": "element", "tagName": "body", "children": []any{name, results, decoys}},
		},
	}
}

func dslManagerGeneratedRule(t *testing.T, baseline *models.Rule) *models.Rule {
	t.Helper()
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var generated models.Rule
	if err := json.Unmarshal(encoded, &generated); err != nil {
		t.Fatal(err)
	}
	generated.Steps = models.JSON(`[
		{"action":"extractText","name":"name","target":{"selector":".name"}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}"},"immediate":true}
	]`)
	return &generated
}

func generatedAttemptExport(t *testing.T, manager *Manager, jobID string) AdminReviewedDSLAttemptExport {
	t.Helper()
	detail, err := manager.GetProviderAttempt(context.Background(), jobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	var artifact DSLAttemptArtifact
	if err := json.Unmarshal(encoded, &artifact); err != nil {
		t.Fatal(err)
	}
	return AdminReviewedDSLAttemptExport{
		SchemaVersion: AdminReviewedDSLAttemptExportVersion,
		Attempt:       detail.Report, Artifact: &artifact,
	}
}

func cloneGeneratedAttemptExport(t *testing.T, source AdminReviewedDSLAttemptExport) AdminReviewedDSLAttemptExport {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var cloned AdminReviewedDSLAttemptExport
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func resealAttemptExport(t *testing.T, exported *AdminReviewedDSLAttemptExport) {
	t.Helper()
	exported.Attempt.BaselineHash = dslJSONHash(exported.Artifact.TrustedBaseline)
	exported.Attempt.ArtifactHash = dslJSONHash(exported.Artifact)
}

func TestDSLManagerAdoptsReviewedAttemptWithoutProviderAndRequiresReplayApproval(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, dslManagerGeneratedRule(t, baseline))), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, sourceJob, err := manager.Submit(ctx, requirement.ID, "source-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	exported := generatedAttemptExport(t, manager, sourceJob.ID)
	providerCalls := len(fake.requests)

	adopted, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "reviewed-profile", exported)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != providerCalls {
		t.Fatalf("attempt adoption made a provider request: before=%d after=%d", providerCalls, len(fake.requests))
	}
	if adopted.Status != models.DSLWorkflowAwaitingReplay || adopted.MaxRepairs != 0 ||
		adopted.CurrentJobID != "" || adopted.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		adopted.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		adopted.SourceArtifactHash != exported.Attempt.ArtifactHash || len(adopted.SourceExportHash) != 64 {
		t.Fatalf("unexpected adopted workflow: %+v", adopted)
	}
	retried, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "reviewed-profile", exported)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != adopted.ID || retried.Status != models.DSLWorkflowAwaitingReplay {
		t.Fatalf("identical adoption retry was not idempotent: first=%+v retry=%+v", adopted, retried)
	}
	var adoptionAudits int
	if err := persistence.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs
		WHERE action = 'dsl_workflow_adopted_from_attempt_export'
		  AND resource_id = ?
	`, adopted.ID).Scan(&adoptionAudits); err != nil {
		t.Fatal(err)
	}
	if adoptionAudits != 1 {
		t.Fatalf("identical adoption retry created %d adoption audits", adoptionAudits)
	}
	if _, _, err := manager.Confirm(ctx, adopted.ID, store.ApproveDSLWorkflowOptions{}); !errors.Is(err, store.ErrDSLWorkflowState) {
		t.Fatalf("adopted rule was approvable before replay: %v", err)
	}
	replay, err := manager.StartReplay(ctx, adopted.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, repair, err := manager.CompleteReplay(ctx, adopted.ID, replay.ID, ReplayCompletionInput{
		Succeeded: true, Output: map[string]any{"name": "Example"},
	})
	if err != nil || completed.Status != models.ReplayAttemptSucceeded || repair != nil {
		t.Fatalf("adopted replay failed: replay=%+v repair=%+v err=%v", completed, repair, err)
	}
	version, contract, err := manager.Confirm(ctx, adopted.ID, store.ApproveDSLWorkflowOptions{})
	if err != nil || version.Status != models.RuleApprovalApproved || contract == nil {
		t.Fatalf("adopted replay was not explicitly approved: version=%+v contract=%+v err=%v",
			version, contract, err)
	}
	if version.Source != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		contract.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		contract.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		contract.SourceArtifactHash != exported.Attempt.ArtifactHash ||
		contract.SourceExportHash != adopted.SourceExportHash || contract.SourceWorkflowID != adopted.ID {
		t.Fatalf("approved version lost imported source lineage: version=%+v contract=%+v err=%v", version, contract, err)
	}
	var jobs int
	if err := persistence.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, adopted.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("adopted workflow created %d DSL jobs", jobs)
	}
}

func TestDSLManagerAdoptsHistoricalBaselineMissingRequiredInputVariable(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	spec := dslWorkflowRequirement()
	spec.RequiredInputs = []models.RequirementInput{{Name: "tag", Type: models.RequirementValueString}}
	requirement := createDSLManagerRequirementWithSpec(
		t, persistence, ctx, cfg, models.RequirementSourceLLM, spec,
	)
	baseline := dslWorkflowBaseline()
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		generated := dslManagerGeneratedRule(t, baseline)
		steps := strings.TrimSpace(string(generated.Steps))
		generated.Steps = models.JSON(strings.TrimSuffix(steps, "]") +
			`,{"action":"sendLog","level":"info","message":"{{tag}}"}]`)
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, sourceJob, err := manager.Submit(ctx, requirement.ID, "source-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	exported := generatedAttemptExport(t, manager, sourceJob.ID)

	producedVariables, ok := strictRuleVariables(exported.Artifact.TrustedBaseline.Variables)
	if !ok || !reflect.DeepEqual(producedVariables, map[string]any{"tag": ""}) {
		t.Fatalf("new generation did not persist its canonical baseline variables: %#v", producedVariables)
	}
	historical := cloneGeneratedAttemptExport(t, exported)
	historical.Artifact.TrustedBaseline.Variables = models.JSON(`{}`)
	providerIR, ok := historical.Artifact.ProviderIR.(map[string]any)
	if !ok {
		t.Fatalf("provider IR was not an object: %T", historical.Artifact.ProviderIR)
	}
	providerIR["variables"] = map[string]any{}
	resolvedVariables, ok := strictRuleVariables(historical.Artifact.ResolvedRule.Variables)
	if !ok || !reflect.DeepEqual(resolvedVariables, map[string]any{"tag": ""}) {
		t.Fatalf("fixture resolved rule does not contain the historical required input: %#v", resolvedVariables)
	}
	resealAttemptExport(t, &historical)
	if historical.Attempt.BaselineHash != dslJSONHash(historical.Artifact.TrustedBaseline) {
		t.Fatal("historical baseline hash was not bound to the archived baseline")
	}
	encodedHistorical, err := json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	expectedExportHash := dslContentHash(string(encodedHistorical))

	providerCalls := len(fake.requests)
	adopted, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "reviewed-profile", historical)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != providerCalls {
		t.Fatalf("historical adoption made a provider request: before=%d after=%d", providerCalls, len(fake.requests))
	}
	if adopted.Status != models.DSLWorkflowAwaitingReplay || adopted.CurrentJobID != "" ||
		adopted.MaxRepairs != 0 || adopted.RepairCount != 0 ||
		adopted.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		adopted.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		adopted.SourceArtifactHash != historical.Attempt.ArtifactHash ||
		adopted.SourceExportHash != expectedExportHash {
		t.Fatalf("unexpected historical adopted workflow: %+v", adopted)
	}
	var jobs int
	if err := persistence.DB().QueryRowContext(
		ctx, `SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, adopted.ID,
	).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("historical adoption created %d DSL jobs", jobs)
	}

	tests := []struct {
		name   string
		mutate func(*AdminReviewedDSLAttemptExport)
	}{
		{
			name: "unrelated output normalization",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.TrustedBaseline.Output = models.JSON(`{"type":"object"}`)
			},
		},
		{
			name: "wrong resolved variable value",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.ResolvedRule.Variables = models.JSON(`{"tag":"attacker-value"}`)
			},
		},
		{
			name: "missing provider IR with wrong resolved variable value",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.ProviderIR = nil
				value.Artifact.ResolvedRule.Variables = models.JSON(`{"tag":"attacker-value"}`)
			},
		},
		{
			name: "missing provider IR with extra resolved variable",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.ProviderIR = nil
				value.Artifact.ResolvedRule.Variables = models.JSON(`{"tag":"","extra":true}`)
			},
		},
		{
			name: "unrelated added baseline variable",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.TrustedBaseline.Variables = models.JSON(`{"unrelated":true}`)
				providerIR := value.Artifact.ProviderIR.(map[string]any)
				providerIR["variables"] = map[string]any{"unrelated": true}
				value.Artifact.ResolvedRule.Variables = models.JSON(`{"tag":"","unrelated":true}`)
			},
		},
		{
			name: "duplicate baseline variable",
			mutate: func(value *AdminReviewedDSLAttemptExport) {
				value.Artifact.TrustedBaseline.Variables = models.JSON(`{"tag":"","tag":""}`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := cloneGeneratedAttemptExport(t, historical)
			test.mutate(&tampered)
			resealAttemptExport(t, &tampered)
			_, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "tampered-profile", tampered)
			if !errors.Is(err, ErrInvalidWorkflowInput) {
				t.Fatalf("expected fail-closed adoption, got %v", err)
			}
		})
	}
}

func TestDSLManagerAdoptsGeneratedAttemptWithRecordedInputValue(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	spec := dslWorkflowRequirement()
	spec.RequiredInputs = []models.RequirementInput{{Name: "keyword", Type: models.RequirementValueString}}
	requirement := createDSLManagerRequirementWithSpec(
		t, persistence, ctx, cfg, models.RequirementSourceLLM, spec,
	)
	baseline := dslWorkflowBaseline()
	baseline.Variables = models.JSON(`{"keyword":"iphone"}`)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		generated := dslManagerGeneratedRule(t, baseline)
		steps := strings.TrimSpace(string(generated.Steps))
		generated.Steps = models.JSON(strings.TrimSuffix(steps, "]") +
			`,{"action":"sendLog","level":"info","message":"{{keyword}}"}]`)
		generated.Variables = nil
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, sourceJob, err := manager.Submit(ctx, requirement.ID, "source-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	exported := generatedAttemptExport(t, manager, sourceJob.ID)
	trustedVariables, trustedOK := strictRuleVariables(exported.Artifact.TrustedBaseline.Variables)
	resolvedVariables, resolvedOK := strictRuleVariables(exported.Artifact.ResolvedRule.Variables)
	if !trustedOK || !resolvedOK ||
		!reflect.DeepEqual(trustedVariables, map[string]any{"keyword": "iphone"}) ||
		!reflect.DeepEqual(resolvedVariables, map[string]any{"keyword": ""}) {
		t.Fatalf("fixture did not preserve producer variable semantics: trusted=%#v resolved=%#v",
			trustedVariables, resolvedVariables)
	}

	providerCalls := len(fake.requests)
	adopted, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "reviewed-profile", exported)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != providerCalls || adopted.Status != models.DSLWorkflowAwaitingReplay ||
		adopted.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative {
		t.Fatalf("generated attempt with recorded input was not adopted safely: workflow=%+v calls=%d/%d",
			adopted, providerCalls, len(fake.requests))
	}
}

func TestCanonicalAdminReviewedBaselineAddsOnlyValidMissingRequiredInputs(t *testing.T) {
	exported := dslWorkflowBaseline()
	exported.Variables = models.JSON(`{}`)
	requirement := dslWorkflowRequirement()
	requirement.RequiredInputs = []models.RequirementInput{
		{Name: "text", Type: models.RequirementValueString},
		{Name: "count", Type: models.RequirementValueNumber},
		{Name: "enabled", Type: models.RequirementValueBoolean},
		{Name: "filters", Type: models.RequirementValueObject},
		{Name: "items", Type: models.RequirementValueArray},
		{Name: "explicit", Type: models.RequirementValueNumber, Default: 3},
	}
	requirement.OptionalInputs = []models.RequirementInput{
		{Name: "optional", Type: models.RequirementValueString, Default: "must-not-be-added"},
	}

	canonical, normalized, ok := canonicalAdminReviewedBaseline(exported, requirement)
	if !ok || !normalized {
		t.Fatal("valid required inputs were rejected")
	}
	variables, ok := strictRuleVariables(canonical.Variables)
	if !ok {
		t.Fatal("canonical variables were not a strict JSON object")
	}
	expected := map[string]any{
		"text":     "",
		"count":    float64(0),
		"enabled":  false,
		"filters":  map[string]any{},
		"items":    []any{},
		"explicit": float64(3),
	}
	if !reflect.DeepEqual(variables, expected) {
		t.Fatalf("unexpected canonical variables: %#v", variables)
	}
	if original, ok := strictRuleVariables(exported.Variables); !ok || len(original) != 0 {
		t.Fatalf("archived baseline was mutated: %#v", original)
	}
	validated, err := copyRule(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformrule.ValidateWorkflowBaseline(validated, canonical, requirement); err != nil {
		t.Fatal(err)
	}
	if dslSemanticJSONHash(validated) == dslSemanticJSONHash(canonical) {
		t.Fatal("missing optional input did not remain a fail-closed validator mutation")
	}

	tests := []struct {
		name        string
		variables   models.JSON
		requirement models.CollectionRequirementSpec
	}{
		{name: "omitted variables", variables: nil, requirement: requirement},
		{name: "null variables", variables: models.JSON(`null`), requirement: requirement},
		{name: "array variables", variables: models.JSON(`[]`), requirement: requirement},
		{name: "duplicate nested key", variables: models.JSON(`{"existing":{"value":1,"value":2}}`), requirement: requirement},
		{name: "nonempty historical variables", variables: models.JSON(`{"existing":true}`), requirement: requirement},
		{
			name:      "duplicate required input",
			variables: models.JSON(`{}`),
			requirement: models.CollectionRequirementSpec{RequiredInputs: []models.RequirementInput{
				{Name: "tag", Type: models.RequirementValueString},
				{Name: "tag", Type: models.RequirementValueString},
			}},
		},
		{
			name:      "noncanonical input name",
			variables: models.JSON(`{}`),
			requirement: models.CollectionRequirementSpec{RequiredInputs: []models.RequirementInput{
				{Name: " tag ", Type: models.RequirementValueString},
			}},
		},
		{
			name:      "wrong explicit default type",
			variables: models.JSON(`{}`),
			requirement: models.CollectionRequirementSpec{RequiredInputs: []models.RequirementInput{
				{Name: "tag", Type: models.RequirementValueString, Default: 5},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := dslWorkflowBaseline()
			candidate.Variables = test.variables
			if _, _, ok := canonicalAdminReviewedBaseline(candidate, test.requirement); ok {
				t.Fatal("invalid historical variables were canonicalized")
			}
		})
	}
}

func TestDSLManagerRejectsTamperedAttemptAdoption(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, dslManagerGeneratedRule(t, baseline))), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, sourceJob, err := manager.Submit(ctx, requirement.ID, "source-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	valid := generatedAttemptExport(t, manager, sourceJob.ID)

	draft := &models.CollectionRequirement{
		ID: "unconfirmed-import-requirement", RecordingID: requirement.RecordingID,
		Source: models.RequirementSourceManual, Requirement: requirement.Requirement, Owner: "alice",
	}
	if err := persistence.CreateCollectionRequirement(ctx, draft); err != nil {
		t.Fatal(err)
	}
	invalidInputSpec := requirement.Requirement
	invalidInputSpec.RequiredInputs = []models.RequirementInput{{
		Name: "tag", Type: models.RequirementValueString, Default: "x",
		Constraints: map[string]any{"minLength": 2},
	}}
	invalidInputRequirement := &models.CollectionRequirement{
		ID: "invalid-input-contract-requirement", RecordingID: requirement.RecordingID,
		Source: models.RequirementSourceManual, Requirement: invalidInputSpec, Owner: "alice",
	}
	if err := persistence.CreateCollectionRequirement(ctx, invalidInputRequirement); err != nil {
		t.Fatal(err)
	}
	invalidInputRequirement, err = persistence.ConfirmCollectionRequirement(ctx, invalidInputRequirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Submit(
		ctx, invalidInputRequirement.ID, "invalid-contract-profile", baseline,
	); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("submit accepted an invalid confirmed input contract: %v", err)
	}
	otherWorkspace := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "mallory", WorkspaceID: "other-workspace", Roles: []authz.Role{authz.RoleAdmin}, Kind: authz.PrincipalAdmin,
	})

	tests := []struct {
		name          string
		requirementID string
		ctx           context.Context
		mutate        func(*AdminReviewedDSLAttemptExport)
		want          error
	}{
		{name: "recording hash", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) { value.Attempt.RecordingHash = strings.Repeat("0", 64) }, want: ErrInvalidWorkflowInput},
		{name: "requirement hash", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) { value.Attempt.RequirementHash = strings.Repeat("1", 64) }, want: ErrInvalidWorkflowInput},
		{name: "baseline", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.TrustedBaseline.Name = "tampered baseline"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "catalog", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			catalog := value.Artifact.SelectorCatalog.(map[string]any)
			catalog["catalogHash"] = "tampered"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "resolved rule drift", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.ResolvedRule.Name = "tampered"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "provider IR drift", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			providerIR := value.Artifact.ProviderIR.(map[string]any)
			providerIR["name"] = "tampered provider IR"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "claimed provider source", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.ProviderIR = nil
			value.Artifact.ResolvedRule.Source = "fresh-canary/provider"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "validator canonicalization", requirementID: requirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.ProviderIR = nil
			var steps []map[string]any
			_ = json.Unmarshal(value.Artifact.ResolvedRule.Steps, &steps)
			delete(steps[0]["target"].(map[string]any), "visible")
			value.Artifact.ResolvedRule.Steps, _ = json.Marshal(steps)
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}, want: ErrInvalidWorkflowInput},
		{name: "unconfirmed requirement", requirementID: draft.ID, ctx: ctx, want: store.ErrRequirementState},
		{name: "invalid confirmed input contract", requirementID: invalidInputRequirement.ID, ctx: ctx, mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Attempt.RequirementHash = invalidInputRequirement.ContentHash
		}, want: ErrInvalidWorkflowInput},
		{name: "cross workspace", requirementID: requirement.ID, ctx: otherWorkspace, want: store.ErrRequirementNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exported := cloneGeneratedAttemptExport(t, valid)
			if test.mutate != nil {
				test.mutate(&exported)
			}
			_, err := manager.AdoptAdminReviewedAttempt(test.ctx, test.requirementID, "reviewed-profile", exported)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestDSLManagerRunsGenerationRepairReplayAndImmutableApproval(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generatedRule := dslManagerGeneratedRule(t, baseline)
	var providerSteps []any
	if err := json.Unmarshal(generatedRule.Steps, &providerSteps); err != nil {
		t.Fatal(err)
	}
	providerSteps = append([]any{map[string]any{
		"action": "click",
		"target": map[string]any{
			"family": "selector", "value": "#results", "name": "",
		},
	}}, providerSteps...)
	generatedRule.Steps, _ = json.Marshal(providerSteps)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "current-chrome-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Status != models.DSLWorkflowGenerating || job.Status != models.DSLJobPending {
		t.Fatalf("unexpected submitted workflow: workflow=%+v job=%+v", workflow, job)
	}
	manager.ProcessOnce(context.Background())
	generated, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Status != models.DSLWorkflowAwaitingReplay || generated.ProvisionalRule == nil || generated.ProvisionalHash == "" {
		t.Fatalf("unexpected generated workflow: %+v", generated)
	}
	var resolvedSteps []any
	if err := json.Unmarshal(generated.ProvisionalRule.Steps, &resolvedSteps); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, _ := resolvedSteps[0].(map[string]any)["target"].(map[string]any)
	if len(resolvedTarget) != 1 || resolvedTarget["selector"] != "#results" {
		t.Fatalf("provider target wire was not lowered before provisional storage: %#v", resolvedTarget)
	}
	completedJob, err := manager.GetJob(ctx, job.ID)
	if err != nil || completedJob.Status != models.DSLJobCompleted || completedJob.Provider != "fake" || completedJob.ResultHash == "" {
		t.Fatalf("unexpected generation job: job=%+v err=%v", completedJob, err)
	}
	if flags := strings.Join(completedJob.SafetyFlags, " "); !strings.Contains(flags, "selector-evidence:passed") ||
		!strings.Contains(flags, "selector-evidence:checked=1") ||
		!strings.Contains(flags, "provider-targets:resolved=1") {
		t.Fatalf("selector evidence audit metadata was not persisted: %v", completedJob.SafetyFlags)
	}
	attempt, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	artifact, _ := attempt.Artifact.(map[string]any)
	rawProviderIR, _ := artifact["providerIR"].(map[string]any)
	rawSteps, _ := rawProviderIR["steps"].([]any)
	rawTarget, _ := rawSteps[0].(map[string]any)["target"].(map[string]any)
	if len(rawTarget) != 3 ||
		rawTarget["family"] != "selector" ||
		rawTarget["value"] != "#results" ||
		rawTarget["name"] != "" {
		t.Fatalf("attempt lineage did not retain the raw provider target wire: %#v", rawTarget)
	}
	if prompt := fake.requests[0].User; !strings.Contains(prompt, "page-text-free selector candidate catalog") ||
		!strings.Contains(prompt, `"observedSelector":"#results \u003e .card"`) {
		t.Fatalf("generation prompt omitted bounded selector evidence: %s", prompt)
	}

	firstReplay, err := manager.StartReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedReplay, repairJob, err := manager.CompleteReplay(ctx, workflow.ID, firstReplay.ID, ReplayCompletionInput{
		Succeeded:   true,
		Diagnostics: map[string]any{"message": "password=hunter2", "authorization": "Bearer secret-token"},
		Output:      map[string]any{"name": 123},
		Artifacts:   []models.ReplayArtifact{{Name: "failure", Type: "text", Data: "token=secret-token"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completedReplay.Status != models.ReplayAttemptFailed || completedReplay.OutputValid || repairJob == nil || repairJob.Kind != models.DSLJobRepair {
		t.Fatalf("schema-invalid replay did not schedule repair: replay=%+v repair=%+v", completedReplay, repairJob)
	}
	storedReplay, err := manager.GetReplay(ctx, firstReplay.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedDiagnostics, _ := json.Marshal(storedReplay.Diagnostics)
	encodedArtifacts, _ := json.Marshal(storedReplay.Artifacts)
	if strings.Contains(string(encodedDiagnostics), "hunter2") || strings.Contains(string(encodedDiagnostics), "secret-token") || strings.Contains(string(encodedArtifacts), "secret-token") {
		t.Fatalf("replay diagnostics were not defensively redacted: diagnostics=%s artifacts=%s", encodedDiagnostics, encodedArtifacts)
	}
	if !strings.Contains(string(encodedDiagnostics), "OUTPUT_SCHEMA_INVALID") && storedReplay.ErrorCode != "OUTPUT_SCHEMA_INVALID" {
		t.Fatalf("schema diagnostic was not retained safely: replay=%+v", storedReplay)
	}

	manager.ProcessOnce(context.Background())
	repairPrompt := fake.requests[len(fake.requests)-1].User
	if !strings.Contains(repairPrompt, "replayArtifacts") || !strings.Contains(repairPrompt, "page-text-free selector candidate catalog") || strings.Contains(repairPrompt, "secret-token") {
		t.Fatalf("repair prompt did not receive bounded redacted replay evidence: %s", repairPrompt)
	}
	repaired, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || repaired.Status != models.DSLWorkflowAwaitingReplay || repaired.RepairCount != 1 {
		t.Fatalf("unexpected repaired workflow: workflow=%+v err=%v", repaired, err)
	}
	repairAttempts, err := manager.ListProviderAttempts(ctx, repairJob.ID)
	if err != nil || len(repairAttempts) != 1 ||
		repairAttempts[0].Outcome != models.LLMAttemptSucceeded ||
		repairAttempts[0].BaselineHash == "" {
		t.Fatalf("repair provider attempt was not retained: attempts=%+v err=%v", repairAttempts, err)
	}
	secondReplay, err := manager.StartReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedReplay, repairJob, err = manager.CompleteReplay(ctx, workflow.ID, secondReplay.ID, ReplayCompletionInput{
		Succeeded: true, Diagnostics: map[string]any{"message": "complete"}, Output: map[string]any{"name": "Example"},
	})
	if err != nil || completedReplay.Status != models.ReplayAttemptSucceeded || !completedReplay.OutputValid || repairJob != nil {
		t.Fatalf("unexpected successful replay: replay=%+v repair=%+v err=%v", completedReplay, repairJob, err)
	}
	version, contract, err := manager.Confirm(ctx, workflow.ID, store.ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != models.RuleApprovalApproved || version.RuleID != baseline.ID ||
		version.RecordingID != requirement.RecordingID || contract == nil ||
		contract.RuleID != version.RuleID || contract.Version != version.Version {
		t.Fatalf("unexpected immutable rule approval: version=%+v contract=%+v", version, contract)
	}
	retriedVersion, retriedContract, err := manager.Confirm(ctx, workflow.ID, store.ApproveDSLWorkflowOptions{})
	if err != nil || retriedVersion.ContentHash != version.ContentHash ||
		retriedVersion.Version != version.Version || retriedContract == nil ||
		retriedContract.Version != contract.Version {
		t.Fatalf("second confirmation changed the immutable approval: version=%+v contract=%+v err=%v",
			retriedVersion, retriedContract, err)
	}
}

func TestDSLManagerRejectsCanonicalOrdinaryProviderTargetWhenStrictOutputIsOff(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 1,
		LLMJobMaxAttempts: 1, LLMRequestTimeout: time.Second,
		RecordingMaxActions: 500, RecordingMaxDuration: time.Hour,
		RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(
		t, persistence, ctx, cfg, models.RequirementSourceLLM,
	)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	var steps []any
	if err := json.Unmarshal(generated.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	steps = append([]any{map[string]any{
		"action": "click",
		"target": map[string]any{"selector": "#results"},
	}}, steps...)
	generated.Steps, _ = json.Marshal(steps)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}

	manager.ProcessOnce(context.Background())

	failedJob, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedJob.Status != models.DSLJobFailed ||
		failedJob.ErrorCode != "INVALID_DSL" ||
		!strings.Contains(strings.Join(failedJob.SafetyFlags, " "), "selector-candidates:failed") {
		t.Fatalf("canonical current-provider target did not fail closed: %+v", failedJob)
	}
	failedWorkflow, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedWorkflow.Status != models.DSLWorkflowFailed ||
		failedWorkflow.ProvisionalRule != nil ||
		failedWorkflow.ProvisionalHash != "" {
		t.Fatalf("canonical provider target reached provisional state: %+v", failedWorkflow)
	}
}

func TestDSLManagerRejectsWrongCardinalitySelectorBeforeReplay(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 1,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	generated.Steps = models.JSON(`[
		{"action":"extract","name":"rows","target":{"selector":"#results > .featured"},"multiple":true,"fields":{"name":{"type":"text","selector":".card-title"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
			{"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
		]}
	]`)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	pending, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.DSLJobPending || pending.ErrorCode != "INVALID_DSL" ||
		!strings.Contains(pending.ErrorMessage, "exactly one opaque selector candidate ID") {
		t.Fatalf("wrong-cardinality selector did not remain retryable: %+v", pending)
	}
	if flags := strings.Join(pending.SafetyFlags, " "); !strings.Contains(flags, "selector-candidates:failed") ||
		strings.Contains(flags, "security-scan:passed") || strings.Contains(flags, "selector-evidence:failed") {
		t.Fatalf("selector rejection order/audit flags are wrong: %v", pending.SafetyFlags)
	}
	current, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || current.Status != models.DSLWorkflowGenerating || current.LastReplaySequence != 0 {
		t.Fatalf("invalid selector reached replay state: workflow=%+v err=%v", current, err)
	}
	detail, err := manager.GetProviderAttempt(ctx, job.ID, 1)
	if err != nil || detail.Report.ValidationPhase != "selector-candidate-resolution" ||
		detail.Report.Outcome != models.LLMAttemptFailed || len(detail.Calls) != 1 {
		t.Fatalf("selector failure history was incomplete: detail=%+v err=%v", detail, err)
	}
	artifact, _ := detail.Artifact.(map[string]any)
	if artifact["providerIR"] == nil || artifact["resolvedRule"] != nil {
		t.Fatalf("failed selector was incorrectly represented as a resolved replayable rule: %#v", artifact)
	}
}

func TestDSLManagerFailsTerminallyBeforeProviderWhenSelectorEvidenceIsUnavailable(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 1,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	recordingService := platformrecording.NewService(persistence, cfg)
	now := time.Now().UTC()
	recording, err := recordingService.Create(ctx, platformrecording.CreateInput{
		StartedAt: now.Add(-time.Minute), EndedAt: now,
		Payload: map[string]any{
			"version": "2", "meta": map[string]any{"startUrl": "https://example.com/"}, "events": []any{},
			"snapshots": []any{
				map[string]any{"phase": "initial", "url": "https://example.com/", "capture": map[string]any{"status": "failed"}},
				map[string]any{"phase": "final", "url": "https://example.com/", "capture": map[string]any{"status": "failed"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement := &models.CollectionRequirement{
		ID: "requirement-no-selector-evidence", RecordingID: recording.ID,
		Source: models.RequirementSourceLLM, Requirement: dslWorkflowRequirement(), Owner: "alice",
	}
	if err := persistence.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.ConfirmCollectionRequirement(ctx, requirement.ID); err != nil {
		t.Fatal(err)
	}
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		t.Fatal("provider must not run without usable selector evidence")
		return nil, errors.New("unexpected provider call")
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "profile", dslWorkflowBaseline())
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failed, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.DSLJobFailed || failed.ErrorCode != "SOURCE_UNAVAILABLE" || failed.AttemptCount != 1 || len(fake.requests) != 0 {
		t.Fatalf("unusable source evidence was not terminal before provider work: %+v requests=%d", failed, len(fake.requests))
	}
	current, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || current.Status != models.DSLWorkflowFailed || current.LastReplaySequence != 0 {
		t.Fatalf("unusable source evidence reached replay or remained retryable: workflow=%+v err=%v", current, err)
	}
	attempts, err := manager.ListProviderAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 1 || attempts[0].CallCount != 0 ||
		attempts[0].ValidationPhase != "selector-catalog" ||
		attempts[0].Outcome != models.LLMAttemptFailed {
		t.Fatalf("pre-provider failure did not retain its claimed attempt: attempts=%+v err=%v", attempts, err)
	}
}

func TestSelectorCatalogUnavailableIsTerminalBeforeProviderRetry(t *testing.T) {
	terminal, code, message := classifyDSLJobError(fmt.Errorf(
		"prepare opaque selector candidates: %w", platformrule.ErrSelectorCatalogUnavailable,
	))
	if !terminal || code != "SOURCE_UNAVAILABLE" ||
		message != "selector evidence could not be derived from the recording" {
		t.Fatalf("selector catalog unavailability was not terminal: terminal=%v code=%q message=%q", terminal, code, message)
	}
	terminal, code, message = classifyDSLJobError(fmt.Errorf(
		"prepare opaque selector candidates: %w", platformrule.ErrSelectorSourceUnavailable,
	))
	if !terminal || code != "SOURCE_UNAVAILABLE" ||
		message != "selector evidence could not be derived from the recording" {
		t.Fatalf("selector source-contract failure was not terminal: terminal=%v code=%q message=%q", terminal, code, message)
	}
	terminal, code, message = classifyDSLJobError(fmt.Errorf(
		"chunk workflow: %w", ErrWorkflowSourceUnavailable,
	))
	if !terminal || code != "SOURCE_UNAVAILABLE" ||
		message != "the workflow source cannot fit the configured model context" {
		t.Fatalf("workflow context failure used misleading selector diagnostics: terminal=%v code=%q message=%q", terminal, code, message)
	}
}

func TestDSLManagerZeroRepairFailsAfterFirstReplay(t *testing.T) {
	t.Setenv("LLM_JOB_MAX_ATTEMPTS", "1")
	t.Setenv("LLM_DSL_MAX_REPAIRS", "0")
	cfg := config.Load()
	cfg.LLMEnabled = true
	cfg.LLMModel = "strict-model"
	cfg.LLMJobBatchSize = 10
	cfg.LLMRequestTimeout = time.Second
	persistence, ctx := newDSLManagerStore(t)
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generatedRule := dslManagerGeneratedRule(t, baseline)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "strict-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.MaxRepairs != 0 || job.MaxAttempts != 1 {
		t.Fatalf("strict limits were not persisted: workflow=%+v job=%+v", workflow, job)
	}
	manager.ProcessOnce(context.Background())
	replay, err := manager.StartReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, repairJob, err := manager.CompleteReplay(ctx, workflow.ID, replay.ID, ReplayCompletionInput{
		Succeeded: false, ErrorCode: "REPLAY_FAILED", ErrorMessage: "missing selector",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.ReplayAttemptFailed || repairJob != nil {
		t.Fatalf("zero-repair replay created recovery work: replay=%+v repair=%+v", completed, repairJob)
	}
	failed, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.DSLWorkflowFailed || failed.RepairCount != 0 || failed.MaxRepairs != 0 {
		t.Fatalf("unexpected terminal workflow: %+v", failed)
	}
}

func TestRepairArtifactContextIsBoundedRedactedAndKeepsBothEnds(t *testing.T) {
	data := "VISIBLE_HEAD\n" + strings.Repeat("application content ", maxRepairArtifactContextBytes) +
		"\nVISIBLE_TAIL token=secret-token"
	safe := sanitizeReplayArtifacts([]models.ReplayArtifact{{Name: "errorSnapshot", Type: "dom", Data: data}})
	context := repairArtifactContext(safe)
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxRepairArtifactContextBytes {
		t.Fatalf("repair artifact context exceeded %d bytes: %d", maxRepairArtifactContextBytes, len(encoded))
	}
	text := string(encoded)
	for _, expected := range []string{"VISIBLE_HEAD", "VISIBLE_TAIL", "middle omitted"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("repair artifact context lost %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "secret-token") {
		t.Fatalf("repair artifact context retained a secret: %s", text)
	}
}

func TestDSLManagerValidatesHumanCorrectionBeforeReplay(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generatedRule := dslManagerGeneratedRule(t, baseline)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, _, err := manager.Submit(ctx, requirement.ID, "current-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Correct(ctx, workflow.ID, generatedRule); !errors.Is(err, store.ErrDSLWorkflowState) {
		t.Fatalf("generating workflow accepted a correction: %v", err)
	}
	manager.ProcessOnce(context.Background())
	before, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}

	corrected := dslManagerGeneratedRule(t, baseline)
	corrected.Steps = models.JSON(`[
		{"action":"extractText","name":"name","target":{"selector":".human-corrected-name"}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}"},"immediate":true}
	]`)
	after, err := manager.Correct(ctx, workflow.ID, corrected)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != models.DSLWorkflowAwaitingReplay || after.ProvisionalHash == "" || after.ProvisionalHash == before.ProvisionalHash || after.CurrentJobID != "" {
		t.Fatalf("unexpected corrected workflow: before=%+v after=%+v", before, after)
	}
	if _, _, err := manager.Confirm(ctx, workflow.ID, store.ApproveDSLWorkflowOptions{}); !errors.Is(err, store.ErrDSLWorkflowState) {
		t.Fatalf("human correction bypassed replay confirmation: %v", err)
	}

	repeatedCorrection := dslManagerGeneratedRule(t, baseline)
	repeatedCorrection.Steps = models.JSON(`[
		{"action":"waitForTimeout","ms":1500},
		{"action":"extract","name":"items","target":{"selector":"#results > article.card","visible":true},"multiple":true,"onEmpty":"fail","fields":{"name":{"type":"text","selector":"h2.card-title"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
			{"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
		]}
	]`)
	afterRepeated, err := manager.Correct(ctx, workflow.ID, repeatedCorrection)
	if err != nil {
		t.Fatal(err)
	}
	if afterRepeated.ProvisionalRule == nil {
		t.Fatal("repeated correction lost its provisional rule")
	}
	var repeatedSteps []any
	if err := json.Unmarshal(afterRepeated.ProvisionalRule.Steps, &repeatedSteps); err != nil {
		t.Fatal(err)
	}
	if len(repeatedSteps) != 4 {
		t.Fatalf("manager did not preserve readiness plus settling wait: %#v", repeatedSteps)
	}
	readiness := repeatedSteps[0].(map[string]any)
	extraction := repeatedSteps[2].(map[string]any)
	if readiness["action"] != "waitForElementVisible" ||
		!reflect.DeepEqual(readiness["target"], extraction["target"]) {
		t.Fatalf("manager left readiness on a stale selector: wait=%#v extract=%#v", readiness, extraction)
	}
	idempotent, err := manager.Correct(ctx, workflow.ID, afterRepeated.ProvisionalRule)
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.ProvisionalRule == nil ||
		!reflect.DeepEqual(idempotent.ProvisionalRule.Steps, afterRepeated.ProvisionalRule.Steps) {
		t.Fatalf("second manager validation changed stabilized readiness: first=%s second=%v",
			afterRepeated.ProvisionalRule.Steps, idempotent.ProvisionalRule)
	}

	identityChange := dslManagerGeneratedRule(t, baseline)
	identityChange.Entry = "https://example.com/other"
	if _, err := manager.Correct(ctx, workflow.ID, identityChange); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("identity-changing correction was accepted: %v", err)
	}
	unsafe := dslManagerGeneratedRule(t, baseline)
	unsafe.Steps = models.JSON(`[
		{"action":"evaluate","script":"document.cookie"},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}"}}
	]`)
	if _, err := manager.Correct(ctx, workflow.ID, unsafe); !errors.Is(err, ErrInvalidWorkflowInput) {
		t.Fatalf("unsafe correction was accepted: %v", err)
	}
	unchanged, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || unchanged.ProvisionalHash != idempotent.ProvisionalHash {
		t.Fatalf("rejected correction mutated workflow: workflow=%+v err=%v", unchanged, err)
	}
}

func TestDSLManagerAllowsOnlyManualDeterministicDegradedPath(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{LLMEnabled: true, LLMJobBatchSize: 10, RecordingMaxActions: 500, RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024}
	manual := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, nil), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, manual.ID, "current-profile", dslWorkflowBaseline())
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	generated, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || generated.Status != models.DSLWorkflowAwaitingReplay || generated.ProvisionalRule.Name != manual.Requirement.Title {
		t.Fatalf("manual degraded generation failed: workflow=%+v err=%v", generated, err)
	}
	completed, err := manager.GetJob(ctx, job.ID)
	if err != nil || completed.Provider != "deterministic" {
		t.Fatalf("degraded job was not labelled: job=%+v err=%v", completed, err)
	}
}

func TestDSLManagerCanDisableManualDeterministicFallback(t *testing.T) {
	t.Setenv("LLM_JOB_MAX_ATTEMPTS", "1")
	t.Setenv("LLM_ALLOW_DEGRADED_FALLBACK", "false")
	cfg := config.Load()
	cfg.LLMEnabled = true
	cfg.LLMJobBatchSize = 10
	cfg.LLMRequestTimeout = time.Second
	persistence, ctx := newDSLManagerStore(t)
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, nil), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "strict-profile", dslWorkflowBaseline())
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failedJob, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedWorkflow, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedJob.Status != models.DSLJobFailed || failedJob.AttemptCount != 1 || failedJob.Provider == "deterministic" {
		t.Fatalf("disabled fallback produced unexpected job: %+v", failedJob)
	}
	if failedWorkflow.Status != models.DSLWorkflowFailed || failedWorkflow.ProvisionalRule != nil {
		t.Fatalf("disabled fallback produced a provisional rule: %+v", failedWorkflow)
	}
}

func TestDSLManagerRejectsUnsafeGeneratedRulesAndOversizedReplay(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{LLMEnabled: true, LLMJobBatchSize: 10, RecordingMaxActions: 500, RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	unsafe := dslWorkflowBaseline()
	unsafe.Steps = models.JSON(`[
		{"action":"click","target":{"family":"selector","value":"button.delete-account","name":""}},
		{"action":"sendResult","payload":{"name":"value"},"immediate":true}
	]`)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, unsafe)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, unsafeJob, err := manager.Submit(ctx, requirement.ID, "current-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failed, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || failed.Status != models.DSLWorkflowFailed || failed.ErrorCode != "UNSAFE_DSL" {
		t.Fatalf("unsafe rule was not terminally rejected: workflow=%+v err=%v", failed, err)
	}
	unsafeDetail, err := manager.GetProviderAttempt(ctx, unsafeJob.ID, 1)
	if err != nil || unsafeDetail.Report.ValidationPhase != "security-scan" ||
		unsafeDetail.Report.Outcome != models.LLMAttemptFailed {
		t.Fatalf("unsafe provider output was not retained with its gate: detail=%+v err=%v", unsafeDetail, err)
	}

	manual := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceManual)
	generatedRule := dslManagerGeneratedRule(t, baseline)
	safeFake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager = NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, safeFake), zap.NewNop())
	safeWorkflow, _, err := manager.Submit(ctx, manual.ID, "current-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	replay, err := manager.StartReplay(ctx, safeWorkflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = manager.CompleteReplay(ctx, safeWorkflow.ID, replay.ID, ReplayCompletionInput{
		Succeeded: false, Diagnostics: map[string]any{"message": strings.Repeat("x", maxReplayDiagnosticsInputBytes+1)},
	})
	if !errors.Is(err, ErrReplayPayloadTooLarge) {
		t.Fatalf("expected hard-ceiling replay rejection, got %v", err)
	}
	stillRunning, err := manager.GetReplay(ctx, replay.ID)
	if err != nil || stillRunning.Status != models.ReplayAttemptRunning {
		t.Fatalf("hard-ceiling payload mutated replay: replay=%+v err=%v", stillRunning, err)
	}

	completedReplay, repairJob, err := manager.CompleteReplay(ctx, safeWorkflow.ID, replay.ID, ReplayCompletionInput{
		Succeeded: false,
		Diagnostics: map[string]any{
			"terminalStatus": "failure",
			"message":        "Unsupported action: waitForNetworkIdle",
			"logs": []any{
				map[string]any{"type": "status", "status": "running", "message": "Started rule"},
				map[string]any{
					"type": "log", "level": "error", "message": "Step failed: waitForNetworkIdle",
					"extra": map[string]any{
						"error": "Unsupported action: waitForNetworkIdle",
						"page":  strings.Repeat("large-page-diagnostic ", maxReplayDiagnosticsBytes),
						"token": "secret-token",
					},
				},
			},
		},
		ErrorCode:    "REPLAY_FAILED",
		ErrorMessage: "Unsupported action: waitForNetworkIdle",
	})
	if err != nil || completedReplay.Status != models.ReplayAttemptFailed || repairJob == nil {
		t.Fatalf("ordinary oversized diagnostics must schedule bounded repair: replay=%+v repair=%+v err=%v", completedReplay, repairJob, err)
	}
	storedReplay, err := manager.GetReplay(ctx, replay.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedDiagnostics, err := json.Marshal(storedReplay.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedDiagnostics) > maxReplayDiagnosticsBytes {
		t.Fatalf("persisted diagnostics exceeded the bounded repair limit: %d", len(encodedDiagnostics))
	}
	if !strings.Contains(string(encodedDiagnostics), `"truncated":true`) ||
		!strings.Contains(string(encodedDiagnostics), "Unsupported action: waitForNetworkIdle") {
		t.Fatalf("bounded diagnostics lost the failure signal: %s", encodedDiagnostics)
	}
	if strings.Contains(string(encodedDiagnostics), "secret-token") || strings.Contains(string(encodedDiagnostics), "large-page-diagnostic") {
		t.Fatalf("bounded diagnostics retained unsafe verbose data: %s", encodedDiagnostics)
	}
}

func TestDSLManagerPersistsChunkProgressWhileGenerating(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		// Rune-unit budgets: the 21K-rune generation system prompt plus its
		// fixed user scaffolding need most of the window, so the output
		// reserve is explicit and small; timeline items are sized so the
		// analysis budget admits exactly one item per chunk (five chunks).
		LLMEnabled: true, LLMModel: "fake-model", LLMMaxInputTokens: 36_000,
		LLMMaxOutputTokens: 256, LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	large := strings.Repeat("visible semantic text ", 800)
	recordingService := platformrecording.NewService(persistence, cfg)
	recording, err := recordingService.Create(ctx, platformrecording.CreateInput{Payload: map[string]any{
		"version": "2",
		"meta":    map[string]any{"sanitizationVersion": "extension-v2"},
		"snapshots": []any{
			map[string]any{"phase": "initial", "sequence": 0, "actionIndex": 0, "url": "https://example.com/products", "capture": map[string]any{"status": "complete"}, "dom": large + "initial", "domTree": dslManagerSemanticDOM()},
			map[string]any{"phase": "before", "sequence": 1, "actionIndex": 1, "url": "https://example.com/products", "capture": map[string]any{"status": "complete"}, "dom": large + "before", "domTree": dslManagerSemanticDOM()},
			map[string]any{"phase": "final", "sequence": 2, "actionIndex": 2, "url": "https://example.com/products", "capture": map[string]any{"status": "complete"}, "dom": large + "final", "domTree": dslManagerSemanticDOM()},
		},
		"events": []any{
			map[string]any{"action": "type", "value": "safe", "detail": large},
			map[string]any{"action": "click", "detail": large},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	requirement := &models.CollectionRequirement{
		ID: "requirement-progress", RecordingID: recording.ID, Source: models.RequirementSourceLLM,
		Requirement: dslWorkflowRequirement(), Owner: "alice",
	}
	if err := persistence.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	confirmed, err := persistence.ConfirmCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline := dslWorkflowBaseline()
	generatedRule := dslManagerGeneratedRule(t, baseline)
	analysisCalls := 0
	fake := &fakeWorkflowCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		if request.System == analysisSystemPrompt {
			analysisCalls++
			if analysisCalls == 2 {
				return nil, errors.New("provider crashed mid-analysis")
			}
			return completion(`{"observedActions":["safe"]}`), nil
		}
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	_, job, err := manager.Submit(ctx, confirmed.ID, "current-chrome-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	pending, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.DSLJobPending || pending.ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("mid-analysis failure should stay retryable: %+v", pending)
	}
	if pending.ChunkCount != 5 || pending.CompletedChunks != 1 {
		t.Fatalf("per-chunk progress was not persisted: %+v", pending)
	}
	if pending.Provider != "fake" || pending.Model != "fake-model" || pending.InputTokens != 25 || pending.OutputTokens != 10 {
		t.Fatalf("successful chunks before a provider failure must remain accounted: %+v", pending)
	}
	if _, err := persistence.DB().Exec(`UPDATE dsl_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.DSLJobCompleted || completed.ChunkCount != 5 || completed.CompletedChunks != 5 {
		t.Fatalf("completed dsl job must persist matching chunk totals: %+v", completed)
	}
	if completed.InputTokens != 175 || completed.OutputTokens != 70 {
		t.Fatalf("retry completion must retain cumulative usage from every successful provider call: %+v", completed)
	}
}

func TestClassifyDSLJobErrorRetriesInvalidRulesButKeepsUnsafeTerminal(t *testing.T) {
	terminal, code, message := classifyDSLJobError(fmt.Errorf("%w: disk unavailable", llm.ErrCompletionCapture))
	if terminal || code != "ARTIFACT_CAPTURE_FAILED" || strings.Contains(message, "disk unavailable") {
		t.Fatalf("capture failure classification was unsafe: terminal=%v code=%s message=%q", terminal, code, message)
	}
	terminal, code, message = classifyDSLJobError(fmt.Errorf("%w: successful execution must submit sendResult", platformrule.ErrInvalidProvisionalRule))
	if terminal || code != "INVALID_DSL" || !strings.Contains(message, "successful execution must submit sendResult") {
		t.Fatalf("invalid provisional rule should be retryable: terminal=%v code=%s message=%q", terminal, code, message)
	}
	terminal, code, _ = classifyDSLJobError(fmt.Errorf("%w: response is missing rule", ErrInvalidLLMRule))
	if terminal || code != "INVALID_DSL" {
		t.Fatalf("invalid llm rule should be retryable: terminal=%v code=%s", terminal, code)
	}
	terminal, code, _ = classifyDSLJobError(fmt.Errorf("%w: bad input", ErrInvalidWorkflowInput))
	if !terminal || code != "INVALID_DSL" {
		t.Fatalf("invalid workflow input should stay terminal: terminal=%v code=%s", terminal, code)
	}
	for _, unsafe := range []error{platformrule.ErrUnsafeProvisionalRule, ErrUnsafeGeneratedRule} {
		terminal, code, _ = classifyDSLJobError(unsafe)
		if !terminal || code != "UNSAFE_DSL" {
			t.Fatalf("unsafe rule should stay terminal: err=%v terminal=%v code=%s", unsafe, terminal, code)
		}
	}
}

type structuredOutputTestError struct {
	code     string
	feedback string
}

func (e structuredOutputTestError) Error() string {
	return "provider returned invalid structured output"
}

func (e structuredOutputTestError) CompletionErrorCode() string {
	return e.code
}

func (e structuredOutputTestError) CompletionValidationFeedback() string {
	return e.feedback
}

func TestClassifyDSLJobErrorReportsStructuredOutputAsInvalidDSL(t *testing.T) {
	for _, providerCode := range []string{
		"structured_output_invalid_arguments",
		"structured_output_call_count",
		"structured_output_wrong_tool",
	} {
		t.Run(providerCode, func(t *testing.T) {
			terminal, code, message := classifyDSLJobError(fmt.Errorf(
				"generate rule: %w",
				structuredOutputTestError{code: providerCode},
			))
			if terminal || code != "INVALID_DSL" {
				t.Fatalf(
					"structured output mismatch classification = terminal %v code %q",
					terminal,
					code,
				)
			}
			if message != "provider strict structured output did not match the generated DSL schema ("+
				providerCode+")" {
				t.Fatalf("structured output mismatch message = %q", message)
			}
		})
	}
}

func TestClassifyDSLJobErrorPreservesBoundedStructuredOutputFeedback(t *testing.T) {
	feedback := "strict structured output arguments do not match schema: validating /rule/steps/3; " +
		"object shapes: $={rule,selectorCatalogHash}; structural fragment: " +
		`{"rule":{"steps":[{"action":"<string>"}]}}`
	terminal, code, message := classifyDSLJobError(fmt.Errorf(
		"generate rule: %w",
		structuredOutputTestError{code: "structured_output_invalid_arguments", feedback: feedback},
	))
	if terminal || code != "INVALID_DSL" {
		t.Fatalf("structured output feedback classification = terminal %v code %q", terminal, code)
	}
	for _, expected := range []string{"/rule/steps/3", "object shapes:", "structural fragment:"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("structured output feedback omitted %q: %q", expected, message)
		}
	}
	if len(message) > maxDSLValidationFeedbackBytes {
		t.Fatalf("structured output feedback exceeded bound: %d", len(message))
	}
}

func TestClassifyDSLJobErrorBoundsAndRedactsValidationFeedback(t *testing.T) {
	message := "token: provider-secret " + strings.Repeat("x", maxDSLValidationFeedbackBytes*2)
	terminal, code, safe := classifyDSLJobError(fmt.Errorf("%w: %s", platformrule.ErrInvalidProvisionalRule, message))
	if terminal || code != "INVALID_DSL" {
		t.Fatalf("invalid provisional rule should remain retryable: terminal=%v code=%s", terminal, code)
	}
	if strings.Contains(safe, "provider-secret") || !strings.Contains(safe, "[REDACTED]") {
		t.Fatalf("validation feedback was not redacted: %q", safe)
	}
	if len(safe) > maxDSLValidationFeedbackBytes {
		t.Fatalf("validation feedback exceeded bound: %d", len(safe))
	}
}

func TestDSLManagerRetriesInvalidGeneratedRuleWithFeedback(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generatedRule := dslManagerGeneratedRule(t, baseline)
	// The fake simulates the response cache: the identical first-attempt
	// prompt returns the same interaction-only rule, while the
	// feedback-augmented retry prompt is a new request that returns a rule
	// satisfying the collection-output contract.
	fake := &fakeWorkflowCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		if strings.Contains(request.User, "A previous attempt returned an invalid or unsafe structure") {
			return completion(ruleEnvelope(t, generatedRule)), nil
		}
		return completion(ruleEnvelope(t, baseline)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, job, err := manager.Submit(ctx, requirement.ID, "current-chrome-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	pending, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.DSLJobPending || pending.AttemptCount != 1 || pending.ErrorCode != "INVALID_DSL" {
		t.Fatalf("invalid generated rule should stay retryable: %+v", pending)
	}
	if !strings.Contains(pending.ErrorMessage, "successful execution must submit the confirmed output fields with sendResult") {
		t.Fatalf("durable job omitted exact validation feedback: %q", pending.ErrorMessage)
	}
	if pending.Provider != "fake" || pending.Model != "fake-model" || pending.InputTokens != 25 || pending.OutputTokens != 10 {
		t.Fatalf("schema-invalid provider response must retain billable usage: %+v", pending)
	}
	if pending.ChunkCount != 1 || pending.CompletedChunks != 1 {
		t.Fatalf("schema-invalid full response must retain completed provider progress: %+v", pending)
	}
	generating, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || generating.Status != models.DSLWorkflowGenerating {
		t.Fatalf("retryable failure must not fail the workflow: workflow=%+v err=%v", generating, err)
	}
	if len(fake.requests) != 1 || strings.Contains(fake.requests[0].User, "A previous attempt") {
		t.Fatalf("first attempt must not carry retry feedback: %+v", fake.requests)
	}
	if _, err := persistence.DB().Exec(`UPDATE dsl_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.DSLJobCompleted || completed.AttemptCount != 2 || len(fake.requests) != 2 {
		t.Fatalf("feedback retry did not complete the same durable job: job=%+v calls=%d", completed, len(fake.requests))
	}
	if completed.InputTokens != 50 || completed.OutputTokens != 20 {
		t.Fatalf("completed retry job omitted cumulative attempt usage: %+v", completed)
	}
	if !strings.Contains(fake.requests[1].User, "A previous attempt returned an invalid or unsafe structure: invalid provisional rule: successful execution must submit the confirmed output fields with sendResult") {
		t.Fatalf("second attempt prompt omitted the previous failure: %q", fake.requests[1].User)
	}
	awaiting, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || awaiting.Status != models.DSLWorkflowAwaitingReplay {
		t.Fatalf("feedback retry should reach replay: workflow=%+v err=%v", awaiting, err)
	}
	attempts, err := manager.ListProviderAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 2 ||
		attempts[0].AttemptNumber != 1 || attempts[0].Outcome != models.LLMAttemptFailed ||
		attempts[1].AttemptNumber != 2 || attempts[1].Outcome != models.LLMAttemptSucceeded {
		t.Fatalf("durable retry did not retain one report per claimed attempt: attempts=%+v err=%v", attempts, err)
	}
}

// TestGenerationPersistsSealingSafetyFlagsOnWorkflow verifies that
// ScanSealingFlags output is appended to the manager's safetyFlags at
// generation success AND that the union is persisted on the
// dsl_workflows.safety_flags column at the awaiting_replay transition
// (WI-7).
func TestGenerationPersistsSealingSafetyFlagsOnWorkflow(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	// Build a generated rule whose extraction selector is clean (so it
	// passes selector evidence) but whose navigate step targets an
	// external domain. ScanSealingFlags must flag this as
	// "external-resource-load" without failing generation.
	generatedRule := dslManagerGeneratedRule(t, baseline)
	var providerSteps []any
	if err := json.Unmarshal(generatedRule.Steps, &providerSteps); err != nil {
		t.Fatal(err)
	}
	providerSteps = append([]any{
		map[string]any{
			"action": "navigate",
			"url":    "https://evil.example.com/payload",
		},
		map[string]any{
			"action": "click",
			"target": map[string]any{
				"family": "selector", "value": "#results", "name": "",
			},
		},
	}, providerSteps...)
	generatedRule.Steps, _ = json.Marshal(providerSteps)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, _, err := manager.Submit(ctx, requirement.ID, "current-chrome-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	generated, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Status != models.DSLWorkflowAwaitingReplay {
		t.Fatalf("generation did not reach awaiting_replay: %+v", generated)
	}
	// The dsl_workflows.safety_flags column must carry the sealing flag.
	var workflowSafetyFlags string
	if err := persistence.DB().QueryRowContext(ctx,
		`SELECT safety_flags FROM dsl_workflows WHERE id = ?`, workflow.ID,
	).Scan(&workflowSafetyFlags); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(workflowSafetyFlags, "external-resource-load") {
		t.Fatalf("expected dsl_workflows.safety_flags to include external-resource-load, got %q", workflowSafetyFlags)
	}
}

// TestGenerationPersistsContentFilterSafetyFlagsOnWorkflow verifies WI-15:
// ContentFilterRule output is appended to the manager's safetyFlags at
// generation success, producing content-filter:* entries on the
// dsl_workflows.safety_flags column.
func TestGenerationPersistsContentFilterSafetyFlagsOnWorkflow(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "fake-model", LLMJobBatchSize: 10,
		LLMRequestTimeout: time.Second, RecordingMaxActions: 500,
		RecordingMaxDuration: time.Hour, RecordingMaxCompressedBytes: 1024 * 1024,
	}
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	// Build a generated rule whose extraction selector is clean (so it
	// passes selector evidence) but whose navigate step targets an
	// external domain. ContentFilterRule must flag this as
	// "content-filter:external-url" without failing generation.
	generatedRule := dslManagerGeneratedRule(t, baseline)
	var providerSteps []any
	if err := json.Unmarshal(generatedRule.Steps, &providerSteps); err != nil {
		t.Fatal(err)
	}
	providerSteps = append([]any{
		map[string]any{
			"action": "navigate",
			"url":    "https://evil.example.com/payload",
		},
		map[string]any{
			"action": "click",
			"target": map[string]any{
				"family": "selector", "value": "#results", "name": "",
			},
		},
	}, providerSteps...)
	generatedRule.Steps, _ = json.Marshal(providerSteps)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generatedRule)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, _, err := manager.Submit(ctx, requirement.ID, "current-chrome-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	generated, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Status != models.DSLWorkflowAwaitingReplay {
		t.Fatalf("generation did not reach awaiting_replay: %+v", generated)
	}
	var workflowSafetyFlags string
	if err := persistence.DB().QueryRowContext(ctx,
		`SELECT safety_flags FROM dsl_workflows WHERE id = ?`, workflow.ID,
	).Scan(&workflowSafetyFlags); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(workflowSafetyFlags, "content-filter:external-url") {
		t.Fatalf("expected dsl_workflows.safety_flags to include content-filter:external-url, got %q", workflowSafetyFlags)
	}
}
