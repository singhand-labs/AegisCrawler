package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	llmdsl "github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	llmrequirement "github.com/singhand-labs/AegisCrawler/internal/llm/requirement"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

const platformE2EWorkerToken = "platform-e2e-worker"

// Go cannot import the TypeScript executor directly. The companion Vitest
// executes this exact rule through production runRule and verifies every byte
// of the fixture before this test submits its captured outcomes through HTTP.
const platformRealExecutorReplayFixtureSHA256 = "7713eb9aab0457cacf233d7cbb0343db0dde35760108f3828e35a6591fcff7ab"

type platformRealExecutorReplayFixture struct {
	SchemaVersion string         `json:"schemaVersion"`
	Rule          models.Rule    `json:"rule"`
	Variables     map[string]any `json:"variables"`
	Scenarios     []struct {
		Name     string `json:"name"`
		Expected struct {
			ReplayCompletion llmdsl.ReplayCompletionInput `json:"replayCompletion"`
		} `json:"expected"`
	} `json:"scenarios"`
}

func loadPlatformRealExecutorReplayFixture(t *testing.T) platformRealExecutorReplayFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "adopted-real-executor-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(data)); digest != platformRealExecutorReplayFixtureSHA256 {
		t.Fatalf("real-executor replay fixture hash changed: got %s want %s",
			digest, platformRealExecutorReplayFixtureSHA256)
	}
	var fixture platformRealExecutorReplayFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != "aegiscrawler.real-executor-replay.v1" {
		t.Fatalf("unexpected real-executor replay fixture schema %q", fixture.SchemaVersion)
	}
	return fixture
}

