package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"go.uber.org/zap"
)

type wrongCohortSelectorRepairCompleter struct {
	t                   *testing.T
	generated           *models.Rule
	correctRowID        string
	wrongRowID          string
	selectorRepairCalls int
	selectorPrompt      string
	selectorRequest     llm.CompletionRequest
	invalidRepairPatch  bool
}

type generationOnlySelectorRepairCompleter struct {
	inner *wrongCohortSelectorRepairCompleter
}

func resealSelectorRepairExport(t *testing.T, value *AdminReviewedDSLAttemptExport) {
	t.Helper()
	if value == nil || value.Attempt == nil || value.Artifact == nil ||
		value.Artifact.SelectorRepair == nil {
		t.Fatal("selector-repair export is incomplete")
	}
	lineage := value.Artifact.SelectorRepair
	selectorCatalog, err := json.Marshal(value.Artifact.SelectorCatalog)
	if err != nil {
		t.Fatal(err)
	}
	lineage.RepairPlanHash = selectorRepairPlanHash(SelectorRepairJobRequest{
		SourceAttemptReportID: lineage.SourceAttemptReportID,
		SourceProviderIRHash:  lineage.SourceProviderIRHash,
		RecordingHash:         value.Attempt.RecordingHash,
		RequirementHash:       value.Attempt.RequirementHash,
		BaselineHash:          value.Attempt.BaselineHash,
		SelectorCatalogHash:   value.Attempt.SelectorCatalogHash,
		SelectorCatalog:       string(selectorCatalog),
		ProviderIR:            lineage.SourceProviderIR,
		Diagnostic:            lineage.Diagnostic,
		OutputContract:        lineage.OutputContract,
		Failure:               lineage.Failure,
		Slots:                 lineage.Slots,
		AllowedAssignments:    lineage.AllowedAssignments,
	})
	lineage.PatchProviderIRHash = dslSemanticJSONHash(value.Artifact.ProviderIR)
	value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
}

func (c *generationOnlySelectorRepairCompleter) Complete(
	ctx context.Context,
	request llm.CompletionRequest,
) (*llm.CompletionResult, error) {
	return c.inner.Complete(ctx, request)
}

func (c *wrongCohortSelectorRepairCompleter) Complete(
	_ context.Context,
	request llm.CompletionRequest,
) (*llm.CompletionResult, error) {
	content := testProviderSelectorEnvelope(request, ruleEnvelope(c.t, c.generated))
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		c.t.Fatal(err)
	}
	rule := envelope["rule"].(map[string]any)
	steps := rule["steps"].([]any)
	target := steps[0].(map[string]any)["target"].(map[string]any)
	c.correctRowID, _ = target["rowCandidateId"].(string)
	marker := `{"version":"selector-catalog-v5"`
	index := strings.Index(request.User, marker)
	if index < 0 {
		c.t.Fatal("generation prompt omitted selector catalog")
	}
	var catalog testSelectorPromptCatalog
	if err := json.NewDecoder(strings.NewReader(request.User[index:])).Decode(&catalog); err != nil {
		c.t.Fatal(err)
	}
	for _, candidate := range catalog.Candidates {
		if candidate.RowCandidateID != "" && candidate.RowCandidateID != c.correctRowID {
			c.wrongRowID = candidate.RowCandidateID
			break
		}
	}
	if c.correctRowID == "" || c.wrongRowID == "" {
		c.t.Fatalf("test catalog lacks two row cohorts: %+v", catalog)
	}
	target["rowCandidateId"] = c.wrongRowID
	encoded, _ := json.Marshal(envelope)
	return completion(string(encoded)), nil
}

