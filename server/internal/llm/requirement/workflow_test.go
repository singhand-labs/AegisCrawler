package requirement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

type fakeCompleter struct {
	requests []llm.CompletionRequest
	respond  func(llm.CompletionRequest) (*llm.CompletionResult, error)
}

func (f *fakeCompleter) Complete(_ context.Context, request llm.CompletionRequest) (*llm.CompletionResult, error) {
	f.requests = append(f.requests, request)
	return f.respond(request)
}

func validSpec(title string) models.CollectionRequirementSpec {
	return models.CollectionRequirementSpec{
		Title: title, Description: "Collect visible product information.",
		RequiredInputs: []models.RequirementInput{{Name: "keyword", Type: models.RequirementValueString, Description: "Search keyword"}},
		OptionalInputs: []models.RequirementInput{},
		OutputFields: []models.RequirementOutputField{
			{Name: "name", Type: models.RequirementValueString, Description: "Visible product name"},
			{Name: "price", Type: models.RequirementValueNumber, Description: "Visible product price"},
		},
		SampleOutput: map[string]any{"name": "Example", "price": 10.5},
	}
}

func candidateResponse(t *testing.T) string {
	t.Helper()
	candidates := []models.RequirementCandidate{
		{ID: "ignored", Confidence: 0.9, Requirement: validSpec("Collect matching products")},
		{ID: "ignored", Confidence: 0.8, Requirement: validSpec("Compare visible prices")},
		{ID: "ignored", Confidence: 0.7, Requirement: validSpec("Monitor listed products")},
	}
	data, err := json.Marshal(map[string]any{"candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func unsafeCandidateResponse(t *testing.T) string {
	t.Helper()
	unsafeSpec := validSpec("Collect account data")
	unsafeSpec.OutputFields[0].Name = "password"
	unsafeSpec.SampleOutput = map[string]any{"password": "redacted", "price": 10.5}
	candidates := []models.RequirementCandidate{
		{Confidence: 0.9, Requirement: validSpec("Collect matching products")},
		{Confidence: 0.8, Requirement: validSpec("Compare visible prices")},
		{Confidence: 0.7, Requirement: unsafeSpec},
	}
	data, err := json.Marshal(map[string]any{"candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBuildPromptChunksIncludesEverySnapshotAndEventExactlyOnce(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "phase": "before-action", "html": strings.Repeat("snapshot-zero-marker", 250)},
			map[string]any{"actionIndex": 1, "phase": "after-action", "html": strings.Repeat("snapshot-one-marker", 250)},
		},
		"events": []any{
			map[string]any{"type": "click", "marker": strings.Repeat("event-zero-marker", 250)},
			map[string]any{"type": "input", "marker": strings.Repeat("event-one-marker", 250)},
		},
	}
	chunks, full, err := buildPromptChunks(recording, 6000)
	if err != nil {
		t.Fatal(err)
	}
	if full || len(chunks) < 2 {
		t.Fatalf("expected ordered chunking, full=%v chunks=%d", full, len(chunks))
	}
	joined := ""
	for _, chunk := range chunks {
		joined += chunk.Content
	}
	for _, marker := range []string{"snapshot-zero-marker", "snapshot-one-marker", "event-zero-marker", "event-one-marker"} {
		if count := strings.Count(joined, marker); count != 250 {
			t.Fatalf("marker %s was omitted or duplicated: count=%d", marker, count)
		}
	}
	if chunks[0].FirstItem != "snapshot:0" {
		t.Fatalf("expected timeline ordering to start at the first snapshot, got %s", chunks[0].FirstItem)
	}
}

func TestBuildPromptChunksIncludesUserMarksAsIntentEvidence(t *testing.T) {
	recording := map[string]any{
		"snapshots": []any{map[string]any{"actionIndex": 0, "phase": "before-action", "html": "snapshot"}},
		"events":    []any{map[string]any{"type": "click", "selector": ".price"}},
		"marks": []any{map[string]any{
			"id": "mark-1", "canonicalId": "m_price", "timestamp": float64(3), "role": "field",
			"note": "price field", "actionIndex": float64(0), "snapshotSequence": float64(0),
			"element": map[string]any{"tagName": "span", "selector": ".price", "text": "$10"},
		}},
	}
	chunks, _, err := buildPromptChunks(recording, 200)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, chunk := range chunks {
		joined += chunk.Content
	}
	if !strings.Contains(joined, `"kind":"mark"`) || !strings.Contains(joined, "price field") || !strings.Contains(joined, "m_price") {
		t.Fatalf("mark intent evidence was not included in prompt chunks: %s", joined)
	}
}

func TestGenerateCandidatesAnalyzesAllChunksThenSynthesizesExactlyThree(t *testing.T) {
	responseJSON := candidateResponse(t)
	fake := &fakeCompleter{}
	fake.respond = func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		content := `{"summary":"observed","observedInputs":[],"observedOutputs":[],"safetyFlags":[]}`
		if request.System == candidateSystemPrompt {
			content = responseJSON
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: content, InputTokens: 10, OutputTokens: 5},
			Provider:           "test-provider", Model: "test-model",
		}, nil
	}
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "html": strings.Repeat("first-snapshot", 300)},
			map[string]any{"actionIndex": 1, "html": strings.Repeat("second-snapshot", 300)},
		},
		"events": []any{map[string]any{"type": "click", "selector": strings.Repeat("event-selector", 300)}},
	}
	workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 6000}, fake)
	result, metadata, err := workflow.GenerateCandidates(context.Background(), recording, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 3 || result.Candidates[0].ID != "c1" || !result.ManualEntryAvailable {
		t.Fatalf("unexpected candidates: %+v", result)
	}
	if len(result.ChunkLineage) < 2 || metadata.ChunkCount != len(result.ChunkLineage) {
		t.Fatalf("missing chunk lineage: result=%+v metadata=%+v", result, metadata)
	}
	if len(fake.requests) != len(result.ChunkLineage)+1 {
		t.Fatalf("expected one analysis per chunk plus synthesis, got %d requests", len(fake.requests))
	}
	if metadata.InputTokens != len(fake.requests)*10 || metadata.OutputTokens != len(fake.requests)*5 {
		t.Fatalf("token metadata did not include all calls: %+v", metadata)
	}
}

