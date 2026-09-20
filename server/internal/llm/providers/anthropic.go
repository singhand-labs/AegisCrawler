package providers

import (
	"unicode/utf8"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

type AnthropicProvider struct {
	name                          string
	apiKey                        string
	baseURL                       string
	model                         string
	temperature                   float64
	temperatureCompatibilityRetry bool
	client                        *http.Client
	// disableThinking sends an explicit thinking {"type":"disabled"} request
	// flag for reasoning-first models. Default false keeps the provider default.
	disableThinking bool
}

// DisableThinking makes requests carry an explicit thinking-disable flag for
// reasoning-first models.
func (p *AnthropicProvider) DisableThinking() *AnthropicProvider {
	p.disableThinking = true
	return p
}

func NewAnthropicProvider(name, apiKey, baseURL, model string, timeout time.Duration, temperature float64, compatibilityRetry ...bool) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	allowCompatibilityRetry := true
	if len(compatibilityRetry) > 0 {
		allowCompatibilityRetry = compatibilityRetry[0]
	}
	return &AnthropicProvider{
		name:                          name,
		apiKey:                        apiKey,
		baseURL:                       baseURL,
		model:                         model,
		temperature:                   temperature,
		temperatureCompatibilityRetry: allowCompatibilityRetry,
		client:                        &http.Client{Timeout: timeout},
	}
}

func (p *AnthropicProvider) Name() string { return p.name }

type anthropicThinkingConfig struct {
	Type string `json:"type"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
	// Thinking omits the field entirely (provider default) when nil and
	// sends an explicit {"type":"disabled"} when false, for reasoning-first
	// models whose default response starts with thinking blocks.
	Thinking *anthropicThinkingConfig `json:"thinking,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              *int `json:"input_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	resp, err := p.completeOnce(ctx, req)
	if errors.Is(err, llm.ErrCompletionCapture) {
		return nil, err
	}
	if err != nil && req.ExecutionPolicy != llm.CompletionExecutionAtMostOnce &&
		p.temperatureCompatibilityRetry && p.isTemperatureError(err) {
		retryReq := req
		retryReq.Temperature = 1.0
		resp, err = p.completeOnce(ctx, retryReq)
	}
	return resp, err
}

func (p *AnthropicProvider) isTemperatureError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "temperature") && (strings.Contains(lower, "must be 1") || strings.Contains(lower, "only 1"))
}

// InputTokenUpperBound returns a conservative token bound for the exact JSON
// request body used by Complete. The bound counts RUNES, not bytes: every
// tokenizer token covers at least one character, so tokens <= runes is
// provable, and runes <= bytes keeps it conservative for any content. For
// CJK-heavy payloads (3 UTF-8 bytes per character) this is ~3x tighter than
// the historical byte count, lifting the effective input capacity for
// Chinese recordings without sacrificing the upper-bound guarantee.
func (p *AnthropicProvider) InputTokenUpperBound(req llm.CompletionRequest) (int, error) {
	_, payload, err := p.requestPayload(req)
	if err != nil {
		return 0, err
	}
	return utf8.RuneCount(payload), nil
}

func (p *AnthropicProvider) requestPayload(req llm.CompletionRequest) (string, []byte, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	temperature := req.Temperature
	if temperature == 0 && p.temperature != 0 {
		temperature = p.temperature
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		// Preserve the legacy adapter default. Enforced calls always carry the
		// positive policy-bound value and therefore never reach this fallback.
		maxTokens = 4096
	}
	body := anthropicRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		System:      req.System,
		Messages:    []anthropicMessage{{Role: "user", Content: req.User}},
	}
	if p.disableThinking {
		disabled := anthropicThinkingConfig{Type: "disabled"}
		body.Thinking = &disabled
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	return model, payload, nil
}