func (c *wrongCohortSelectorRepairCompleter) CompleteOnce(
	_ context.Context,
	request llm.CompletionRequest,
) (*llm.CompletionResult, error) {
	c.selectorRepairCalls++
	c.selectorPrompt = request.User
	c.selectorRequest = request
	if request.ExecutionPolicy != llm.CompletionExecutionAtMostOnce {
		c.t.Fatalf("selector repair did not request at-most-once execution: %+v", request)
	}
	var prompt map[string]any
	if err := json.Unmarshal([]byte(request.User), &prompt); err != nil {
		c.t.Fatal(err)
	}
	slots, _ := prompt["repairSlots"].([]any)
	var targetSlot string
	for _, raw := range slots {
		slot, _ := raw.(map[string]any)
		if slot["kind"] == "rowCandidateId" {
			targetSlot, _ = slot["slotId"].(string)
			break
		}
	}
	if targetSlot == "" {
		c.t.Fatalf("repair prompt omitted the failed target slot: %#v", prompt)
	}
	candidateID := c.correctRowID
	if c.invalidRepairPatch {
		candidateID = c.wrongRowID
	}
	content, _ := json.Marshal(map[string]any{
		"selectorCatalogHash": prompt["selectorCatalogHash"],
		"replacements": []any{map[string]any{
			"slotId": targetSlot, "candidateId": candidateID,
		}},
	})
	return completion(string(content)), nil
}