func TestGenerateCandidatesReportsPerChunkProgress(t *testing.T) {
	responseJSON := candidateResponse(t)
	fake := &fakeCompleter{}
	fake.respond = func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		content := `{"summary":"observed","observedInputs":[],"observedOutputs":[],"safetyFlags":[]}`
		if request.System == candidateSystemPrompt {
			content = responseJSON
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: content, InputTokens: 10, OutputTokens: 5},
			Provider:           "test-provider", Model: "test-model",
		}, nil
	}
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "html": strings.Repeat("first-snapshot", 100)},
			map[string]any{"actionIndex": 1, "html": strings.Repeat("second-snapshot", 100)},
		},
		"events": []any{map[string]any{"type": "click", "selector": strings.Repeat("event-selector", 100)}},
	}
	var events [][2]int
	onProgress := func(completed, total int) { events = append(events, [2]int{completed, total}) }
	workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 100}, fake)
	result, metadata, err := workflow.GenerateCandidates(context.Background(), recording, onProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	total := metadata.ChunkCount
	if total < 2 || len(events) != total+1 {
		t.Fatalf("expected initial plus per-chunk progress, chunks=%d events=%v", total, events)
	}
	for index, event := range events {
		if event[0] != index || event[1] != total {
			t.Fatalf("unexpected progress sequence: %v", events)
		}
	}
	if len(result.ChunkLineage) != total {
		t.Fatalf("unexpected lineage: %+v", result)
	}
}

