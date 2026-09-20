// Offline strict-output generation harness: drives Workflow.Generate
// against the real configured provider (LLM_* env) with a synthetic
// Bing-shaped recording and distractor catalog. Used to verify the
// strict schema and prompt contracts without a live browser run.
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
	if baseURL == "" || apiKey == "" || model == "" {
		fmt.Println("LLM_BASE_URL / LLM_API_KEY / LLM_MODEL must be set")
		os.Exit(1)
	}
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "repro", LLMModel: model, LLMMaxInputTokens: 200_000}

	strict := true
	provider, err := providers.Build("repro", config.ProviderConfig{
		Provider:               "openai",
		APIKey:                 apiKey,
		BaseURL:                baseURL,
		Model:                  model,
		Timeout:                120 * time.Second,
		Temperature:            0.2,
		OpenAIStrictToolOutput: &strict,
	})
	if err != nil {
		fmt.Println("provider build:", err)
		os.Exit(1)
	}
	_ = provider
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(provider)

	workflow := dsl.NewDSLWorkflow(cfg, orch)

	requirement := models.CollectionRequirementSpec{
		Title:       "Bing result identity",
		Description: "Report one visible organic Bing result without opening it.",
		RequiredInputs: []models.RequirementInput{
			{Name: "keyword", Type: models.RequirementValueString},
		},
		OutputFields: []models.RequirementOutputField{
			{Name: "title", Type: models.RequirementValueString, Description: "Visible result level-two heading"},
			{Name: "website", Type: models.RequirementValueString, Description: "Visible result citation host"},
		},
		SampleOutput: map[string]any{"title": "Example title", "website": "example.com"},
	}
	baseline := &models.Rule{
		ID: "bing-readonly", Version: "1", Name: "Bing Readonly",
		Domain: models.JSON(`"bing.com"`), Entry: "https://www.bing.com",
		Variables: models.JSON(`{}`),
		Steps:     models.JSON(`[{"action":"type","value":"{{keyword}}","target":{"selector":"#sb_form_q"},"submit":true}]`),
	}
	const catalog = `{
		"version":"selector-catalog-v5",
		"catalogHash":"repro-catalog-hash",
		"candidates":[{
			"rowCandidateId":"r_ad",
			"observedSelector":"#b_results > li.b_ad",
			"fieldCandidates":[{
				"fieldCandidateId":"f_ad_title",
				"observedRelativeSelector":"h2",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		},{
			"rowCandidateId":"r_organic",
			"observedSelector":"#b_results > li.b_algo",
			"fieldCandidates":[{
				"fieldCandidateId":"f_title",
				"observedRelativeSelector":"h2",
				"supportedTypes":["text"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_host",
				"observedRelativeSelector":"cite",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		},{
			"rowCandidateId":"r_related",
			"observedSelector":"#b_context .b_ans",
			"fieldCandidates":[{
				"fieldCandidateId":"f_related_title",
				"observedRelativeSelector":"li",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		}]
	}`
	recording := map[string]any{
		"session": "repro-1",
		"snapshots": []any{
			map[string]any{"phase": "initial", "actionIndex": 0,
				"dom": `<header><a href="/">Bing</a></header><form><input id="sb_form_q" aria-label="Enter your search here - Search suggestions will show as you type"/></form><nav role="navigation"><ul><li>Images</li><li>Videos</li><li>News</li></ul></nav>`},
			map[string]any{"phase": "before", "actionIndex": 1,
				"dom": `<ol id="b_results"><li class="b_ad"><h2><a href="https://adpartner.example/tides-guide">Sponsored: Tide tables for sale</a></h2><div class="b_attribution">Ad · adpartner.example</div></li><li class="b_algo"><h2><a href="https://www.noaa.gov/tides/how-tides-work">How ocean tides work - NOAA</a></h2><p>Tides are caused by gravitational forces of the moon and sun...</p><cite>https://www.noaa.gov › tides</cite></li><li class="b_algo"><h2><a href="https://education.nationalgeographic.org/eclipse">Solar eclipses explained - National Geographic</a></h2><cite>https://education.nationalgeographic.org</cite></li><li class="b_algo"><h2><a href="https://oceanservice.noaa.gov/facts/tides.html">Tides and water levels explained</a></h2><cite>https://oceanservice.noaa.gov</cite></li></ol><section id="b_context"><div class="b_ans"><ul><li>Related: moon phases</li><li>Related: tide charts</li></ul></div></section><footer><nav><ul><li>Privacy</li><li>Terms</li></ul></nav></footer>`},
		},
		"events": []any{
			map[string]any{"action": "type", "value": "how do ocean tides work", "detail": "type keyword into #sb_form_q"},
			map[string]any{"action": "click", "detail": "press Enter to submit search"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, metadata, err := workflow.Generate(ctx, recording, requirement, baseline, func(done, total int) {
		fmt.Printf("[progress] chunk %d/%d\n", done, total)
	}, "", catalog)
	if err != nil {
		fmt.Println("GENERATE ERROR:", err)
		os.Exit(2)
	}
	fmt.Printf("metadata: chunks=%d provider=%s model=%s in=%d out=%d\n",
		metadata.ChunkCount, metadata.Provider, metadata.Model, metadata.InputTokens, metadata.OutputTokens)
	fmt.Println("rule steps:", string(result.Rule.Steps))
	fmt.Println("catalogHash:", result.SelectorCatalogHash)
}