func assertPlatformRealExecutorReplayRule(t *testing.T, fixture, adopted *models.Rule) {
	t.Helper()
	fixtureRule, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	adoptedRule, err := json.Marshal(adopted)
	if err != nil {
		t.Fatal(err)
	}
	var fixtureValue, adoptedValue any
	if err := json.Unmarshal(fixtureRule, &fixtureValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(adoptedRule, &adoptedValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixtureValue, adoptedValue) {
		t.Fatalf("real-executor fixture rule drifted from adopted provisional rule:\nfixture=%s\nadopted=%s",
			fixtureRule, adoptedRule)
	}
}

func platformRealExecutorReplayScenario(
	t *testing.T,
	fixture platformRealExecutorReplayFixture,
	name string,
) llmdsl.ReplayCompletionInput {
	t.Helper()
	for _, scenario := range fixture.Scenarios {
		if scenario.Name == name {
			return scenario.Expected.ReplayCompletion
		}
	}
	t.Fatalf("real-executor replay scenario %q is missing", name)
	return llmdsl.ReplayCompletionInput{}
}

// platformE2ECompleter is the only fake in this test's product pipeline. It
// replaces external model calls while the durable managers, store, HTTP APIs,
// replay state machine, worker protocol, scheduler, and MCP server stay real.
type platformE2ECompleter struct {
	mu         sync.Mutex
	candidates string
	rule       *models.Rule
	repairRule *models.Rule
	requests   []llm.CompletionRequest
}

type platformE2EFieldCandidate struct {
	FieldCandidateID         string `json:"fieldCandidateId"`
	ParentFieldCandidateID   string `json:"parentFieldCandidateId"`
	ObservedRelativeSelector string `json:"observedRelativeSelector"`
}

func (f *platformE2ECompleter) Complete(_ context.Context, request llm.CompletionRequest) (*llm.CompletionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if strings.Contains(request.User, "raw-password-value") || strings.Contains(request.User, "raw-token-value") {
		return nil, fmt.Errorf("fake provider received an unsanitized recording")
	}

	content := f.candidates
	rule := f.rule
	if strings.Contains(request.User, "Repair the complete provider rule") && f.repairRule != nil {
		rule = f.repairRule
	}
	if strings.Contains(request.User, "Generate the complete provisional DSL rule") ||
		strings.Contains(request.User, "Generate the complete provisional provider rule") ||
		strings.Contains(request.User, "Repair the complete provider rule") {
		envelope, err := platformE2EProviderEnvelope(request.User, rule)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		content = string(encoded)
	}
	return &llm.CompletionResult{
		CompletionResponse: &llm.CompletionResponse{
			Content: content, InputTokens: 40, OutputTokens: 20,
		},
		Provider: "hermetic-fake", Model: "hermetic-model",
	}, nil
}

func platformE2EProviderEnvelope(prompt string, rule *models.Rule) (map[string]any, error) {
	marker := `{"version":"selector-catalog-v6"`
	index := strings.Index(prompt, marker)
	if index < 0 {
		return nil, fmt.Errorf("provider prompt omitted selector candidate catalog")
	}
	var catalog struct {
		CatalogHash string `json:"catalogHash"`
		Candidates  []struct {
			RowCandidateID    string                      `json:"rowCandidateId"`
			TargetCandidateID string                      `json:"targetCandidateId"`
			ObservedSelector  string                      `json:"observedSelector"`
			FieldCandidates   []platformE2EFieldCandidate `json:"fieldCandidates"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(strings.NewReader(prompt[index:])).Decode(&catalog); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}
	var providerRule map[string]any
	if err := json.Unmarshal(encoded, &providerRule); err != nil {
		return nil, err
	}
	steps, _ := providerRule["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		switch step["action"] {
		case "extract", "extractText", "extractAttribute", "extractTable", "extractJson", "extractHtml":
		default:
			continue
		}
		target, _ := step["target"].(map[string]any)
		selector, _ := target["selector"].(string)
		for _, candidate := range catalog.Candidates {
			if candidate.ObservedSelector != selector {
				continue
			}
			id, key := candidate.TargetCandidateID, "targetCandidateId"
			if step["multiple"] == true {
				id, key = candidate.RowCandidateID, "rowCandidateId"
			}
			if id == "" {
				continue
			}
			delete(target, "selector")
			target[key] = id
			if fields, ok := step["fields"].(map[string]any); ok {
				platformE2ERewriteProviderFields(fields, candidate.FieldCandidates, "")
			}
			break
		}
	}
	return map[string]any{"selectorCatalogHash": catalog.CatalogHash, "rule": providerRule}, nil
}

func platformE2ERewriteProviderFields(
	fields map[string]any,
	candidates []platformE2EFieldCandidate,
	parentID string,
) {
	for _, rawField := range fields {
		field, _ := rawField.(map[string]any)
		fieldSelector, _ := field["selector"].(string)
		for _, candidate := range candidates {
			if candidate.ParentFieldCandidateID != parentID ||
				candidate.ObservedRelativeSelector != fieldSelector {
				continue
			}
			delete(field, "selector")
			field["fieldCandidateId"] = candidate.FieldCandidateID
			if nested, ok := field["fields"].(map[string]any); ok {
				platformE2ERewriteProviderFields(nested, candidates, candidate.FieldCandidateID)
			}
			break
		}
	}
}

func (f *platformE2ECompleter) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func platformE2ERequirement(title string, confidence float64) models.RequirementCandidate {
	return models.RequirementCandidate{
		ID: "provider-id-is-normalized", Confidence: confidence,
		Requirement: models.CollectionRequirementSpec{
			Title: title, Description: "Collect visible product names and prices for a search keyword.",
			RequiredInputs: []models.RequirementInput{{
				Name: "keyword", Type: models.RequirementValueString,
				Description: "Product search keyword", Constraints: map[string]any{"minLength": 1},
			}},
			OptionalInputs: []models.RequirementInput{},
			OutputFields: []models.RequirementOutputField{
				{Name: "name", Type: models.RequirementValueString, Description: "Visible product name"},
				{Name: "price", Type: models.RequirementValueNumber, Description: "Visible product price"},
			},
			SampleOutput: map[string]any{"name": "Example product", "price": 12.5},
		},
	}
}

func newPlatformE2EServer(t *testing.T) (*httptest.Server, *store.Store, *llmrequirement.Manager, *llmdsl.Manager, *platformE2ECompleter) {
	t.Helper()
	persistence, err := store.New(filepath.Join(t.TempDir(), "platform-e2e.db"), "platform-e2e-recording-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })

	candidates := []models.RequirementCandidate{
		platformE2ERequirement("Collect matching products", 0.94),
		platformE2ERequirement("Compare visible product prices", 0.86),
		platformE2ERequirement("Monitor visible product listings", 0.78),
	}
	candidateJSON, err := json.Marshal(map[string]any{"candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	completer := &platformE2ECompleter{
		candidates: string(candidateJSON),
		rule:       platformE2EGeneratedRule(),
		repairRule: platformE2ERepairedRule(),
	}
	cfg := &config.Config{
		AdminAPIKey: mcpTestAdminToken, WorkerAPIKey: platformE2EWorkerToken,
		AuditActor: "platform-admin", RecordingV2Enabled: true, WorkflowV2Enabled: true,
		WorkerProtocolV2Enabled: true, MCPEnabled: true,
		RecordingMaxDuration: time.Hour, RecordingMaxActions: 500,
		RecordingMaxCompressedBytes: 1024 * 1024, RecordingRetention: 24 * time.Hour,
		LLMEnabled: true, LLMModel: "hermetic-model", LLMMaxInputTokens: 40_000,
		LLMJobBatchSize: 10, LLMRequestTimeout: time.Second,
		LeaseDuration: time.Minute, MaxRetries: 0, MaxWorkerTasks: 5,
		RateLimitPerSecond: 1000, RateLimitBurst: 2000,
		WorkerRateLimitPerSecond: 1000, WorkerRateLimitBurst: 2000,
		SiteRateLimitPerSecond: 1000, SiteRateLimitBurst: 2000,
		CircuitBreakerFailureThreshold: 1000, CircuitBreakerFailureWindow: time.Minute,
		CircuitBreakerOpenDuration: time.Minute, RequestTimeout: 10 * time.Second,
		MaxRequestBodyBytes:   2 * 1024 * 1024,
		MCPRateLimitPerSecond: 1000, MCPRateLimitBurst: 2000,
	}
	logger := zap.NewNop()
	runner := scheduler.New(persistence, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	requirementManager := llmrequirement.NewManager(
		persistence, cfg, llmrequirement.NewWorkflow(cfg, completer), logger,
	)
	dslManager := llmdsl.NewDSLManager(
		persistence, cfg, llmdsl.NewDSLWorkflow(cfg, completer), logger,
	)
	handler := NewHandler(persistence, runner, nil, nil, cfg, logger)
	handler.SetRequirementManager(requirementManager)
	handler.SetDSLWorkflowManager(dslManager)
	server := httptest.NewServer(NewRouter(handler, cfg, logger, NewMetrics()))
	t.Cleanup(server.Close)
	return server, persistence, requirementManager, dslManager, completer
}

func platformE2EBaselineRule() *models.Rule {
	return &models.Rule{
		ID: "fixture-products", Version: "1", Name: "Fixture products",
		Domain: models.JSON(`"fixture.local"`), Entry: "https://fixture.local/products",
		Variables: models.JSON(`{"keyword":""}`),
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#search"},"value":"{{keyword}}"},
			{"action":"extractText","name":"name","target":{"selector":".product-name"}},
			{"action":"extractText","name":"price","target":{"selector":".product-price"}}
		]`),
	}
}

func platformE2EGeneratedRule() *models.Rule {
	rule := platformE2EBaselineRule()
	rule.Steps = models.JSON(`[
		{"action":"type","target":{"family":"selector","value":"#search","name":""},"value":"{{keyword}}"},
		{"action":"extractText","name":"name","target":{"selector":".product-name","visible":true}},
		{"action":"extract","name":"price","target":{"selector":".product-price","visible":true},"fields":{"details":{"type":"exists","selector":".amount","fields":{"price":{"type":"number"}}}}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price.details.price}}"},"immediate":true}
	]`)
	return rule
}

func platformE2ERepairedRule() *models.Rule {
	rule := platformE2EGeneratedRule()
	rule.Steps = models.JSON(`[
		{"action":"type","target":{"family":"selector","value":"#search","name":""},"value":"{{keyword}}"},
		{"action":"extractText","name":"name","target":{"selector":".product-name","visible":true}},
		{"action":"extract","name":"price","target":{"selector":".product-price","visible":true},"fields":{"details":{"type":"exists","selector":".amount","fields":{"price":{"type":"number"}}}}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price.details.price}}"},"immediate":false}
	]`)
	return rule
}

func platformE2ERecording() map[string]any {
	initialDOM := map[string]any{"type": "element", "tagName": "main", "children": []any{
		map[string]any{"type": "element", "tagName": "input", "attributes": []any{
			map[string]any{"name": "id", "value": "search"},
			map[string]any{"name": "aria-label", "value": "Search"},
			map[string]any{"name": "type", "value": "password"},
			map[string]any{"name": "value", "value": "raw-password-value"},
		}},
	}}
	beforeDOM := map[string]any{"type": "element", "tagName": "main", "children": []any{
		map[string]any{"type": "text", "text": "Search products"},
	}}
	finalDOM := map[string]any{"type": "element", "tagName": "main", "children": []any{
		map[string]any{"type": "element", "tagName": "span", "attributes": []any{map[string]any{"name": "class", "value": "product-name"}}, "children": []any{map[string]any{"type": "text", "text": "Example product"}}},
		map[string]any{"type": "element", "tagName": "span", "attributes": []any{map[string]any{"name": "class", "value": "product-price"}}, "children": []any{
			map[string]any{"type": "element", "tagName": "strong", "attributes": []any{map[string]any{"name": "class", "value": "amount"}}, "children": []any{
				map[string]any{"type": "text", "text": "$12.50"},
			}},
		}},
	}}
	return map[string]any{
		"version": "2",
		"meta":    map[string]any{"sanitizationVersion": "extension-v2"},
		"events": []any{map[string]any{
			"type": "input", "target": map[string]any{"selector": "#search"}, "value": "token=raw-token-value",
		}},
		"snapshots": []any{
			map[string]any{
				"phase": "initial", "sequence": 0, "actionIndex": 0, "url": "https://fixture.local/products",
				"capture": map[string]any{"status": "complete"}, "domTree": initialDOM,
				"frames": []any{
					map[string]any{"frameId": "same-origin", "status": "captured", "domTree": map[string]any{"tagName": "section", "text": "Products"}},
					map[string]any{"frameId": "restricted", "status": "unavailable", "error": "permission_denied"},
				},
				"outerHTML": "<script>unsafe()</script>",
			},
			map[string]any{
				"phase": "before-action", "sequence": 1, "actionIndex": 0, "url": "https://fixture.local/products",
				"capture": map[string]any{"status": "complete"}, "domTree": beforeDOM,
			},
			map[string]any{
				"phase": "final", "sequence": 2, "actionIndex": 1, "url": "https://fixture.local/products?q=books",
				"capture": map[string]any{"status": "complete"}, "domTree": finalDOM,
			},
		},
	}
}

func platformE2EJSON[T any](t *testing.T, method, url, token string, payload any, wantStatus int) T {
	t.Helper()
	var body io.Reader = http.NoBody
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s returned %d, want %d: %s", method, url, response.StatusCode, wantStatus, data)
	}
	var output T
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("decode %s %s response: %v; body=%s", method, url, err, data)
		}
	}
	return output
}