func TestGenerateCandidatesReportsFullRecordingProgress(t *testing.T) {
	responseJSON := candidateResponse(t)
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: responseJSON, InputTokens: 10, OutputTokens: 5},
			Provider:           "test-provider", Model: "test-model",
		}, nil
	}}
	var events [][2]int
	workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMMaxInputTokens: 10_000}, fake)
	_, _, err := workflow.GenerateCandidates(context.Background(), map[string]any{"events": []any{}}, func(completed, total int) {
		events = append(events, [2]int{completed, total})
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != [2]int{0, 1} || events[1] != [2]int{1, 1} {
		t.Fatalf("full recording should report 0/1 then 1/1, got %v", events)
	}
}

func TestCandidateAndRequirementValidationRejectsAmbiguousOrSensitiveContracts(t *testing.T) {
	response := candidateResponse(t)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		t.Fatal(err)
	}
	candidates := parsed["candidates"].([]any)
	candidates = candidates[:2]
	short, _ := json.Marshal(map[string]any{"candidates": candidates})
	if _, err := ParseCandidates(string(short)); !errors.Is(err, ErrInvalidRequirement) {
		t.Fatalf("expected exact-three validation, got %v", err)
	}

	sensitive := validSpec("Collect account data")
	sensitive.OutputFields[0].Name = "password"
	if err := ValidateSpec(sensitive); !errors.Is(err, ErrUnsafeRequirement) {
		t.Fatalf("expected sensitive-data rejection, got %v", err)
	}

	invalidSample := validSpec("Collect products")
	invalidSample.SampleOutput["price"] = "not-a-number"
	if err := ValidateSpec(invalidSample); !errors.Is(err, ErrInvalidRequirement) {
		t.Fatalf("expected sample type validation, got %v", err)
	}
}

func TestGenerateCandidatesClassifiesUnsafeModelResponseAsRetryableProviderOutput(t *testing.T) {
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: unsafeCandidateResponse(t)},
			Provider:           "test-provider", Model: "test-model",
		}, nil
	}}
	workflow := NewWorkflow(&config.Config{LLMEnabled: true}, fake)
	_, _, err := workflow.GenerateCandidates(context.Background(), map[string]any{"events": []any{}}, nil, "")
	if !errors.Is(err, ErrInvalidProviderOutput) || !errors.Is(err, ErrUnsafeRequirement) {
		t.Fatalf("unsafe model output should retain both classifications, got %v", err)
	}
}

func TestNormalizeCustomReturnsStructuredContract(t *testing.T) {
	spec := validSpec("Collect searched products")
	data, _ := json.Marshal(map[string]any{"requirement": spec})
	fake := &fakeCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		if !strings.Contains(request.User, "products matching a keyword") {
			return nil, fmt.Errorf("custom request missing from prompt")
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: string(data), InputTokens: 20, OutputTokens: 10},
			Provider:           "test-provider", Model: "test-model",
		}, nil
	}}
	recording := map[string]any{
		"snapshots": []any{map[string]any{"html": "first"}, map[string]any{"html": "last"}},
		"events":    []any{map[string]any{"type": "input"}},
	}
	workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000}, fake)
	result, metadata, err := workflow.NormalizeCustom(context.Background(), recording, "products matching a keyword", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Requirement.Title != spec.Title || metadata.Provider != "test-provider" || len(result.ChunkLineage) != 1 {
		t.Fatalf("unexpected normalization: result=%+v metadata=%+v", result, metadata)
	}
}

func TestCandidatePromptDeclaresFieldNameConstraints(t *testing.T) {
	for _, prompt := range []string{candidateUserPrompt("recording", ""), candidateSynthesisPrompt("analyses", "")} {
		if !strings.Contains(prompt, "at most 64 characters") {
			t.Fatalf("candidate prompt omits the field-name length constraint: %q", prompt)
		}
		if !strings.Contains(prompt, "never full page titles") {
			t.Fatalf("candidate prompt omits guidance against page-title field names: %q", prompt)
		}
	}
}

func TestCandidatePromptsRequireEmittedRowCollectionShape(t *testing.T) {
	for name, prompt := range map[string]string{
		"full recording": candidateUserPrompt("recording", ""),
		"synthesis":      candidateSynthesisPrompt("analyses", ""),
	} {
		for _, instruction := range []string{
			"outputFields must describe the fields of one emitted row",
			"Never wrap all collected rows in a top-level array output field",
			"at least one of the three candidates must be a conservative projection",
			"only independently emitted scalar string, number, or boolean fields",
			"omit array/object metadata from that candidate",
			"A value selected or discovered during the recording is not a task input",
			"requiredInputs and optionalInputs must both be empty",
		} {
			if !strings.Contains(prompt, instruction) {
				t.Fatalf("%s prompt omits emitted-row instruction %q: %q", name, instruction, prompt)
			}
		}
	}
}

