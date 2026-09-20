// Offline strict-output generation harness: drives Workflow.Generate
// against the real configured provider (LLM_* env) with a synthetic
// hockey standings recording, verifying field-candidate mapping
// (e.g. goals_for/goals_against) without a live browser run.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/llm/providers"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"go.uber.org/zap"
)

func main() {
	baseURL := os.Getenv("LLM_BASE_URL")
	apiKey := os.Getenv("LLM_API_KEY")
	model := os.Getenv("LLM_MODEL")
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "repro", LLMModel: model, LLMMaxInputTokens: 200_000}

	strict := true
	provider, err := providers.Build("repro", config.ProviderConfig{
		Provider: "openai", APIKey: apiKey, BaseURL: baseURL, Model: model,
		Timeout: 120 * time.Second, Temperature: 0.2, OpenAIStrictToolOutput: &strict,
	})
	if err != nil {
		fmt.Println("provider build:", err)
		os.Exit(1)
	}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(provider)
	workflow := dsl.NewDSLWorkflow(cfg, orch)

	requirement := models.CollectionRequirementSpec{
		Title:       "Hockey team standings",
		Description: "Collect visible hockey team standings rows from the search-results table.",
		RequiredInputs: []models.RequirementInput{
			{Name: "team_name", Type: models.RequirementValueString},
		},
		OutputFields: []models.RequirementOutputField{
			{Name: "team", Type: models.RequirementValueString, Description: "Visible team name cell"},
			{Name: "year", Type: models.RequirementValueString, Description: "Visible season year cell"},
			{Name: "wins", Type: models.RequirementValueString, Description: "Visible wins cell labeled W"},
			{Name: "goals_for", Type: models.RequirementValueString, Description: "Visible goals-for cell labeled GF"},
			{Name: "goals_against", Type: models.RequirementValueString, Description: "Visible goals-against cell labeled GA"},
		},
		SampleOutput: map[string]any{"team": "New York", "year": "2024", "wins": "1", "goals_for": "4", "goals_against": "3"},
	}
	baseline := &models.Rule{
		ID: "hockey", Version: "1", Name: "Hockey",
		Domain: models.JSON(`"scrapethissite.com"`), Entry: "https://scrapethissite.com/pages/tables/",
		Variables: models.JSON(`{}`),
		Steps: models.JSON(`[{"action":"type","value":"{{team_name}}","target":{"selector":"#search-input"},"submit":true}]`),
	}
	const catalog = `{
		"version":"selector-catalog-v5",
		"catalogHash":"repro-hockey-hash",
		"candidates":[{
			"rowCandidateId":"r_row",
			"observedSelector":"table.table tbody tr",
			"fieldCandidates":[
				{"fieldCandidateId":"f_team","observedRelativeSelector":"td.team","supportedTypes":["text"],"nonEmptyText":true},
				{"fieldCandidateId":"f_year","observedRelativeSelector":"td.year","supportedTypes":["text"],"nonEmptyText":true},
				{"fieldCandidateId":"f_wins","observedRelativeSelector":"td.wins","supportedTypes":["text"],"nonEmptyText":true},
				{"fieldCandidateId":"f_gf","observedRelativeSelector":"td.gf","supportedTypes":["text"],"nonEmptyText":true},
				{"fieldCandidateId":"f_ga","observedRelativeSelector":"td.ga","supportedTypes":["text"],"nonEmptyText":true}
			]
		}]
	}`
	recording := map[string]any{
		"session": "repro-hockey",
		"snapshots": []any{
			map[string]any{"phase": "initial", "actionIndex": 0,
				"dom": `<input id="search-input" type="text" class="form-control" placeholder="Type to search teams"/>`},
			map[string]any{"phase": "before", "actionIndex": 1,
				"dom": `<h2 class="title">Hockey Teams</h2><table class="table"><thead><tr><th class="team">Team Name</th><th class="year">Year</th><th class="wins">W</th><th class="gf">GF</th><th class="ga">GA</th></tr></thead><tbody><tr><td class="team">Boston Bruins</td><td class="year">1988</td><td class="wins">44</td><td class="gf">312</td><td class="ga">238</td></tr><tr><td class="team">New York Rangers</td><td class="year">1994</td><td class="wins">52</td><td class="gf">297</td><td class="ga">226</td></tr></tbody></table>`},
		},
		"events": []any{
			map[string]any{"action": "type", "value": "New York", "detail": "type team name into #search-input"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, _, err := workflow.Generate(ctx, recording, requirement, baseline, func(done, total int) {}, "", catalog)
	if err != nil {
		fmt.Println("GENERATE ERROR:", err)
		os.Exit(2)
	}
	fmt.Println(string(result.Rule.Steps))
}