func TestDSLManagerAutomaticallyRepairsBaiduLikeWrongCohortWithoutRecordingResend(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	cfg.LLMMaxOutputTokens = 2_048
	requirement := createDSLManagerRequirement(
		t, persistence, ctx, cfg, models.RequirementSourceLLM,
	)
	baseline := dslWorkflowBaseline()
	generated := &models.Rule{}
	encoded, _ := json.Marshal(baseline)
	_ = json.Unmarshal(encoded, generated)
	generated.Steps = models.JSON(`[
		{"action":"extract","name":"rows","multiple":true,
		 "target":{"selector":"#results > .card"},
		 "fields":{"name":{"type":"text","selector":".card-title"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
		]}
	]`)
	completer := &wrongCohortSelectorRepairCompleter{t: t, generated: generated}
	manager := NewDSLManager(
		persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop(),
	)
	workflow, sourceJob, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	repairing, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repairing.Status != models.DSLWorkflowRepairing ||
		repairing.RepairCount != 0 || repairing.CurrentJobID == sourceJob.ID {
		t.Fatalf("eligible source attempt did not atomically schedule selector repair: %+v", repairing)
	}
	failedSource, err := manager.GetJob(ctx, sourceJob.ID)
	if err != nil || failedSource.Status != models.DSLJobFailed {
		t.Fatalf("source job was not terminalized: job=%+v err=%v", failedSource, err)
	}
	child, err := manager.GetJob(ctx, repairing.CurrentJobID)
	if err != nil || child.Kind != models.DSLJobSelectorRepair ||
		child.MaxAttempts != 1 || child.SourceAttemptReportID == "" {
		t.Fatalf("unexpected selector repair child: job=%+v err=%v", child, err)
	}
	sourceAttempts, err := manager.ListProviderAttempts(ctx, sourceJob.ID)
	if err != nil || len(sourceAttempts) != 1 ||
		sourceAttempts[0].ID != child.SourceAttemptReportID ||
		!sourceAttempts[0].Replayable {
		t.Fatalf("child is not bound to its replayable source attempt: attempts=%+v err=%v", sourceAttempts, err)
	}

	manager.ProcessOnce(context.Background())
	repaired, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != models.DSLWorkflowAwaitingReplay ||
		repaired.ProvisionalRule == nil || completer.selectorRepairCalls != 1 {
		t.Fatalf("selector repair did not publish exactly one awaiting-replay rule: workflow=%+v calls=%d",
			repaired, completer.selectorRepairCalls)
	}
	if completer.selectorRequest.MaxOutputTokens != cfg.LLMMaxOutputTokens {
		t.Fatalf(
			"selector repair output token cap = %d, want %d",
			completer.selectorRequest.MaxOutputTokens,
			cfg.LLMMaxOutputTokens,
		)
	}
	for _, forbidden := range []string{
		`"recording"`, `"events"`, `"snapshots"`, `"chunkAnalyses"`, `"replayArtifacts"`,
	} {
		if strings.Contains(completer.selectorPrompt, forbidden) {
			t.Fatalf("selector repair resent forbidden source %q", forbidden)
		}
	}
	completedChild, err := manager.GetJob(ctx, child.ID)
	if err != nil || completedChild.Status != models.DSLJobCompleted ||
		completedChild.PromptVersion != "dsl-selector-repair-v1" {
		t.Fatalf("unexpected completed child: job=%+v err=%v", completedChild, err)
	}
	detail, err := manager.GetProviderAttempt(ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Calls) != 1 ||
		detail.Calls[0].Phase != llm.CompletionPhaseSelectorRepair {
		t.Fatalf("selector repair made more than one physical provider call: %+v", detail.Calls)
	}
	artifact, _ := detail.Artifact.(map[string]any)
	lineage, _ := artifact["selectorRepair"].(map[string]any)
	if lineage["sourceAttemptReportId"] != child.SourceAttemptReportID ||
		lineage["sourceProviderIrHash"] == "" ||
		lineage["sourceProviderIr"] == nil ||
		lineage["patchProviderIrHash"] == "" ||
		lineage["repairPlanHash"] == "" ||
		lineage["diagnostic"] == nil ||
		lineage["outputContract"] == nil ||
		lineage["failure"] == nil ||
		lineage["derivedProviderIrHash"] == "" ||
		lineage["derivedProviderIr"] == nil ||
		lineage["slots"] == nil ||
		lineage["allowedAssignments"] == nil ||
		artifact["resolvedRule"] == nil {
		t.Fatalf("selector repair history lacks source→patch→derived→resolved lineage: %#v", artifact)
	}

	exported := generatedAttemptExport(t, manager, child.ID)
	for _, test := range []struct {
		name   string
		mutate func(*AdminReviewedDSLAttemptExport)
	}{
		{name: "source provider IR", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.SourceProviderIR.Name = "tampered source"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}},
		{name: "patch provider IR", mutate: func(value *AdminReviewedDSLAttemptExport) {
			patch := value.Artifact.ProviderIR.(map[string]any)
			patch["selectorCatalogHash"] = "tampered"
			resealSelectorRepairExport(t, value)
		}},
		{name: "selector failure", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.Failure.Detail += " tampered"
			resealSelectorRepairExport(t, value)
		}},
		{name: "selector diagnostic", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.Diagnostic.Detail += " tampered"
			resealSelectorRepairExport(t, value)
		}},
		{name: "output contract", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.OutputContract = json.RawMessage(`{"type":"object"}`)
			resealSelectorRepairExport(t, value)
		}},
		{name: "derived provider IR", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.DerivedProviderIR.Name = "tampered derived"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}},
		{name: "repair plan hash", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.RepairPlanHash = strings.Repeat("0", 64)
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}},
		{name: "repair slots", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.Slots[0].AllowedCandidateIDs = append(
				value.Artifact.SelectorRepair.Slots[0].AllowedCandidateIDs, "tampered-candidate",
			)
			resealSelectorRepairExport(t, value)
		}},
		{name: "allowed assignments", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.SelectorRepair.AllowedAssignments = append(
				value.Artifact.SelectorRepair.AllowedAssignments,
				value.Artifact.SelectorRepair.AllowedAssignments[0],
			)
			resealSelectorRepairExport(t, value)
		}},
		{name: "resolved rule", mutate: func(value *AdminReviewedDSLAttemptExport) {
			value.Artifact.ResolvedRule.Name = "tampered resolved"
			value.Attempt.ArtifactHash = dslJSONHash(value.Artifact)
		}},
	} {
		t.Run("adoption rejects tampered "+test.name, func(t *testing.T) {
			candidate := cloneGeneratedAttemptExport(t, exported)
			test.mutate(&candidate)
			if _, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "profile", candidate); !errors.Is(err, ErrInvalidWorkflowInput) {
				t.Fatalf("tampered selector-repair export was accepted: %v", err)
			}
		})
	}
	adopted, err := manager.AdoptAdminReviewedAttempt(ctx, requirement.ID, "profile", exported)
	if err != nil || adopted.Status != models.DSLWorkflowAwaitingReplay || adopted.CurrentJobID != "" {
		t.Fatalf("legitimate selector-repair export was not adopted: workflow=%+v err=%v", adopted, err)
	}
}