func TestCandidatePromptsPrependRetryFeedbackOnlyWhenProvided(t *testing.T) {
	feedback := "the provider returned an invalid or unsafe structured requirement; retrying with bounded backoff"
	for _, prompt := range []string{candidateUserPrompt("recording", feedback), candidateSynthesisPrompt("analyses", feedback)} {
		if !strings.HasPrefix(prompt, "A previous attempt returned an invalid or unsafe structure: "+feedback+". Correct the identified problem and return a fully valid structure.") {
			t.Fatalf("retry feedback was not prepended to the candidate prompt: %q", prompt)
		}
	}
	for _, prompt := range []string{candidateUserPrompt("recording", ""), candidateSynthesisPrompt("analyses", ""), candidateUserPrompt("recording", "  ")} {
		if strings.Contains(prompt, "A previous attempt") {
			t.Fatalf("prompt without feedback must stay unchanged: %q", prompt)
		}
	}
}

func TestGenerateCandidatesSendsRetryFeedbackInFullAndSynthesisPrompts(t *testing.T) {
	feedback := "the provider returned an invalid or unsafe structured requirement; retrying with bounded backoff"
	instruction := "A previous attempt returned an invalid or unsafe structure: " + feedback

	t.Run("full recording", func(t *testing.T) {
		fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
			return &llm.CompletionResult{
				CompletionResponse: &llm.CompletionResponse{Content: candidateResponse(t), InputTokens: 10, OutputTokens: 5},
				Provider:           "test-provider", Model: "test-model",
			}, nil
		}}
		workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMMaxInputTokens: 10_000}, fake)
		if _, _, err := workflow.GenerateCandidates(context.Background(), map[string]any{"events": []any{}}, nil, feedback); err != nil {
			t.Fatal(err)
		}
		if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].User, instruction) {
			t.Fatalf("full-path prompt omitted the retry feedback: %+v", fake.requests)
		}
	})

	t.Run("chunked synthesis", func(t *testing.T) {
		fake := &fakeCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
			content := `{"summary":"observed","observedInputs":[],"observedOutputs":[],"safetyFlags":[]}`
			if request.System == candidateSystemPrompt {
				content = candidateResponse(t)
			}
			return &llm.CompletionResult{
				CompletionResponse: &llm.CompletionResponse{Content: content, InputTokens: 10, OutputTokens: 5},
				Provider:           "test-provider", Model: "test-model",
			}, nil
		}}
		recording := map[string]any{
			"snapshots": []any{
				map[string]any{"actionIndex": 0, "html": strings.Repeat("first-snapshot", 100)},
				map[string]any{"actionIndex": 1, "html": strings.Repeat("second-snapshot", 100)},
			},
			"events": []any{map[string]any{"type": "click", "selector": strings.Repeat("event-selector", 100)}},
		}
		workflow := NewWorkflow(&config.Config{LLMEnabled: true, LLMMaxInputTokens: 100}, fake)
		if _, _, err := workflow.GenerateCandidates(context.Background(), recording, nil, feedback); err != nil {
			t.Fatal(err)
		}
		synthesis := fake.requests[len(fake.requests)-1]
		if synthesis.System != candidateSystemPrompt || !strings.Contains(synthesis.User, instruction) {
			t.Fatalf("synthesis prompt omitted the retry feedback: %+v", synthesis)
		}
		for index, request := range fake.requests[:len(fake.requests)-1] {
			if strings.Contains(request.User, instruction) {
				t.Fatalf("chunk analysis prompt %d must not carry retry feedback: %q", index, request.User)
			}
		}
	})
}