func (p *AnthropicProvider) completeOnce(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model, payload, err := p.requestPayload(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := p.client
	if req.ExecutionPolicy == llm.CompletionExecutionAtMostOnce {
		strictClient := *p.client
		strictClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &strictClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		safeErr := newProviderResponseError(p.name, 0, "transport_error", err.Error(), err)
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, 0, "transport_error", "", safeErr); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := readBoundedProviderErrorBody(resp.Body)
		provenance := llm.CompletionContentProvenance{
			OriginalBytes:      body.originalBytes,
			OriginalBytesExact: body.originalBytesExact,
			Truncated:          body.truncated,
		}
		if body.err != nil {
			safeErr := newProviderResponseError(p.name, resp.StatusCode, "response_read_error", body.err.Error(), body.err)
			if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "response_read_error", body.content, safeErr, provenance); captureErr != nil {
				return nil, captureErr
			}
			return nil, safeErr
		}
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "http_error", body.content)
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "http_error", body.content, safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}

	responseBody := readBoundedProviderResponseBody(resp.Body)
	provenance := providerBodyProvenance(responseBody)
	if responseBody.overflow {
		safeErr := newProviderResponseTooLargeError(p.name, resp.StatusCode)
		if captureErr := llm.TraceProviderError(
			ctx,
			req,
			p.name,
			model,
			resp.StatusCode,
			"response_too_large",
			providerArtifactBody(responseBody),
			safeErr,
			provenance,
		); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	if responseBody.err != nil {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "response_read_error", responseBody.err.Error(), responseBody.err)
		if captureErr := llm.TraceProviderError(
			ctx,
			req,
			p.name,
			model,
			resp.StatusCode,
			"response_read_error",
			providerArtifactBody(responseBody),
			safeErr,
			provenance,
		); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	var parsed anthropicResponse
	if err := json.Unmarshal(responseBody.content, &parsed); err != nil {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "response_decode_error", err.Error(), err)
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "response_decode_error", string(responseBody.content), safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	if parsed.Error != nil {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "provider_error", parsed.Error.Message)
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "provider_error", string(responseBody.content), safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	if len(parsed.Content) == 0 {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "empty_content", "anthropic empty content")
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "empty_content", string(responseBody.content), safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	unknownUsage := unknownUsageCategories(responseBody.content, map[string]struct{}{
		"input_tokens":                {},
		"cache_read_input_tokens":     {},
		"cache_creation_input_tokens": {},
		"output_tokens":               {},
		"service_tier":                {},
		// Official Anthropic API server-tool usage reporting (for example
		// web_search_requests). It carries no token-billing semantics, so it
		// must not fail the usage contract.
		"server_tool_use": {},
	})
	cacheCreationTokens := intValue(parsed.Usage.CacheCreationInputTokens)
	if cacheCreationTokens != 0 {
		// Cache creation has provider-specific premium pricing that is not
		// representable in the v1 rate vocabulary.
		unknownUsage = append(unknownUsage, "cache_creation_input_tokens")
	}
	// Reasoning-first models prepend thinking blocks; the completion text is
	// the first block of type "text".
	responseText := ""
	for _, block := range parsed.Content {
		if block.Type == "text" && block.Text != "" {
			responseText = block.Text
			break
		}
	}
	if responseText == "" {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "empty_content", "anthropic response contains no text block")
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "empty_content", string(responseBody.content), safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	uncachedInput := intValue(parsed.Usage.InputTokens)
	cachedInput := intValue(parsed.Usage.CacheReadInputTokens)
	totalInput, inputSumOK := safeIntSum(uncachedInput, cachedInput)
	result := &llm.CompletionResponse{
		Content:           responseText,
		InputTokens:       totalInput,
		CachedInputTokens: cachedInput,
		OutputTokens:      intValue(parsed.Usage.OutputTokens),
		UsageMetadata: llm.CompletionUsageMetadata{
			InputTokensPresent:       parsed.Usage.InputTokens != nil,
			OutputTokensPresent:      parsed.Usage.OutputTokens != nil,
			CachedInputTokensPresent: parsed.Usage.CacheReadInputTokens != nil,
			Contradictory:            !inputSumOK,
			UnknownCategories:        unknownUsage,
		},
		ResponseID:   llm.SanitizeCompletionMetadata(parsed.ID),
		FinishReason: llm.SanitizeCompletionMetadata(parsed.StopReason),
	}
	if err := llm.TraceProviderResponse(ctx, req, p.name, model, result, resp.StatusCode); err != nil {
		return nil, err
	}
	return result, nil
}