func TestDSLManagerSelectorRepairGateRetainsSourceFailureWithoutChild(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	disabled := false
	cfg.LLMDSLSelectorRepairEnabled = &disabled
	requirement := createDSLManagerRequirement(
		t, persistence, ctx, cfg, models.RequirementSourceLLM,
	)
	baseline := dslWorkflowBaseline()
	generated := &models.Rule{}
	encoded, _ := json.Marshal(baseline)
	_ = json.Unmarshal(encoded, generated)
	generated.Steps = models.JSON(`[
		{"action":"extract","name":"rows","multiple":true,
		 "target":{"selector":"#results > .card"},
		 "fields":{"name":{"type":"text","selector":".card-title"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
		]}
	]`)
	completer := &wrongCohortSelectorRepairCompleter{t: t, generated: generated}
	manager := NewDSLManager(
		persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop(),
	)
	workflow, sourceJob, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}

	manager.ProcessOnce(context.Background())

	failedWorkflow, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil ||
		failedWorkflow.Status != models.DSLWorkflowFailed ||
		failedWorkflow.CurrentJobID != sourceJob.ID ||
		failedWorkflow.ProvisionalRule != nil ||
		failedWorkflow.ProvisionalHash != "" ||
		failedWorkflow.LastReplaySequence != 0 {
		t.Fatalf("disabled selector repair changed the source failure boundary: workflow=%+v err=%v",
			failedWorkflow, err)
	}
	failedSource, err := manager.GetJob(ctx, sourceJob.ID)
	if err != nil ||
		failedSource.Status != models.DSLJobFailed ||
		failedSource.AttemptCount != 1 ||
		failedSource.MaxAttempts != 1 ||
		!strings.Contains(strings.Join(failedSource.SafetyFlags, " "), "selector-repair:auto-disabled") {
		t.Fatalf("source failure lost the disabled scheduling audit: job=%+v err=%v", failedSource, err)
	}
	var jobCount int
	if err := persistence.DB().QueryRow(
		`SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, workflow.ID,
	).Scan(&jobCount); err != nil || jobCount != 1 {
		t.Fatalf("disabled selector repair created a child: count=%d err=%v", jobCount, err)
	}
	if completer.selectorRepairCalls != 0 {
		t.Fatalf("disabled selector repair reached the provider: calls=%d", completer.selectorRepairCalls)
	}
	detail, err := manager.GetProviderAttempt(ctx, sourceJob.ID, 1)
	if err != nil ||
		detail.Report == nil ||
		detail.Report.Outcome != models.LLMAttemptFailed ||
		detail.Report.ValidationPhase != "selector-candidate-resolution" ||
		len(detail.Calls) != 1 {
		t.Fatalf("source provider attempt was not retained exactly once: detail=%+v err=%v", detail, err)
	}
	artifactJSON, _ := json.Marshal(detail.Artifact)
	if !strings.Contains(string(artifactJSON), `"selector-repair:auto-disabled"`) {
		t.Fatalf("attempt artifact omitted disabled scheduling evidence: %s", artifactJSON)
	}
}

func TestDSLManagerSelectorRepairFailureIsOneShotAuditableAndHumanCorrectable(t *testing.T) {
	persistence, ctx := newDSLManagerStore(t)
	cfg := dslAttemptConfig()
	requirement := createDSLManagerRequirement(
		t, persistence, ctx, cfg, models.RequirementSourceLLM,
	)
	baseline := dslWorkflowBaseline()
	generated := &models.Rule{}
	encoded, _ := json.Marshal(baseline)
	_ = json.Unmarshal(encoded, generated)
	generated.Steps = models.JSON(`[
		{"action":"extract","name":"rows","multiple":true,
		 "target":{"selector":"#results > .card"},
		 "fields":{"name":{"type":"text","selector":".card-title"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
		]}
	]`)
	completer := &wrongCohortSelectorRepairCompleter{
		t: t, generated: generated, invalidRepairPatch: true,
	}
	manager := NewDSLManager(
		persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop(),
	)
	workflow, sourceJob, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	scheduled, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || scheduled.Status != models.DSLWorkflowRepairing {
		t.Fatalf("source attempt did not schedule selector repair: workflow=%+v err=%v",
			scheduled, err)
	}
	childID := scheduled.CurrentJobID
	manager.ProcessOnce(context.Background())

	failed, err := manager.GetWorkflow(ctx, workflow.ID)
	if err != nil || failed.Status != models.DSLWorkflowFailed ||
		failed.ProvisionalRule != nil || failed.ProvisionalHash != "" ||
		failed.CurrentJobID != childID || completer.selectorRepairCalls != 1 {
		t.Fatalf("invalid selector patch was not a one-shot no-provisional failure: workflow=%+v calls=%d err=%v",
			failed, completer.selectorRepairCalls, err)
	}
	child, err := manager.GetJob(ctx, childID)
	if err != nil || child.Status != models.DSLJobFailed ||
		child.AttemptCount != 1 || child.MaxAttempts != 1 {
		t.Fatalf("failed selector repair remained retryable: job=%+v err=%v", child, err)
	}
	var jobCount int
	if err := persistence.DB().QueryRow(
		`SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, workflow.ID,
	).Scan(&jobCount); err != nil || jobCount != 2 {
		t.Fatalf("selector repair spawned another child: count=%d err=%v", jobCount, err)
	}
	sourceDetail, err := manager.GetProviderAttempt(ctx, sourceJob.ID, 1)
	if err != nil || sourceDetail.Report == nil || len(sourceDetail.Calls) != 1 {
		t.Fatalf("source attempt history was lost: detail=%+v err=%v", sourceDetail, err)
	}
	childDetail, err := manager.GetProviderAttempt(ctx, childID, 1)
	if err != nil || childDetail.Report == nil ||
		childDetail.Report.Outcome != models.LLMAttemptFailed ||
		len(childDetail.Calls) != 1 ||
		childDetail.Calls[0].Phase != llm.CompletionPhaseSelectorRepair {
		t.Fatalf("failed child history was not retained: detail=%+v err=%v", childDetail, err)
	}
	artifact, _ := childDetail.Artifact.(map[string]any)
	lineage, _ := artifact["selectorRepair"].(map[string]any)
	if lineage["sourceAttemptReportId"] == "" ||
		lineage["sourceProviderIrHash"] == "" ||
		lineage["derivedProviderIr"] != nil ||
		artifact["resolvedRule"] != nil {
		t.Fatalf("failed child artifact has unsafe/incomplete lineage: %#v", artifact)
	}

	corrected := dslManagerGeneratedRule(t, baseline)
	reopened, err := manager.Correct(ctx, workflow.ID, corrected)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != models.DSLWorkflowAwaitingReplay ||
		reopened.ProvisionalRule == nil || reopened.ProvisionalHash == "" {
		t.Fatalf("authorized no-provisional correction did not resume replay: %+v", reopened)
	}
}