func TestNormalizePromptsPrependRetryFeedbackOnlyWhenProvided(t *testing.T) {
	const marker = "A previous attempt returned an invalid or unsafe structure"
	for name, prompt := range map[string]string{
		"user":      normalizeUserPrompt("request", "recording", ""),
		"synthesis": normalizeSynthesisPrompt("request", "analyses", ""),
	} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("%s prompt must not carry retry feedback without a previous failure: %q", name, prompt)
		}
	}
	for name, prompt := range map[string]string{
		"user":      normalizeUserPrompt("request", "recording", "sampleOutput was an array"),
		"synthesis": normalizeSynthesisPrompt("request", "analyses", "sampleOutput was an array"),
	} {
		if !strings.Contains(prompt, marker) || !strings.Contains(prompt, "sampleOutput was an array") {
			t.Fatalf("%s prompt must prepend retry feedback: %q", name, prompt)
		}
	}
}

func TestRequirementSchemaPromptDeclaresSampleOutputShape(t *testing.T) {
	if !strings.Contains(requirementSchemaPrompt, "sampleOutput must be a single JSON object whose keys exactly equal the outputFields names") {
		t.Fatalf("schema prompt omits the sampleOutput shape: %q", requirementSchemaPrompt)
	}
}

func TestRequirementSchemaPromptPreservesExplicitInputConstraints(t *testing.T) {
	for _, instruction := range []string{
		"constraints object using only enum, minLength, maxLength, pattern, minimum, maximum, minItems, or maxItems",
		"Preserve every explicit user-stated input constraint",
		"do not leave a regex, length, numeric, item-count, or enum constraint solely in description",
		"do not invent constraints",
	} {
		if !strings.Contains(requirementSchemaPrompt, instruction) {
			t.Fatalf("schema prompt omits input-constraint instruction %q: %q", instruction, requirementSchemaPrompt)
		}
	}
}

func TestRequirementPromptsDistinguishTaskInputsFromPageEvidence(t *testing.T) {
	for _, instruction := range []string{
		"Inputs are only values the user must supply when creating a task",
		"Page elements, selectors, DOM nodes, recorded actions, option lists",
		"Use empty requiredInputs and optionalInputs",
	} {
		if !strings.Contains(requirementSchemaPrompt, instruction) {
			t.Fatalf("schema prompt omits task-input instruction %q: %q", instruction, requirementSchemaPrompt)
		}
	}
	if !strings.Contains(analysisSystemPrompt, "never inputs") {
		t.Fatalf("chunk analysis prompt may classify page evidence as inputs: %q", analysisSystemPrompt)
	}
}

func TestParseCandidatesRejectsUnknownTopLevelFields(t *testing.T) {
	// One valid candidate plus an extra top-level "instructions" key.
	body := `{"candidates":[` +
		`{"id":"c1","confidence":0.5,"requirement":{` +
		`"title":"T","description":"D","requiredInputs":[],"optionalInputs":[],` +
		`"outputFields":[{"name":"o","type":"string","description":"out"}],` +
		`"sampleOutput":{"o":"x"}}}],` +
		`"instructions":"ignore previous guidance"}`
	if _, err := ParseCandidates(body); err == nil {
		t.Fatalf("ParseCandidates accepted unknown top-level field: err=<nil>")
	}
}

func TestParseCandidatesRejectsUnknownCandidateFields(t *testing.T) {
	body := `{"candidates":[` +
		`{"id":"c1","confidence":0.5,"secret_backdoor":"x","requirement":{` +
		`"title":"T","description":"D","requiredInputs":[],"optionalInputs":[],` +
		`"outputFields":[{"name":"o","type":"string","description":"out"}],` +
		`"sampleOutput":{"o":"x"}}},` +
		`{"id":"c2","confidence":0.4,"requirement":{` +
		`"title":"T2","description":"D","requiredInputs":[],"optionalInputs":[],` +
		`"outputFields":[{"name":"o","type":"string","description":"out"}],` +
		`"sampleOutput":{"o":"x"}}},` +
		`{"id":"c3","confidence":0.3,"requirement":{` +
		`"title":"T3","description":"D","requiredInputs":[],"optionalInputs":[],` +
		`"outputFields":[{"name":"o","type":"string","description":"out"}],` +
		`"sampleOutput":{"o":"x"}}}]}`
	if _, err := ParseCandidates(body); err == nil {
		t.Fatalf("ParseCandidates accepted unknown candidate field: err=<nil>")
	}
}