func TestHermeticPlatformLifecycleE2E(t *testing.T) {
	realExecutorReplay := loadPlatformRealExecutorReplayFixture(t)
	invalidExecutorReplay := platformRealExecutorReplayScenario(t, realExecutorReplay, "invalid-output")
	validExecutorReplay := platformRealExecutorReplayScenario(t, realExecutorReplay, "valid-output")
	server, persistence, requirementManager, dslManager, completer := newPlatformE2EServer(t)
	admin := mcpTestAdminToken

	// The built-in extension boundary uploads a recording before any LLM work.
	createdRecording := platformE2EJSON[RecordingResponse](t, http.MethodPost, server.URL+"/api/v1/recordings", admin,
		CreateRecordingRequest{Recording: platformE2ERecording()}, http.StatusCreated)
	if createdRecording.Recording == nil || createdRecording.Recording.ActionCount != 1 || createdRecording.Recording.SnapshotCount != 3 {
		t.Fatalf("unexpected persisted recording metadata: %+v", createdRecording.Recording)
	}
	recordingID := createdRecording.Recording.ID
	storedRecording := platformE2EJSON[RecordingResponse](t, http.MethodGet, server.URL+"/api/v1/recordings/"+recordingID, admin, nil, http.StatusOK)
	sanitizedRecording, _ := json.Marshal(storedRecording.Recording.Payload)
	for _, unsafe := range []string{"raw-password-value", "raw-token-value", "outerHTML", "<script>"} {
		if bytes.Contains(sanitizedRecording, []byte(unsafe)) {
			t.Fatalf("sanitized recording leaked %q: %s", unsafe, sanitizedRecording)
		}
	}
	if !bytes.Contains(sanitizedRecording, []byte("permission_denied")) {
		t.Fatalf("inaccessible iframe marker was lost: %s", sanitizedRecording)
	}

	// The real durable requirement worker invokes the deterministic fake model.
	candidateSubmission := platformE2EJSON[RequirementJobResponse](t, http.MethodPost,
		server.URL+"/api/v1/recordings/"+recordingID+"/requirement-jobs", admin, nil, http.StatusAccepted)
	requirementManager.ProcessOnce(context.Background())
	candidateJob := platformE2EJSON[RequirementJobResponse](t, http.MethodGet,
		server.URL+candidateSubmission.StatusURL, admin, nil, http.StatusOK)
	if candidateJob.Job.Status != models.RequirementJobCompleted || candidateJob.Job.Provider != "hermetic-fake" {
		t.Fatalf("candidate job did not complete through the durable worker: %+v", candidateJob.Job)
	}
	resultJSON, _ := json.Marshal(candidateJob.Job.Result)
	var candidateResult llmrequirement.CandidateResult
	if err := json.Unmarshal(resultJSON, &candidateResult); err != nil {
		t.Fatal(err)
	}
	if len(candidateResult.Candidates) != 3 || candidateResult.Candidates[0].ID != "c1" || !candidateResult.ManualEntryAvailable {
		t.Fatalf("expected exactly three normalized candidates: %+v", candidateResult)
	}

	selected := candidateResult.Candidates[0]
	normalizeRequest := NormalizeRequirementRequest{
		Requirement: &selected.Requirement, CandidateJobID: candidateJob.Job.ID, CandidateID: selected.ID,
	}
	normalizeSubmission := platformE2EJSON[RequirementJobResponse](t, http.MethodPost,
		server.URL+"/api/v1/recordings/"+recordingID+"/requirement-jobs/normalize", admin,
		normalizeRequest, http.StatusAccepted)
	requirementManager.ProcessOnce(context.Background())
	normalizedJob := platformE2EJSON[RequirementJobResponse](t, http.MethodGet,
		server.URL+normalizeSubmission.StatusURL, admin, nil, http.StatusOK)
	if normalizedJob.Job.Status != models.RequirementJobCompleted || normalizedJob.Job.RequirementID == "" {
		t.Fatalf("selected requirement was not normalized durably: %+v", normalizedJob.Job)
	}
	confirmed := platformE2EJSON[CollectionRequirementResponse](t, http.MethodPost,
		server.URL+"/api/v1/requirements/"+normalizedJob.Job.RequirementID+"/confirm", admin, nil, http.StatusOK)
	if confirmed.Requirement.Status != models.CollectionRequirementConfirmed || confirmed.Requirement.Requirement.Title != selected.Requirement.Title {
		t.Fatalf("unexpected confirmed requirement: %+v", confirmed.Requirement)
	}

	// Generate the provisional rule, fail a complete replay, repair it, and then
	// replay successfully before explicit immutable approval.
	workflowSubmission := platformE2EJSON[DSLWorkflowResponse](t, http.MethodPost,
		server.URL+"/api/v1/requirements/"+confirmed.Requirement.ID+"/dsl-workflows", admin,
		CreateDSLWorkflowRequest{BrowserProfileID: "fixture-chrome-profile", BaselineRule: platformE2EBaselineRule()},
		http.StatusAccepted)
	dslManager.ProcessOnce(context.Background())
	workflow := platformE2EJSON[DSLWorkflowResponse](t, http.MethodGet,
		server.URL+workflowSubmission.StatusURL, admin, nil, http.StatusOK)
	if workflow.Workflow.Status != models.DSLWorkflowAwaitingReplay || workflow.Workflow.ProvisionalHash == "" {
		job := platformE2EJSON[DSLJobResponse](t, http.MethodGet,
			server.URL+"/api/v1/dsl-jobs/"+workflow.Workflow.CurrentJobID, admin, nil, http.StatusOK)
		t.Fatalf("provisional DSL was not generated: workflow=%+v job=%+v", workflow.Workflow, job.Job)
	}
	sourceAttempt := platformE2EJSON[DSLProviderAttemptResponse](t, http.MethodGet,
		server.URL+"/api/v1/dsl-jobs/"+workflowSubmission.Job.ID+"/provider-attempts/1", admin, nil, http.StatusOK)
	artifactJSON, _ := json.Marshal(sourceAttempt.Artifact)
	var exportedArtifact llmdsl.DSLAttemptArtifact
	if err := json.Unmarshal(artifactJSON, &exportedArtifact); err != nil {
		t.Fatal(err)
	}
	exportedAttempt := llmdsl.AdminReviewedDSLAttemptExport{
		SchemaVersion: llmdsl.AdminReviewedDSLAttemptExportVersion,
		Attempt:       sourceAttempt.Attempt,
		Artifact:      &exportedArtifact,
	}
	adoptAttempt := func() DSLWorkflowResponse {
		return platformE2EJSON[DSLWorkflowResponse](t, http.MethodPost,
			server.URL+"/api/v1/requirements/"+confirmed.Requirement.ID+"/dsl-workflows/adopt-attempt-export", admin,
			AdoptDSLAttemptExportRequest{
				BrowserProfileID: "fixture-chrome-profile",
				Export:           exportedAttempt,
			}, http.StatusCreated)
	}
	assertAdoptedWorkflow := func(label string, response DSLWorkflowResponse) {
		t.Helper()
		if response.Workflow == nil || response.Job != nil ||
			response.Workflow.Status != models.DSLWorkflowAwaitingReplay || response.Workflow.MaxRepairs != 0 ||
			response.Workflow.RepairCount != 0 || response.Workflow.CurrentJobID != "" ||
			response.Workflow.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
			response.Workflow.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
			response.Workflow.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
			response.Workflow.SourceExportHash == "" {
			t.Fatalf("%s adoption crossed the provider/non-authoritative boundary: workflow=%+v job=%+v",
				label, response.Workflow, response.Job)
		}
		assertPlatformRealExecutorReplayRule(t, &realExecutorReplay.Rule, response.Workflow.ProvisionalRule)
	}
	requestsBeforeAdoption := completer.requestCount()
	failedAdoption := adoptAttempt()
	assertAdoptedWorkflow("failed replay", failedAdoption)
	if completer.requestCount() != requestsBeforeAdoption {
		t.Fatalf("attempt adoption made a provider call: requests=%d/%d", requestsBeforeAdoption, completer.requestCount())
	}
	platformE2EJSON[ErrorResponse](t, http.MethodPost, server.URL+"/admin/tasks", admin,
		map[string]any{
			"ruleId": platformE2EBaselineRule().ID, "ruleVersionNumber": 1,
			"variables": map[string]any{"keyword": "books"}, "maxRetries": 0,
		}, http.StatusBadRequest)
	failedAdoptedReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+failedAdoption.Workflow.ID+"/replays", admin, nil, http.StatusCreated)
	requestsBeforeFailedReplay := completer.requestCount()
	failedAdoptedReplayResult := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+failedAdoption.Workflow.ID+"/replays/"+failedAdoptedReplay.Replay.ID+"/complete", admin,
		invalidExecutorReplay, http.StatusOK)
	failedAdoptedReplayStored := platformE2EJSON[DSLReplayResponse](t, http.MethodGet,
		server.URL+"/api/v1/dsl-replays/"+failedAdoptedReplay.Replay.ID, admin, nil, http.StatusOK)
	failedAdoptionState := platformE2EJSON[DSLWorkflowResponse](t, http.MethodGet,
		server.URL+"/api/v1/dsl-workflows/"+failedAdoption.Workflow.ID, admin, nil, http.StatusOK)
	platformE2EJSON[ErrorResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+failedAdoption.Workflow.ID+"/confirm",
		admin, nil, http.StatusConflict)
	var failedAdoptionJobs int
	if err := persistence.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, failedAdoption.Workflow.ID).Scan(&failedAdoptionJobs); err != nil {
		t.Fatal(err)
	}
	failedReplayDiagnostics, _ := json.Marshal(failedAdoptedReplayStored.Replay.Diagnostics)
	if failedAdoptedReplayResult.Replay.Status != models.ReplayAttemptFailed ||
		failedAdoptedReplayResult.Replay.ErrorCode != "OUTPUT_SCHEMA_INVALID" ||
		!reflect.DeepEqual(failedAdoptedReplayStored.Replay.Output, invalidExecutorReplay.Output) ||
		!bytes.Contains(failedReplayDiagnostics, []byte(`"executor":"production-runRule"`)) ||
		failedAdoptedReplayResult.RepairJob != nil ||
		failedAdoptionState.Workflow.Status != models.DSLWorkflowFailed || failedAdoptionState.Workflow.RepairCount != 0 ||
		failedAdoptionState.Workflow.CurrentJobID != "" || failedAdoptionJobs != 0 ||
		completer.requestCount() != requestsBeforeFailedReplay {
		t.Fatalf("real-executor invalid output bypassed or crossed the zero-repair/provider boundary: replay=%+v output=%#v wantOutput=%#v diagnostics=%s workflow=%+v jobs=%d requests=%d/%d",
			failedAdoptedReplayResult.Replay, failedAdoptedReplayStored.Replay.Output,
			invalidExecutorReplay.Output, failedReplayDiagnostics, failedAdoptionState.Workflow, failedAdoptionJobs,
			requestsBeforeFailedReplay, completer.requestCount())
	}

	firstReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/replays", admin, nil, http.StatusCreated)
	failedReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/replays/"+firstReplay.Replay.ID+"/complete", admin,
		llmdsl.ReplayCompletionInput{
			Succeeded: false, ErrorCode: "ELEMENT_NOT_FOUND", ErrorMessage: "recorded selector no longer matches",
			Diagnostics: map[string]any{"selector": ".old-product-name", "authorization": "Bearer diagnostic-secret"},
		}, http.StatusOK)
	if failedReplay.Replay.Status != models.ReplayAttemptFailed || failedReplay.RepairJob == nil {
		t.Fatalf("failed replay did not enqueue repair: %+v", failedReplay)
	}
	dslManager.ProcessOnce(context.Background())
	repairedWorkflow := platformE2EJSON[DSLWorkflowResponse](t, http.MethodGet,
		server.URL+workflowSubmission.StatusURL, admin, nil, http.StatusOK)
	if repairedWorkflow.Workflow.Status != models.DSLWorkflowAwaitingReplay || repairedWorkflow.Workflow.RepairCount != 1 {
		t.Fatalf("repair job did not produce another provisional rule: %+v", repairedWorkflow.Workflow)
	}

	secondReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/replays", admin, nil, http.StatusCreated)
	successfulReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/replays/"+secondReplay.Replay.ID+"/complete", admin,
		llmdsl.ReplayCompletionInput{
			Succeeded: true, Diagnostics: map[string]any{"message": "fresh-tab replay completed"},
			Output: map[string]any{"name": "Example product", "price": 12.5},
		}, http.StatusOK)
	if successfulReplay.Replay.Status != models.ReplayAttemptSucceeded || !successfulReplay.Replay.OutputValid {
		t.Fatalf("successful replay was not schema-valid: %+v", successfulReplay.Replay)
	}
	generatedApproval := platformE2EJSON[RuleVersionResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/confirm", admin, nil, http.StatusOK)
	if generatedApproval.RuleVersion.Status != models.RuleApprovalApproved || generatedApproval.RuleVersion.Version != 1 ||
		generatedApproval.Contract == nil || generatedApproval.Contract.BrowserProfileID != "fixture-chrome-profile" {
		t.Fatalf("unexpected immutable approval: %+v", generatedApproval)
	}
	generatedApprovalRetry := platformE2EJSON[RuleVersionResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+workflow.Workflow.ID+"/confirm", admin, nil, http.StatusOK)
	if generatedApprovalRetry.RuleVersion.ContentHash != generatedApproval.RuleVersion.ContentHash ||
		generatedApprovalRetry.RuleVersion.Version != generatedApproval.RuleVersion.Version ||
		generatedApprovalRetry.Contract == nil ||
		generatedApprovalRetry.Contract.Version != generatedApproval.Contract.Version {
		t.Fatalf("workflow approval retry changed the immutable approval: first=%+v retry=%+v",
			generatedApproval, generatedApprovalRetry)
	}
	requestsBeforeSuccessfulAdoption := completer.requestCount()
	adopted := adoptAttempt()
	assertAdoptedWorkflow("successful replay", adopted)
	if adopted.Workflow.ID == failedAdoption.Workflow.ID ||
		adopted.Workflow.SourceExportHash != failedAdoption.Workflow.SourceExportHash ||
		completer.requestCount() != requestsBeforeSuccessfulAdoption {
		t.Fatalf("fresh adoption did not retain an independent provider-free identity: failed=%+v fresh=%+v requests=%d/%d",
			failedAdoption.Workflow, adopted.Workflow, requestsBeforeSuccessfulAdoption, completer.requestCount())
	}
	adoptedReplay := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+adopted.Workflow.ID+"/replays", admin, nil, http.StatusCreated)
	adoptedReplayResult := platformE2EJSON[DSLReplayResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+adopted.Workflow.ID+"/replays/"+adoptedReplay.Replay.ID+"/complete", admin,
		validExecutorReplay, http.StatusOK)
	adoptedReplayStored := platformE2EJSON[DSLReplayResponse](t, http.MethodGet,
		server.URL+"/api/v1/dsl-replays/"+adoptedReplay.Replay.ID, admin, nil, http.StatusOK)
	adoptedReplayDiagnostics, _ := json.Marshal(adoptedReplayStored.Replay.Diagnostics)
	if adoptedReplayResult.Replay.Status != models.ReplayAttemptSucceeded ||
		!adoptedReplayResult.Replay.OutputValid ||
		!reflect.DeepEqual(adoptedReplayStored.Replay.Output, validExecutorReplay.Output) ||
		!bytes.Contains(adoptedReplayDiagnostics, []byte(`"executor":"production-runRule"`)) ||
		adoptedReplayResult.RepairJob != nil {
		t.Fatalf("adopted workflow replay did not pass exactly once: %+v", adoptedReplayResult)
	}
	adoptedApproval := platformE2EJSON[RuleVersionResponse](t, http.MethodPost,
		server.URL+"/api/v1/dsl-workflows/"+adopted.Workflow.ID+"/confirm", admin, nil, http.StatusOK)
	adoptedWorkflowState := platformE2EJSON[DSLWorkflowResponse](t, http.MethodGet,
		server.URL+"/api/v1/dsl-workflows/"+adopted.Workflow.ID, admin, nil, http.StatusOK)
	if adoptedApproval.RuleVersion == nil || adoptedApproval.Contract == nil ||
		adoptedApproval.RuleVersion.Status != models.RuleApprovalApproved ||
		adoptedApproval.RuleVersion.Source != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		adoptedApproval.RuleVersion.RuleID != generatedApproval.RuleVersion.RuleID ||
		adoptedApproval.RuleVersion.Version != generatedApproval.RuleVersion.Version+1 ||
		adoptedApproval.RuleVersion.ContentHash == generatedApproval.RuleVersion.ContentHash ||
		adoptedApproval.RuleVersion.Rule == nil || generatedApproval.RuleVersion.Rule == nil ||
		bytes.Equal(adoptedApproval.RuleVersion.Rule.Steps, generatedApproval.RuleVersion.Rule.Steps) ||
		adoptedApproval.Contract.RuleID != adoptedApproval.RuleVersion.RuleID ||
		adoptedApproval.Contract.Version != adoptedApproval.RuleVersion.Version ||
		adoptedApproval.Contract.BrowserProfileID != "fixture-chrome-profile" ||
		adoptedApproval.Contract.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		adoptedApproval.Contract.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		adoptedApproval.Contract.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
		adoptedApproval.Contract.SourceExportHash != adopted.Workflow.SourceExportHash ||
		adoptedApproval.Contract.SourceWorkflowID != adopted.Workflow.ID ||
		adoptedWorkflowState.Workflow.Status != models.DSLWorkflowApproved ||
		adoptedWorkflowState.Workflow.ApprovedRuleID != adoptedApproval.RuleVersion.RuleID ||
		adoptedWorkflowState.Workflow.ApprovedVersion != adoptedApproval.RuleVersion.Version ||
		adoptedWorkflowState.Workflow.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
		adoptedWorkflowState.Workflow.SourceExportHash != adopted.Workflow.SourceExportHash {
		t.Fatalf("adopted workflow lost its distinct immutable version or exact source lineage: generated=%+v adopted=%+v workflow=%+v",
			generatedApproval, adoptedApproval, adoptedWorkflowState.Workflow)
	}
	if completer.requestCount() != 3 {
		t.Fatalf("expected candidate, generation, and repair model calls, got %d", completer.requestCount())
	}

	// Bind a one-time task to the immutable rule contract and execute it through
	// the authenticated worker protocol, including invalid and duplicate batches.
	createdTask := platformE2EJSON[map[string]string](t, http.MethodPost, server.URL+"/admin/tasks", admin,
		map[string]any{
			"ruleId": generatedApproval.RuleVersion.RuleID, "ruleVersionNumber": generatedApproval.RuleVersion.Version,
			"variables": map[string]any{"keyword": "books"},
		}, http.StatusOK)
	taskID := createdTask["taskId"]
	claim := platformE2EJSON[ClaimTaskResponse](t, http.MethodPost, server.URL+"/tasks/claim", platformE2EWorkerToken,
		ClaimTaskRequest{WorkerID: "fixture-worker", BrowserProfileID: "fixture-chrome-profile"}, http.StatusOK)
	if claim.TaskID != taskID || claim.AttemptID == "" || claim.RuleVersionNumber != generatedApproval.RuleVersion.Version ||
		claim.BrowserProfileID != "fixture-chrome-profile" || claim.Rule == nil {
		t.Fatalf("worker did not receive the immutable execution contract: %+v", claim)
	}
	platformE2EJSON[SuccessResponse](t, http.MethodPost, server.URL+"/status", platformE2EWorkerToken,
		StatusRequest{TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID, Status: string(models.TaskStatusRunning)}, http.StatusOK)

	invalid := platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		ResultRequest{
			TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID,
			IdempotencyKey: claim.AttemptID + ":1:batch", Sequence: 1, Kind: string(models.ResultKindBatch),
			Payload: map[string]any{"name": "Wrong type", "price": "not-a-number"},
		}, http.StatusOK)
	if invalid.Valid || invalid.Success {
		t.Fatalf("schema-invalid result was accepted as collection data: %+v", invalid)
	}
	validRequest := ResultRequest{
		TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID,
		IdempotencyKey: claim.AttemptID + ":2:batch", Sequence: 2, Kind: string(models.ResultKindBatch),
		Payload: map[string]any{"name": "Example product", "price": 12.5},
	}
	valid := platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		validRequest, http.StatusOK)
	duplicate := platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		validRequest, http.StatusOK)
	if !valid.Valid || valid.Duplicate || !duplicate.Valid || !duplicate.Duplicate {
		t.Fatalf("result idempotency failed: first=%+v duplicate=%+v", valid, duplicate)
	}
	platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		ResultRequest{
			TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID,
			IdempotencyKey: claim.AttemptID + ":3:summary", Sequence: 3, Kind: string(models.ResultKindSummary),
			Payload: map[string]any{"validRows": 1, "invalidRows": 1},
		}, http.StatusOK)
	platformE2EJSON[ErrorResponse](t, http.MethodPost, server.URL+"/status", platformE2EWorkerToken,
		StatusRequest{TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID, Status: string(models.TaskStatusDone)}, http.StatusConflict)
	platformE2EJSON[SuccessResponse](t, http.MethodPost, server.URL+"/status", platformE2EWorkerToken,
		StatusRequest{TaskID: taskID, WorkerID: "fixture-worker", AttemptID: claim.AttemptID, Status: string(models.TaskStatusFailed)}, http.StatusOK)

	adminResults := platformE2EJSON[TaskResultsResponse](t, http.MethodGet,
		server.URL+"/api/v1/tasks/"+taskID+"/results?include_invalid=true", admin, nil, http.StatusOK)
	if adminResults.RuleVersionNumber != generatedApproval.RuleVersion.Version || adminResults.Page.Total != 1 ||
		len(adminResults.Page.Batches) != 1 || len(adminResults.Page.InvalidBatches) != 1 || adminResults.Page.Summary == nil {
		t.Fatalf("admin result lineage is incomplete: %+v", adminResults)
	}

	// The adopted version also completes one zero-retry execution with only
	// valid collection data and a final summary.
	zeroRetryTask := platformE2EJSON[map[string]string](t, http.MethodPost, server.URL+"/admin/tasks", admin,
		map[string]any{
			"ruleId": adoptedApproval.RuleVersion.RuleID, "ruleVersionNumber": adoptedApproval.RuleVersion.Version,
			"variables": map[string]any{"keyword": "books"}, "maxRetries": 0,
		}, http.StatusOK)
	zeroRetryTaskID := zeroRetryTask["taskId"]
	zeroRetryClaim := platformE2EJSON[ClaimTaskResponse](t, http.MethodPost, server.URL+"/tasks/claim", platformE2EWorkerToken,
		ClaimTaskRequest{WorkerID: "fixture-worker", BrowserProfileID: "fixture-chrome-profile"}, http.StatusOK)
	if zeroRetryClaim.TaskID != zeroRetryTaskID || zeroRetryClaim.RuleID != adoptedApproval.RuleVersion.RuleID ||
		zeroRetryClaim.RuleVersionNumber != adoptedApproval.RuleVersion.Version ||
		zeroRetryClaim.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		zeroRetryClaim.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		zeroRetryClaim.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
		zeroRetryClaim.SourceExportHash != adopted.Workflow.SourceExportHash ||
		zeroRetryClaim.SourceWorkflowID != adopted.Workflow.ID {
		t.Fatalf("worker claimed the wrong zero-retry adopted contract: %+v", zeroRetryClaim)
	}
	platformE2EJSON[SuccessResponse](t, http.MethodPost, server.URL+"/status", platformE2EWorkerToken,
		StatusRequest{TaskID: zeroRetryTaskID, WorkerID: "fixture-worker", AttemptID: zeroRetryClaim.AttemptID, Status: string(models.TaskStatusRunning)}, http.StatusOK)
	zeroRetryBatch := platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		ResultRequest{
			TaskID: zeroRetryTaskID, WorkerID: "fixture-worker", AttemptID: zeroRetryClaim.AttemptID,
			IdempotencyKey: zeroRetryClaim.AttemptID + ":1:batch", Sequence: 1, Kind: string(models.ResultKindBatch),
			Payload: map[string]any{"name": "Example product", "price": 12.5},
		}, http.StatusOK)
	zeroRetrySummary := platformE2EJSON[ResultSubmissionResponse](t, http.MethodPost, server.URL+"/results", platformE2EWorkerToken,
		ResultRequest{
			TaskID: zeroRetryTaskID, WorkerID: "fixture-worker", AttemptID: zeroRetryClaim.AttemptID,
			IdempotencyKey: zeroRetryClaim.AttemptID + ":2:summary", Sequence: 2, Kind: string(models.ResultKindSummary),
			Payload: map[string]any{"validRows": 1, "invalidRows": 0},
		}, http.StatusOK)
	if !zeroRetryBatch.Success || !zeroRetryBatch.Valid || zeroRetryBatch.Duplicate ||
		!zeroRetrySummary.Success || !zeroRetrySummary.Valid || zeroRetrySummary.Duplicate {
		t.Fatalf("adopted task did not accept its valid batch and final summary: batch=%+v summary=%+v",
			zeroRetryBatch, zeroRetrySummary)
	}
	platformE2EJSON[SuccessResponse](t, http.MethodPost, server.URL+"/status", platformE2EWorkerToken,
		StatusRequest{TaskID: zeroRetryTaskID, WorkerID: "fixture-worker", AttemptID: zeroRetryClaim.AttemptID, Status: string(models.TaskStatusDone)}, http.StatusOK)
	zeroRetryTaskState := platformE2EJSON[TaskResponse](t, http.MethodGet,
		server.URL+"/admin/tasks/"+zeroRetryTaskID, admin, nil, http.StatusOK)
	if zeroRetryTaskState.Status != string(models.TaskStatusDone) || zeroRetryTaskState.MaxRetries != 0 ||
		zeroRetryTaskState.RuleID != adoptedApproval.RuleVersion.RuleID ||
		zeroRetryTaskState.RuleVersionNumber != adoptedApproval.RuleVersion.Version ||
		zeroRetryTaskState.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
		zeroRetryTaskState.SourceExportHash != adopted.Workflow.SourceExportHash ||
		zeroRetryTaskState.SourceWorkflowID != adopted.Workflow.ID {
		t.Fatalf("adopted task was not a completed zero-retry execution: %+v", zeroRetryTaskState)
	}
	zeroRetryAdminResults := platformE2EJSON[TaskResultsResponse](t, http.MethodGet,
		server.URL+"/api/v1/tasks/"+zeroRetryTaskID+"/results?include_invalid=true", admin, nil, http.StatusOK)
	if zeroRetryAdminResults.TaskID != zeroRetryTaskID ||
		zeroRetryAdminResults.RuleID != adoptedApproval.RuleVersion.RuleID ||
		zeroRetryAdminResults.RuleVersionNumber != adoptedApproval.RuleVersion.Version ||
		zeroRetryAdminResults.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		zeroRetryAdminResults.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		zeroRetryAdminResults.SourceArtifactHash != sourceAttempt.Attempt.ArtifactHash ||
		zeroRetryAdminResults.SourceExportHash != adopted.Workflow.SourceExportHash ||
		zeroRetryAdminResults.SourceWorkflowID != adopted.Workflow.ID ||
		zeroRetryAdminResults.Page.Total != 1 || len(zeroRetryAdminResults.Page.Batches) != 1 ||
		len(zeroRetryAdminResults.Page.InvalidBatches) != 0 || zeroRetryAdminResults.Page.Summary == nil {
		t.Fatalf("zero-retry adopted task results are incomplete: %+v", zeroRetryAdminResults)
	}

	// A schedule keeps the same immutable inputs and creates a distinct task for
	// each occurrence. Manual triggering avoids wall-clock timing in this test.
	schedule := platformE2EJSON[ScheduleResponse](t, http.MethodPost, server.URL+"/admin/schedules", admin,
		CreateScheduleRequest{
			RuleID: generatedApproval.RuleVersion.RuleID, RuleVersionNumber: generatedApproval.RuleVersion.Version,
			Name: "Hourly fixture collection", Type: string(models.ScheduleTypeCron), Expression: "0 * * * *",
			Timezone: "UTC", Variables: func() *jsonRaw {
				value := jsonRaw(map[string]any{"keyword": "books"})
				return &value
			}(),
		}, http.StatusOK)
	triggered := platformE2EJSON[TriggerScheduleResponse](t, http.MethodPost,
		server.URL+"/admin/schedules/"+schedule.ID+"/trigger", admin, nil, http.StatusOK)
	if triggered.TaskID == "" || triggered.TaskID == taskID {
		t.Fatalf("schedule did not create a distinct occurrence task: %+v", triggered)
	}
	scheduledTask := platformE2EJSON[TaskResponse](t, http.MethodGet,
		server.URL+"/admin/tasks/"+triggered.TaskID, admin, nil, http.StatusOK)
	if scheduledTask.ScheduleID != schedule.ID || scheduledTask.RuleVersionNumber != generatedApproval.RuleVersion.Version {
		t.Fatalf("scheduled occurrence lost immutable lineage: %+v", scheduledTask)
	}

	// Finally, retrieve the same validated result lineage over authenticated MCP.
	mcpToken := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead})
	mcpSession := connectMCPClient(t, server.URL, mcpToken.Token)
	mcpResults, err := mcpSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_task_results", Arguments: map[string]any{"taskId": zeroRetryTaskID, "includeInvalid": true},
	})
	if err != nil || mcpResults.IsError {
		t.Fatalf("MCP result retrieval failed: result=%+v err=%v", mcpResults, err)
	}
	mcpOutput, ok := mcpResults.StructuredContent.(map[string]any)
	mcpBatches, _ := mcpOutput["batches"].([]any)
	mcpInvalid, _ := mcpOutput["invalid"].([]any)
	mcpBatch, batchOK := func() (map[string]any, bool) {
		if len(mcpBatches) != 1 {
			return nil, false
		}
		value, valid := mcpBatches[0].(map[string]any)
		return value, valid
	}()
	mcpSummary, summaryOK := mcpOutput["summary"].(map[string]any)
	var adminBatchPayload, adminSummaryPayload any
	if err := json.Unmarshal(zeroRetryAdminResults.Page.Batches[0].Payload, &adminBatchPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(zeroRetryAdminResults.Page.Summary.Payload, &adminSummaryPayload); err != nil {
		t.Fatal(err)
	}
	if !ok || int(mcpOutput["total"].(float64)) != 1 ||
		mcpOutput["taskId"] != zeroRetryAdminResults.TaskID ||
		mcpOutput["ruleId"] != zeroRetryAdminResults.RuleID ||
		int(mcpOutput["ruleVersion"].(float64)) != adoptedApproval.RuleVersion.Version ||
		mcpOutput["sourceKind"] != zeroRetryAdminResults.SourceKind ||
		mcpOutput["sourceAuthority"] != zeroRetryAdminResults.SourceAuthority ||
		mcpOutput["sourceArtifactHash"] != zeroRetryAdminResults.SourceArtifactHash ||
		mcpOutput["sourceExportHash"] != zeroRetryAdminResults.SourceExportHash ||
		mcpOutput["sourceWorkflowId"] != zeroRetryAdminResults.SourceWorkflowID ||
		len(mcpInvalid) != 0 || !batchOK || !summaryOK ||
		mcpBatch["id"] != zeroRetryAdminResults.Page.Batches[0].ID ||
		mcpSummary["id"] != zeroRetryAdminResults.Page.Summary.ID ||
		!reflect.DeepEqual(mcpBatch["payload"], adminBatchPayload) ||
		!reflect.DeepEqual(mcpSummary["payload"], adminSummaryPayload) {
		t.Fatalf("MCP returned incomplete result lineage: %+v", mcpResults.StructuredContent)
	}
	adoptionAudit, adoptionAuditTotal, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action: "dsl_workflow_adopted_from_attempt_export", ResourceType: "dsl_workflow", ResourceID: adopted.Workflow.ID,
	})
	if err != nil || adoptionAuditTotal != 1 || len(adoptionAudit) != 1 ||
		!bytes.Contains(adoptionAudit[0].Payload, []byte(adopted.Workflow.SourceExportHash)) ||
		!bytes.Contains(adoptionAudit[0].Payload, []byte(models.DSLWorkflowSourceAuthorityNonAuthoritative)) {
		t.Fatalf("adoption audit lost exact non-authoritative source lineage: total=%d logs=%+v err=%v",
			adoptionAuditTotal, adoptionAudit, err)
	}
	approvalAudit, approvalAuditTotal, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow", ResourceID: adopted.Workflow.ID,
	})
	if err != nil || approvalAuditTotal != 1 || len(approvalAudit) != 1 ||
		!bytes.Contains(approvalAudit[0].Payload, []byte(adopted.Workflow.SourceExportHash)) ||
		!bytes.Contains(approvalAudit[0].Payload, []byte(adopted.Workflow.ID)) ||
		!bytes.Contains(approvalAudit[0].Payload, []byte(models.DSLWorkflowSourceAuthorityNonAuthoritative)) {
		t.Fatalf("approval audit lost exact non-authoritative source lineage: total=%d logs=%+v err=%v",
			approvalAuditTotal, approvalAudit, err)
	}

	logs, total, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{ResourceType: "mcp_tool"})
	if err != nil || total != 1 || len(logs) != 1 || logs[0].Action != "mcp.get_task_results" {
		t.Fatalf("MCP read was not audited: total=%d logs=%+v err=%v", total, logs, err)
	}
}