func TestDSLManagerSelectorRepairReadinessFailsBeforeDispatch(t *testing.T) {
	tests := []struct {
		name                      string
		unsupported               bool
		shrinkContextPostSchedule bool
	}{
		{name: "unsupported one-shot completer", unsupported: true},
		{name: "context overflow", shrinkContextPostSchedule: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence, ctx := newDSLManagerStore(t)
			cfg := dslAttemptConfig()
			requirement := createDSLManagerRequirement(
				t, persistence, ctx, cfg, models.RequirementSourceLLM,
			)
			baseline := dslWorkflowBaseline()
			generated := &models.Rule{}
			encoded, _ := json.Marshal(baseline)
			_ = json.Unmarshal(encoded, generated)
			generated.Steps = models.JSON(`[
				{"action":"extract","name":"rows","multiple":true,
				 "target":{"selector":"#results > .card"},
				 "fields":{"name":{"type":"text","selector":".card-title"}}},
				{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
				 {"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
				]}
			]`)
			inner := &wrongCohortSelectorRepairCompleter{t: t, generated: generated}
			var completer workflowCompleter = inner
			if test.unsupported {
				completer = &generationOnlySelectorRepairCompleter{inner: inner}
			}
			manager := NewDSLManager(
				persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop(),
			)
			workflow, _, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
			if err != nil {
				t.Fatal(err)
			}
			manager.ProcessOnce(context.Background())
			scheduled, err := manager.GetWorkflow(ctx, workflow.ID)
			if err != nil || scheduled.Status != models.DSLWorkflowRepairing {
				t.Fatalf("source did not schedule child: workflow=%+v err=%v", scheduled, err)
			}
			childID := scheduled.CurrentJobID
			if test.shrinkContextPostSchedule {
				cfg.LLMMaxInputTokens = 1
			}
			manager.ProcessOnce(context.Background())
			child, err := manager.GetJob(ctx, childID)
			if err != nil || child.Status != models.DSLJobFailed ||
				child.ProviderDispatched || inner.selectorRepairCalls != 0 {
				t.Fatalf("deterministic readiness failure crossed dispatch: job=%+v calls=%d err=%v",
					child, inner.selectorRepairCalls, err)
			}
			failed, err := manager.GetWorkflow(ctx, workflow.ID)
			if err != nil || failed.Status != models.DSLWorkflowFailed ||
				failed.ProvisionalRule != nil {
				t.Fatalf("readiness failure was not explicit and terminal: workflow=%+v err=%v",
					failed, err)
			}
			detail, err := manager.GetProviderAttempt(ctx, childID, 1)
			if err != nil || detail.Report == nil || len(detail.Calls) != 0 ||
				detail.Report.ValidationPhase != "provider-readiness" {
				t.Fatalf("zero-call readiness failure was not audited: detail=%+v err=%v",
					detail, err)
			}
		})
	}
}