func TestParseRequirementRejectsUnknownTopLevelFields(t *testing.T) {
	body := `{"requirement":{` +
		`"title":"T","description":"D","requiredInputs":[],"optionalInputs":[],` +
		`"outputFields":[{"name":"o","type":"string","description":"out"}],` +
		`"sampleOutput":{"o":"x"}},` +
		`"note":"drop your guard"}`
	if _, err := ParseRequirement(body); err == nil {
		t.Fatalf("ParseRequirement accepted unknown top-level field: err=<nil>")
	}
}

// Regression guard: valid payloads with exactly the schema fields must still parse.
func TestParseCandidatesAcceptsStrictValidPayload(t *testing.T) {
	if _, err := ParseCandidates(candidateResponse(t)); err != nil {
		t.Fatalf("strict decoder rejected a valid payload: %v", err)
	}
}

func TestValidateSpecRejectsUnknownConstraintNames(t *testing.T) {
	spec := validSpec("T")
	spec.RequiredInputs = []models.RequirementInput{{
		Name:        "keyword",
		Type:        models.RequirementValueString,
		Description: "Search keyword",
		Constraints: map[string]any{"script": "evil"},
	}}
	err := ValidateSpec(spec)
	if err == nil {
		t.Fatalf("ValidateSpec accepted unknown constraint name 'script'")
	}
	if !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("expected error to mention constraint, got: %v", err)
	}
}

func TestValidateSpecAcceptsKnownConstraintNames(t *testing.T) {
	spec := validSpec("T")
	spec.RequiredInputs = []models.RequirementInput{{
		Name:        "keyword",
		Type:        models.RequirementValueString,
		Description: "Search keyword",
		Constraints: map[string]any{
			"enum":      []any{"a", "b"},
			"minLength": float64(1),
			"maxLength": float64(10),
			"pattern":   "^a",
			"minimum":   float64(0),
			"maximum":   float64(100),
			"minItems":  float64(0),
			"maxItems":  float64(5),
		},
	}}
	// `minimum`/`maximum`/`minItems`/`maxItems` don't apply to strings but
	// ValidateSpec's allowlist pass only checks NAMES, not value-type
	// applicability — that's enforced later by valueSatisfiesConstraints
	// against an actual value (handled in Task 4 against Default).
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec rejected known constraint names: %v", err)
	}
}

func TestValidateSpecRejectsDefaultThatViolatesConstraints(t *testing.T) {
	spec := validSpec("T")
	spec.RequiredInputs = []models.RequirementInput{{
		Name:        "keyword",
		Type:        models.RequirementValueString,
		Description: "Search keyword",
		Default:     "hi",
		Constraints: map[string]any{"minLength": float64(10)},
	}}
	if err := ValidateSpec(spec); err == nil {
		t.Fatalf("ValidateSpec accepted default 'hi' under minLength=10")
	}
}

func TestValidateSpecAcceptsDefaultThatSatisfiesConstraints(t *testing.T) {
	spec := validSpec("T")
	spec.RequiredInputs = []models.RequirementInput{}
	spec.OptionalInputs = []models.RequirementInput{{
		Name:        "keyword",
		Type:        models.RequirementValueString,
		Description: "Search keyword",
		Default:     "hello world",
		Constraints: map[string]any{"minLength": float64(5), "maxLength": float64(140)},
	}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec rejected valid default: %v", err)
	}
}

func TestValidateSpecAcceptsMissingDefault(t *testing.T) {
	// No Default + Constraints present: nothing to check, must pass.
	spec := validSpec("T")
	spec.RequiredInputs = []models.RequirementInput{}
	spec.OptionalInputs = []models.RequirementInput{{
		Name:        "keyword",
		Type:        models.RequirementValueString,
		Description: "Search keyword",
		Constraints: map[string]any{"minLength": float64(5)},
	}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec rejected constraints-without-default: %v", err)
	}
}

