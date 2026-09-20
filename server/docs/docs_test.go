package docs

import (
	"encoding/json"
	"testing"
)

func TestDocsPackageInitializes(t *testing.T) {
	if SwaggerInfo == nil {
		t.Fatal("SwaggerInfo should be initialized")
	}
	if SwaggerInfo.InstanceName() != "swagger" {
		t.Fatalf("expected instance name swagger, got %s", SwaggerInfo.InstanceName())
	}
}

func TestRequirementWorkflowPathsAreDocumented(t *testing.T) {
	var document struct {
		Paths       map[string]json.RawMessage            `json:"paths"`
		Definitions map[string]map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("parse generated swagger: %v", err)
	}

	for _, path := range []string{
		"/admin/llm/policy",
		"/admin/llm/budget",
		"/api/v1/recordings/{id}/requirement-jobs",
		"/api/v1/recordings/{id}/requirement-jobs/normalize",
		"/api/v1/requirement-jobs/{id}",
		"/api/v1/requirement-jobs/{id}/provider-attempts",
		"/api/v1/requirement-jobs/{id}/provider-attempts/{attempt}",
		"/api/v1/requirement-jobs/{id}/provider-attempts/{attempt}/calls/{callId}/content",
		"/api/v1/requirement-jobs/{id}/retry",
		"/api/v1/requirements/{id}",
		"/api/v1/requirements/{id}/confirm",
		"/api/v1/requirements/{id}/dsl-workflows",
		"/api/v1/requirements/{id}/dsl-workflows/adopt-attempt-export",
		"/api/v1/dsl-workflows/{id}",
		"/api/v1/dsl-workflows/{id}/provisional",
		"/api/v1/dsl-jobs/{id}",
		"/api/v1/dsl-workflows/{id}/replays",
		"/api/v1/dsl-replays/{id}",
		"/api/v1/dsl-workflows/{id}/replays/{replayId}/complete",
		"/api/v1/dsl-workflows/{id}/confirm",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Errorf("generated swagger is missing %s", path)
		}
	}

	for _, definition := range []string{
		"api.ClaimTaskResponse",
		"api.TaskResponse",
		"api.TaskResultsResponse",
		"models.RuleVersionContract",
	} {
		raw, ok := document.Definitions[definition]["properties"]
		if !ok {
			t.Errorf("generated swagger is missing properties for %s", definition)
			continue
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(raw, &properties); err != nil {
			t.Errorf("decode generated swagger properties for %s: %v", definition, err)
			continue
		}
		for _, property := range []string{
			"sourceKind",
			"sourceAuthority",
			"sourceArtifactHash",
			"sourceExportHash",
			"sourceWorkflowId",
		} {
			if _, ok := properties[property]; !ok {
				t.Errorf("generated swagger definition %s is missing %s", definition, property)
			}
		}
	}
}

func TestLLMHardBudgetContractsAreDocumented(t *testing.T) {
	var document struct {
		Paths       map[string]json.RawMessage            `json:"paths"`
		Definitions map[string]map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("parse generated swagger: %v", err)
	}

	for _, path := range []string{
		"/admin/rules/predict-intent",
		"/admin/rules/generate-from-intent",
	} {
		var operations map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		}
		if err := json.Unmarshal(document.Paths[path], &operations); err != nil {
			t.Fatalf("decode swagger path %s: %v", path, err)
		}
		for _, status := range []string{"429", "503"} {
			if _, ok := operations["post"].Responses[status]; !ok {
				t.Errorf("generated swagger path %s is missing response %s", path, status)
			}
		}
	}

	wantProperties := map[string][]string{
		"api.LLMPolicyResponse": {
			"mode", "enabled", "productionEligible", "fingerprint", "primary",
			"attemptLimits", "fallbackTaxonomy", "budgetLedgerBound", "reconciled",
		},
		"api.LLMRouteResponse": {
			"outputCapDialect",
		},
		"api.LLMBudgetResponse": {
			"budgetDay", "global", "workspace",
		},
		"models.LLMAttemptReport": {
			"policyFingerprint",
		},
		"models.LLMProviderCall": {
			"dispatch",
		},
		"models.LLMDispatchLineage": {
			"operationKind", "operationId", "logicalAttempt", "physicalOrdinal",
			"routeSlot", "policyFingerprint", "priceRevision",
		},
	}
	for definition, wanted := range wantProperties {
		raw, ok := document.Definitions[definition]["properties"]
		if !ok {
			t.Errorf("generated swagger is missing properties for %s", definition)
			continue
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(raw, &properties); err != nil {
			t.Errorf("decode generated swagger properties for %s: %v", definition, err)
			continue
		}
		for _, property := range wanted {
			if _, ok := properties[property]; !ok {
				t.Errorf("generated swagger definition %s is missing %s", definition, property)
			}
		}
	}
}