func TestDSLManagerDoesNotScheduleSelectorRepairWhenImmutableRuleIsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		steps string
	}{
		{
			name: "output mutation",
			steps: `[
				{"action":"extract","name":"rows","multiple":true,
				 "target":{"selector":"#results > .card"},
				 "fields":{"name":{"type":"text","selector":".card-title"}}},
				{"action":"sendResult","payload":{"unexpected":"{{extracted.rows}}"},"immediate":true}
			]`,
		},
		{
			name: "unsafe action",
			steps: `[
				{"action":"extract","name":"rows","multiple":true,
				 "target":{"selector":"#results > .card"},
				 "fields":{"name":{"type":"text","selector":".card-title"}}},
				{"action":"evaluate","script":"return document.cookie"},
				{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"row","steps":[
				 {"action":"sendResult","payload":{"name":"{{loopItem.name}}"},"immediate":true}
				]}
			]`,
		},
		{
			name: "non selector contract defect",
			steps: `[
				{"action":"extract","name":"rows","multiple":true,
				 "target":{"selector":"#results > .card"},
				 "fields":{"name":{"type":"text","selector":".card-title"}}},
				{"action":"sendResult","payload":{"name":"{{extracted.rows}}"},"immediate":true}
			]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence, ctx := newDSLManagerStore(t)
			cfg := dslAttemptConfig()
			requirement := createDSLManagerRequirement(
				t, persistence, ctx, cfg, models.RequirementSourceLLM,
			)
			baseline := dslWorkflowBaseline()
			generated := &models.Rule{}
			encoded, _ := json.Marshal(baseline)
			_ = json.Unmarshal(encoded, generated)
			generated.Steps = models.JSON(test.steps)
			completer := &wrongCohortSelectorRepairCompleter{t: t, generated: generated}
			manager := NewDSLManager(
				persistence, cfg, NewDSLWorkflow(cfg, completer), zap.NewNop(),
			)
			workflow, sourceJob, err := manager.Submit(ctx, requirement.ID, "profile", baseline)
			if err != nil {
				t.Fatal(err)
			}
			manager.ProcessOnce(context.Background())
			failed, err := manager.GetWorkflow(ctx, workflow.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failed.Status != models.DSLWorkflowFailed ||
				failed.CurrentJobID != sourceJob.ID ||
				completer.selectorRepairCalls != 0 {
				t.Fatalf("masked immutable defect consumed selector repair: workflow=%+v calls=%d",
					failed, completer.selectorRepairCalls)
			}
			source, err := manager.GetJob(ctx, sourceJob.ID)
			if err != nil || source.Status != models.DSLJobFailed {
				t.Fatalf("source failure was not consistently terminal: source=%+v err=%v", source, err)
			}
			reports, err := manager.ListProviderAttempts(ctx, sourceJob.ID)
			if err != nil || len(reports) != 1 ||
				reports[0].ErrorCode != source.ErrorCode ||
				reports[0].ErrorMessage != source.ErrorMessage {
				t.Fatalf("job/report terminal diagnostics diverged: source=%+v reports=%+v err=%v",
					source, reports, err)
			}
		})
	}
}