func TestBuildPromptChunksRedactsSensitiveRecordingContent(t *testing.T) {
	// End-to-end pattern-based leak guard: recording carries an email under a
	// sensitive field name (value) and a credit-card-number-shaped value inside
	// outerHTML. Both are caught by redactString's regex patterns. The
	// TestBuildPromptChunksSensitiveFieldNameWithoutRegexIsNotRedacted test
	// below documents that the isSensitiveField branch alone (without a regex
	// match) does NOT blanket-replace — a known coverage gap.
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"events": []any{
			map[string]any{
				"type":  "input",
				"value": "user@example.com",
			},
		},
		"snapshots": []any{
			map[string]any{
				"phase":     "before-action",
				"outerHTML": "<p>Card 4111111111111111 on file</p>",
			},
		},
	}
	chunks, _, err := buildPromptChunks(recording, 96_000)
	if err != nil {
		t.Fatalf("buildPromptChunks: %v", err)
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if strings.Contains(joined, "user@example.com") {
		t.Errorf("email leaked into prompt chunk: %s", joined)
	}
	if strings.Contains(joined, "4111111111111111") {
		t.Errorf("card number leaked into prompt chunk: %s", joined)
	}
	if !strings.Contains(joined, "[REDACTED]") {
		t.Errorf("expected [REDACTED] markers in redacted prompt chunk, got: %s", joined)
	}
}

// Regression guard: buildPromptChunks' existing behavior (markers preserved
// exactly once) must continue to hold after redaction is added, because the
// test fixture markers are not sensitive and should pass through unchanged.
func TestBuildPromptChunksPreservesNonSensitiveMarkers(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "phase": "before-action", "html": strings.Repeat("snapshot-zero-marker", 20)},
		},
		"events": []any{
			map[string]any{"type": "click", "marker": strings.Repeat("event-zero-marker", 20)},
		},
	}
	chunks, full, err := buildPromptChunks(recording, 80)
	if err != nil {
		t.Fatalf("buildPromptChunks: %v", err)
	}
	if full {
		t.Fatalf("expected chunked output, got full payload")
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if c := strings.Count(joined, "snapshot-zero-marker"); c != 20 {
		t.Errorf("snapshot marker count drift: %d", c)
	}
	if c := strings.Count(joined, "event-zero-marker"); c != 20 {
		t.Errorf("event marker count drift: %d", c)
	}
}

// TestBuildPromptChunksSensitiveFieldNameWithoutRegexIsNotRedacted documents
// the isSensitiveField code path's actual behavior in redact.Recording. When
// the value under a sensitive key (value/text/outerHTML/innerText/textContent/
// innerHTML) is a plain string that matches NO regex (no email, no card number,
// no credential pattern), redact.Recording does NOT blanket-replace it — the
// isSensitiveField branch routes strings through redactString, which is regex-
// only. The value passes through unchanged.
//
// This is a known coverage gap: PII that is not pattern-shaped (e.g. a plain
// customer note typed into an input) survives redaction. Closing it would
// require blanket-replacing all sensitive-named string values, which would
// destroy DOM text the LLM needs for selector inference. The gap is documented
// in the WI-5 audit log (see plan file) rather than closed in this batch.
//
// The test asserts the actual behavior so any future change to the
// isSensitiveField branch (e.g. adding blanket replacement) is surfaced here.
func TestBuildPromptChunksSensitiveFieldNameWithoutRegexIsNotRedacted(t *testing.T) {
	const harmlessNote = "plain-customer-note-without-patterns"
	recording := map[string]any{
		"meta": map[string]any{"url": "https://example.test"},
		"events": []any{
			map[string]any{
				"type":  "input",
				"value": harmlessNote,
			},
		},
	}
	chunks, _, err := buildPromptChunks(recording, 96_000)
	if err != nil {
		t.Fatalf("buildPromptChunks: %v", err)
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, harmlessNote) {
		t.Errorf("plain non-pattern string under sensitive key 'value' was redacted; "+
			"if isSensitiveField now blanket-replaces, update this test and the WI-5 audit log: %s", joined)
	}
}
