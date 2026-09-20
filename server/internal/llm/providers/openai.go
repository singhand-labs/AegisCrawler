package providers

import (
	"unicode/utf8"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

type OpenAIProvider struct {
	name                          string
	apiKey                        string
	baseURL                       string
	model                         string
	temperature                   float64
	enableThinking                *bool
	outputCapDialect              config.OutputCapDialect
	strictToolOutput              bool
	deepSeekBetaDialect           bool
	temperatureCompatibilityRetry bool
	client                        *http.Client
}

func NewOpenAIProvider(name, apiKey, baseURL, model string, timeout time.Duration, temperature float64, compatibilityRetry ...bool) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	allowCompatibilityRetry := true
	if len(compatibilityRetry) > 0 {
		allowCompatibilityRetry = compatibilityRetry[0]
	}
	return &OpenAIProvider{
		name:                          name,
		apiKey:                        apiKey,
		baseURL:                       baseURL,
		model:                         model,
		temperature:                   temperature,
		deepSeekBetaDialect:           isDeepSeekBetaBaseURL(baseURL),
		temperatureCompatibilityRetry: allowCompatibilityRetry,
		client:                        &http.Client{Timeout: timeout},
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model               string            `json:"model"`
	Messages            []openAIMessage   `json:"messages"`
	Temperature         float64           `json:"temperature"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	ResponseFormat      *responseFormat   `json:"response_format,omitempty"`
	EnableThinking      *bool             `json:"enable_thinking,omitempty"`
	Thinking            *openAIThinking   `json:"thinking,omitempty"`
	Tools               []openAITool      `json:"tools,omitempty"`
	ToolChoice          *openAIToolChoice `json:"tool_choice,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type openAIToolChoice struct {
	Type     string                   `json:"type"`
	Function openAIToolChoiceFunction `json:"function"`
}

type openAIToolChoiceFunction struct {
	Name string `json:"name"`
}

type openAIPromptTokenDetails struct {
	CachedTokens *int `json:"cached_tokens"`
	AudioTokens  *int `json:"audio_tokens"`
}

type openAICompletionTokenDetails struct {
	ReasoningTokens          *int `json:"reasoning_tokens"`
	AudioTokens              *int `json:"audio_tokens"`
	AcceptedPredictionTokens *int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens *int `json:"rejected_prediction_tokens"`
}

type openAIUsage struct {
	PromptTokens           *int                          `json:"prompt_tokens"`
	CompletionTokens       *int                          `json:"completion_tokens"`
	TotalTokens            *int                          `json:"total_tokens"`
	PromptCacheHitTokens   *int                          `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens  *int                          `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails    *openAIPromptTokenDetails     `json:"prompt_tokens_details"`
	CompletionTokenDetails *openAICompletionTokenDetails `json:"completion_tokens_details"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAIProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	resp, err := p.completeOnce(ctx, req)
	if errors.Is(err, llm.ErrCompletionCapture) {
		return nil, err
	}
	if err != nil && req.ExecutionPolicy != llm.CompletionExecutionAtMostOnce &&
		p.temperatureCompatibilityRetry && p.isTemperatureError(err) {
		// Some reasoning models (e.g. kimi-for-coding-highspeed) only accept
		// temperature=1. Retry once with that value before giving up.
		retryReq := req
		retryReq.Temperature = 1.0
		resp, err = p.completeOnce(ctx, retryReq)
	}
	return resp, err
}

func (p *OpenAIProvider) isTemperatureError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsFold(msg, "invalid temperature") || containsFold(msg, "temperature") && containsFold(msg, "only 1")
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// InputTokenUpperBound returns a conservative token bound for the exact JSON
// request body used by Complete. The bound counts RUNES, not bytes: every
// tokenizer token covers at least one character, so tokens <= runes is
// provable, and runes <= bytes keeps it conservative for any content. For
// CJK-heavy payloads (3 UTF-8 bytes per character) this is ~3x tighter than
// the historical byte count, lifting the effective input capacity for
// Chinese recordings without sacrificing the upper-bound guarantee. The body includes the full message envelope,
// structured-output schema, and adapter-specific thinking fields.
func (p *OpenAIProvider) InputTokenUpperBound(req llm.CompletionRequest) (int, error) {
	_, payload, err := p.requestPayload(req)
	if err != nil {
		return 0, err
	}
	return utf8.RuneCount(payload), nil
}

func (p *OpenAIProvider) requestPayload(req llm.CompletionRequest) (string, []byte, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	temperature := req.Temperature
	if temperature == 0 && p.temperature != 0 {
		temperature = p.temperature
	}
	body := openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Temperature: temperature,
	}
	if req.MaxOutputTokens > 0 {
		switch p.outputCapDialect {
		case "", config.OutputCapDialectMaxTokens:
			body.MaxTokens = req.MaxOutputTokens
		case config.OutputCapDialectMaxCompletionTokens:
			if req.MaxOutputTokens <= config.AliyunMaxCompletionTokensTolerance {
				return "", nil, fmt.Errorf(
					"max output tokens must be greater than %d for %s",
					config.AliyunMaxCompletionTokensTolerance,
					config.OutputCapDialectMaxCompletionTokens,
				)
			}
			body.MaxCompletionTokens = req.MaxOutputTokens - config.AliyunMaxCompletionTokensTolerance
		default:
			return "", nil, fmt.Errorf("unsupported output cap dialect %q", p.outputCapDialect)
		}
	}
	if p.enableThinking != nil && isDeepSeekV4Model(model) && p.deepSeekBetaDialect {
		mode := "disabled"
		if *p.enableThinking {
			mode = "enabled"
		}
		body.Thinking = &openAIThinking{Type: mode}
	} else {
		body.EnableThinking = p.enableThinking
	}
	if p.strictToolOutput && req.StructuredOutput != nil {
		if err := validateStructuredOutputDefinition(req.StructuredOutput); err != nil {
			return "", nil, err
		}
		parameters, err := structuredOutputSchemaForModel(
			model,
			req.StructuredOutput.Schema,
			p.deepSeekBetaDialect,
		)
		if err != nil {
			return "", nil, err
		}
		body.Tools = []openAITool{{
			Type: "function",
			Function: openAIToolFunction{
				Name:        req.StructuredOutput.Name,
				Description: req.StructuredOutput.Description,
				Parameters:  parameters,
				Strict:      true,
			},
		}}
		body.ToolChoice = &openAIToolChoice{
			Type:     "function",
			Function: openAIToolChoiceFunction{Name: req.StructuredOutput.Name},
		}
	} else if req.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	return model, payload, nil
}

func (p *OpenAIProvider) completeOnce(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model, payload, err := p.requestPayload(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

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
	var parsed openAIResponse
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
	if len(parsed.Choices) == 0 {
		safeErr := newProviderResponseError(p.name, resp.StatusCode, "empty_choices", "openai empty choices")
		if captureErr := llm.TraceProviderError(ctx, req, p.name, model, resp.StatusCode, "empty_choices", string(responseBody.content), safeErr, provenance); captureErr != nil {
			return nil, captureErr
		}
		return nil, safeErr
	}
	content := parsed.Choices[0].Message.Content
	if p.strictToolOutput && req.StructuredOutput != nil {
		toolCalls := parsed.Choices[0].Message.ToolCalls
		if len(toolCalls) != 1 {
			return p.structuredOutputResponseError(
				ctx, req, model, resp.StatusCode, responseBody, provenance,
				"structured_output_call_count",
				fmt.Sprintf("strict structured output returned %d tool calls, want exactly 1", len(toolCalls)),
			)
		}
		call := toolCalls[0]
		if call.Type != "function" || call.Function.Name != req.StructuredOutput.Name {
			return p.structuredOutputResponseError(
				ctx, req, model, resp.StatusCode, responseBody, provenance,
				"structured_output_wrong_tool",
				fmt.Sprintf("strict structured output returned unexpected tool %q", call.Function.Name),
			)
		}
		if err := validateStructuredOutputArguments(req.StructuredOutput.Schema, call.Function.Arguments); err != nil {
			return p.structuredOutputResponseError(
				ctx, req, model, resp.StatusCode, responseBody, provenance,
				"structured_output_invalid_arguments",
				err.Error(),
			)
		}
		content = call.Function.Arguments
	}
	unknownUsage := unknownUsageCategories(responseBody.content, map[string]struct{}{
		"prompt_tokens":             {},
		"completion_tokens":         {},
		"total_tokens":              {},
		"prompt_tokens_details":     {},
		"completion_tokens_details": {},
		// These OpenAI-compatible fields partition prompt_tokens. We still
		// charge the total at the conservative uncached rate.
		"prompt_cache_hit_tokens":  {},
		"prompt_cache_miss_tokens": {},
	})
	unknownUsage = append(unknownUsage, unknownNestedUsageCategories(
		responseBody.content,
		"prompt_tokens_details",
		map[string]struct{}{
			"cached_tokens": {},
			"audio_tokens":  {},
		},
	)...)
	unknownUsage = append(unknownUsage, unknownNestedUsageCategories(
		responseBody.content,
		"completion_tokens_details",
		map[string]struct{}{
			"reasoning_tokens":           {},
			"audio_tokens":               {},
			"accepted_prediction_tokens": {},
			"rejected_prediction_tokens": {},
		},
	)...)
	unknownUsage = append(unknownUsage, openAIUnpricedUsageCategories(parsed.Usage)...)
	sort.Strings(unknownUsage)
	result := &llm.CompletionResponse{
		Content:      content,
		InputTokens:  intValue(parsed.Usage.PromptTokens),
		OutputTokens: intValue(parsed.Usage.CompletionTokens),
		UsageMetadata: llm.CompletionUsageMetadata{
			InputTokensPresent:  parsed.Usage.PromptTokens != nil,
			OutputTokensPresent: parsed.Usage.CompletionTokens != nil,
			Contradictory:       openAIUsageContradictory(parsed.Usage),
			UnknownCategories:   unknownUsage,
		},
		ResponseID:   llm.SanitizeCompletionMetadata(parsed.ID),
		FinishReason: llm.SanitizeCompletionMetadata(parsed.Choices[0].FinishReason),
	}
	if err := llm.TraceProviderResponse(ctx, req, p.name, model, result, resp.StatusCode); err != nil {
		return nil, err
	}
	return result, nil
}

func openAIUsageContradictory(usage openAIUsage) bool {
	values := []*int{
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		usage.PromptCacheHitTokens,
		usage.PromptCacheMissTokens,
	}
	if usage.PromptTokensDetails != nil {
		values = append(
			values,
			usage.PromptTokensDetails.CachedTokens,
			usage.PromptTokensDetails.AudioTokens,
		)
	}
	if usage.CompletionTokenDetails != nil {
		values = append(
			values,
			usage.CompletionTokenDetails.ReasoningTokens,
			usage.CompletionTokenDetails.AudioTokens,
			usage.CompletionTokenDetails.AcceptedPredictionTokens,
			usage.CompletionTokenDetails.RejectedPredictionTokens,
		)
	}
	for _, value := range values {
		if value != nil && *value < 0 {
			return true
		}
	}

	if usage.PromptTokens != nil &&
		usage.CompletionTokens != nil &&
		usage.TotalTokens != nil {
		total, ok := safeIntSum(*usage.PromptTokens, *usage.CompletionTokens)
		if !ok || total != *usage.TotalTokens {
			return true
		}
	}

	if usage.PromptTokens != nil {
		promptTokens := *usage.PromptTokens
		for _, partition := range []*int{
			usage.PromptCacheHitTokens,
			usage.PromptCacheMissTokens,
		} {
			if partition != nil && *partition > promptTokens {
				return true
			}
		}
		if usage.PromptTokensDetails != nil &&
			usage.PromptTokensDetails.CachedTokens != nil &&
			*usage.PromptTokensDetails.CachedTokens > promptTokens {
			return true
		}
		if usage.PromptTokensDetails != nil &&
			usage.PromptTokensDetails.AudioTokens != nil &&
			*usage.PromptTokensDetails.AudioTokens > promptTokens {
			return true
		}
		if usage.PromptCacheHitTokens != nil && usage.PromptCacheMissTokens != nil {
			partitionTotal, ok := safeIntSum(
				*usage.PromptCacheHitTokens,
				*usage.PromptCacheMissTokens,
			)
			if !ok || partitionTotal != promptTokens {
				return true
			}
		}
		if usage.PromptTokensDetails != nil &&
			usage.PromptTokensDetails.CachedTokens != nil &&
			usage.PromptCacheMissTokens != nil {
			partitionTotal, ok := safeIntSum(
				*usage.PromptTokensDetails.CachedTokens,
				*usage.PromptCacheMissTokens,
			)
			if !ok || partitionTotal != promptTokens {
				return true
			}
		}
	}

	if usage.PromptTokensDetails != nil &&
		usage.PromptTokensDetails.CachedTokens != nil &&
		usage.PromptCacheHitTokens != nil &&
		*usage.PromptTokensDetails.CachedTokens != *usage.PromptCacheHitTokens {
		return true
	}

	if usage.CompletionTokens != nil && usage.CompletionTokenDetails != nil {
		completionTokens := *usage.CompletionTokens
		for _, partition := range []*int{
			usage.CompletionTokenDetails.ReasoningTokens,
			usage.CompletionTokenDetails.AudioTokens,
			usage.CompletionTokenDetails.AcceptedPredictionTokens,
			usage.CompletionTokenDetails.RejectedPredictionTokens,
		} {
			if partition != nil && *partition > completionTokens {
				return true
			}
		}
		if usage.CompletionTokenDetails.AcceptedPredictionTokens != nil &&
			usage.CompletionTokenDetails.RejectedPredictionTokens != nil {
			predictionTotal, ok := safeIntSum(
				*usage.CompletionTokenDetails.AcceptedPredictionTokens,
				*usage.CompletionTokenDetails.RejectedPredictionTokens,
			)
			if !ok || predictionTotal > completionTokens {
				return true
			}
		}
	}
	return false
}

func openAIUnpricedUsageCategories(usage openAIUsage) []string {
	var categories []string
	if usage.PromptTokensDetails != nil &&
		intValue(usage.PromptTokensDetails.AudioTokens) != 0 {
		categories = append(categories, "prompt_tokens_details.audio_tokens")
	}
	if usage.CompletionTokenDetails == nil {
		return categories
	}
	for _, category := range []struct {
		name  string
		value *int
	}{
		{
			name:  "completion_tokens_details.audio_tokens",
			value: usage.CompletionTokenDetails.AudioTokens,
		},
		{
			name:  "completion_tokens_details.accepted_prediction_tokens",
			value: usage.CompletionTokenDetails.AcceptedPredictionTokens,
		},
		{
			name:  "completion_tokens_details.rejected_prediction_tokens",
			value: usage.CompletionTokenDetails.RejectedPredictionTokens,
		},
	} {
		if intValue(category.value) != 0 {
			categories = append(categories, category.name)
		}
	}
	return categories
}

func isDeepSeekV4Model(model string) bool {
	return model == "deepseek-v4-flash" || model == "deepseek-v4-pro"
}

func isDeepSeekBetaBaseURL(baseURL string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return normalized == "https://api.deepseek.com/beta"
}

func structuredOutputSchemaForModel(
	model string,
	schemaJSON json.RawMessage,
	deepSeekBetaDialect ...bool,
) (json.RawMessage, error) {
	useDeepSeekDialect := isDeepSeekV4Model(model)
	if len(deepSeekBetaDialect) > 0 {
		useDeepSeekDialect = useDeepSeekDialect && deepSeekBetaDialect[0]
	}
	if !useDeepSeekDialect {
		return schemaJSON, nil
	}
	var schema any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil, fmt.Errorf("decode DeepSeek V4 strict structured output schema: %w", err)
	}
	rewriteDeepSeekSchemaDialect(schema)
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode DeepSeek V4 strict structured output schema: %w", err)
	}
	return encoded, nil
}

func rewriteDeepSeekSchemaDialect(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if definitions, ok := typed["$defs"]; ok {
			typed["$def"] = definitions
			delete(typed, "$defs")
		}
		for key, child := range typed {
			if key == "$ref" {
				if ref, ok := child.(string); ok {
					typed[key] = strings.Replace(ref, "#/$defs/", "#/$def/", 1)
				}
				continue
			}
			rewriteDeepSeekSchemaDialect(child)
		}
	case []any:
		for _, child := range typed {
			rewriteDeepSeekSchemaDialect(child)
		}
	}
}

func validateStructuredOutputDefinition(output *llm.StructuredOutput) error {
	if output == nil {
		return nil
	}
	name := strings.TrimSpace(output.Name)
	if name == "" || name != output.Name || len(name) > 64 {
		return fmt.Errorf("openai strict structured output requires an exact function name of at most 64 bytes")
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("openai strict structured output function name contains unsupported character %q", char)
	}
	if len(output.Schema) == 0 || !json.Valid(output.Schema) {
		return fmt.Errorf("openai strict structured output requires a valid JSON schema")
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(output.Schema, &schema); err != nil {
		return fmt.Errorf("openai strict structured output schema: %w", err)
	}
	if _, err := schema.Resolve(nil); err != nil {
		return fmt.Errorf("openai strict structured output schema: %w", err)
	}
	return nil
}

func validateStructuredOutputArguments(schemaJSON json.RawMessage, arguments string) error {
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return fmt.Errorf("decode strict structured output schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve strict structured output schema: %w", err)
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return fmt.Errorf("strict structured output arguments are not valid JSON: %w", err)
	}
	if err := resolved.Validate(value); err != nil {
		// Branch causes first: the bounded feedback budget truncates the tail,
		// and the concrete violating values are the most diagnostic segment.
		return fmt.Errorf(
			"strict structured output arguments do not match schema: %s; branch causes: %s; object shapes: %s; structural fragment: %s",
			structuredOutputValidationSummary(err.Error()),
			structuredOutputBranchCauses(schemaJSON, arguments),
			structuredOutputObjectShapes(value),
			structuredOutputStructuralFragment(value),
		)
	}
	return nil
}

// structuredOutputBranchCauses walks the arguments against the action anyOf
// branches with a self-contained strict-shape checker so failures name the
// concrete violating values (e.g. `steps[2].criteria.field: "items" not in
// [text website]`). The schema library's anyOf handler discards branch-level
// detail and its error formatting dereferences nil pointers, so this path
// deliberately avoids it.
func structuredOutputBranchCauses(schemaJSON json.RawMessage, arguments string) string {
	var root map[string]any
	if err := json.Unmarshal(schemaJSON, &root); err != nil {
		return "<unavailable>"
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return "<no-anyOf>"
	}
	actionDef, ok := defs["action"].(map[string]any)
	if !ok {
		return "<no-anyOf>"
	}
	branches, ok := actionDef["anyOf"].([]any)
	if !ok || len(branches) == 0 {
		return "<no-anyOf>"
	}
	normalizedBranches := make([]map[string]any, 0, len(branches))
	for _, branch := range branches {
		if branchObject, ok := branch.(map[string]any); ok {
			normalizedBranches = append(normalizedBranches, branchObject)
		}
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return "<unavailable>"
	}
	steps := make([]struct {
		path string
		step map[string]any
	}, 0, 8)
	collectActionObjects(value, "$", &steps)
	causes := make([]string, 0, 8)
	for _, entry := range steps {
		if match := diagnoseStep(entry.path, entry.step, normalizedBranches, defs); match != "" {
			causes = append(causes, boundedString(match, 240))
		}
		if len(causes) >= 6 {
			break
		}
	}
	if len(causes) == 0 {
		return "<none>"
	}
	return strings.Join(causes, " | ")
}

// collectActionObjects gathers every object that carries an "action" key,
// including nested loop/if step arrays.
func collectActionObjects(node any, path string, steps *[]struct {
	path string
	step map[string]any
}) {
	switch typed := node.(type) {
	case map[string]any:
		if _, hasAction := typed["action"]; hasAction && len(typed) > 1 {
			*steps = append(*steps, struct {
				path string
				step map[string]any
			}{path, typed})
		}
		for key, child := range typed {
			collectActionObjects(child, path+"."+key, steps)
		}
	case []any:
		for index, child := range typed {
			collectActionObjects(child, fmt.Sprintf("%s[%d]", path, index), steps)
		}
	}
}

// diagnoseStep reports why a step matches no branch. A step is fine when ANY
// branch fully matches (the schema's anyOf semantics); problems are only
// reported when every branch fails, preferring branches that share the
// step's action.
func diagnoseStep(path string, step map[string]any, branches []map[string]any, defs map[string]any) string {
	action, _ := step["action"].(string)
	type scoredBranch struct {
		branch map[string]any
		score  int
	}
	scored := make([]scoredBranch, 0, 2)
	for _, branch := range branches {
		if checkStrictObjectDefs(path, step, branch, defs, 0) == "" {
			return ""
		}
		if branchAction, ok := actionEnumValue(branch); ok && branchAction == action {
			scored = append(scored, scoredBranch{branch, branchEnumMatchScore(step, branch)})
		}
	}
	if len(scored) == 0 {
		return fmt.Sprintf("%s: no branch for action %q", path, action)
	}
	// Report problems only from the branches that best match the step's other
	// enum-discriminated properties (e.g. the forEach loop branch, not the
	// fixedCount one), so the cause names the intended shape.
	best := 0
	for _, candidate := range scored {
		if candidate.score > best {
			best = candidate.score
		}
	}
	problems := make([]string, 0, 4)
	for _, candidate := range scored {
		if candidate.score != best {
			continue
		}
		if problem := checkStrictObjectDefs(path, step, candidate.branch, defs, 0); problem != "" {
			problems = append(problems, problem)
		}
		if len(problems) >= 2 {
			break
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return fmt.Sprintf("%s(action=%s): %s", path, action, strings.Join(problems, "; "))
}

func actionEnumValue(branch map[string]any) (string, bool) {
	properties, ok := branch["properties"].(map[string]any)
	if !ok {
		return "", false
	}
	actionSchema, ok := properties["action"].(map[string]any)
	if !ok {
		return "", false
	}
	enum, ok := actionSchema["enum"].([]any)
	if !ok || len(enum) == 0 {
		return "", false
	}
	value, ok := enum[0].(string)
	return value, ok
}

// branchEnumMatchScore counts how many of the branch's enum-discriminated
// properties (beyond "action") match the step's values, preferring the
// branch the step was actually shaped for.
func branchEnumMatchScore(step map[string]any, branch map[string]any) int {
	properties, ok := branch["properties"].(map[string]any)
	if !ok {
		return 0
	}
	score := 0
	for name, propertySchema := range properties {
		if name == "action" {
			continue
		}
		schemaObject, ok := propertySchema.(map[string]any)
		if !ok {
			continue
		}
		enum, ok := schemaObject["enum"].([]any)
		if !ok {
			continue
		}
		if value, present := step[name]; present && enumContains(enum, value) {
			score++
		}
	}
	return score
}

// checkStrictObject validates an object against one of our machine-generated
// strictObject schemas (type/properties/required/additionalProperties/enum),
// recursing one level into nested object schemas, and returns the first
// concrete violation.
func checkStrictObject(path string, object map[string]any, schema map[string]any, depth int) string {
	return checkStrictObjectDefs(path, object, schema, nil, depth)
}

func checkStrictObjectDefs(path string, object map[string]any, schema map[string]any, defs map[string]any, depth int) string {
	if depth > 4 {
		return ""
	}
	if resolved := resolveSchemaRef(schema, defs); resolved != nil {
		schema = resolved
	}
	if enum, ok := schema["enum"].([]any); ok {
		if !enumContains(enum, object) {
			return fmt.Sprintf("%s: %s not in %s", path, boundedString(fmt.Sprint(object), 80), enumSummary(enum))
		}
		return ""
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		return ""
	}
	if required, ok := schema["required"].([]any); ok {
		for _, name := range required {
			if _, present := object[name.(string)]; !present {
				return fmt.Sprintf("%s.%s: required property missing", path, name)
			}
		}
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		propertySchema, ok := properties[key].(map[string]any)
		if !ok {
			if additional, has := schema["additionalProperties"].(bool); has && !additional {
				return fmt.Sprintf("%s.%s: unknown property", path, key)
			}
			continue
		}
		if problem := checkValueDefs(fmt.Sprintf("%s.%s", path, key), object[key], propertySchema, defs, depth); problem != "" {
			return problem
		}
	}
	return ""
}

func resolveSchemaRef(schema map[string]any, defs map[string]any) map[string]any {
	reference, ok := schema["$ref"].(string)
	if !ok || !strings.HasPrefix(reference, "#/$defs/") || defs == nil {
		return nil
	}
	resolved, ok := defs[strings.TrimPrefix(reference, "#/$defs/")].(map[string]any)
	return resolved
}

func checkValue(path string, value any, schema map[string]any, depth int) string {
	return checkValueDefs(path, value, schema, nil, depth)
}

func checkValueDefs(path string, value any, schema map[string]any, defs map[string]any, depth int) string {
	if resolved := resolveSchemaRef(schema, defs); resolved != nil {
		schema = resolved
	}
	if enum, ok := schema["enum"].([]any); ok && !enumContains(enum, value) {
		return fmt.Sprintf("%s: %s not in %s", path, boundedString(fmt.Sprint(value), 80), enumSummary(enum))
	}
	if expected, ok := schema["type"].(string); ok && expected == "object" {
		if object, isObject := value.(map[string]any); isObject {
			return checkStrictObjectDefs(path, object, schema, defs, depth+1)
		}
	}
	if expected, ok := schema["type"].(string); ok && expected == "array" {
		if items, isItems := schema["items"].(map[string]any); isItems {
			if list, isList := value.([]any); isList {
				for index, item := range list {
					if itemObject, isObject := item.(map[string]any); isObject {
						if problem := checkStrictObjectDefs(fmt.Sprintf("%s[%d]", path, index), itemObject, items, defs, depth+1); problem != "" {
							return problem
						}
					}
				}
			}
		}
	}
	return ""
}

func enumContains(enum []any, value any) bool {
	encodedValue, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, candidate := range enum {
		encodedCandidate, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		if string(encodedValue) == string(encodedCandidate) {
			return true
		}
	}
	return false
}

func enumSummary(enum []any) string {
	values := make([]string, 0, len(enum))
	for _, candidate := range enum {
		values = append(values, fmt.Sprint(candidate))
	}
	summary := "[" + strings.Join(values, " ") + "]"
	return boundedString(summary, 120)
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func structuredOutputValidationSummary(message string) string {
	pathPattern := regexp.MustCompile(`validating (root|/[A-Za-z0-9_~./-]+)`)
	paths := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, match := range pathPattern.FindAllStringSubmatch(message, 8) {
		if len(match) < 2 {
			continue
		}
		if _, exists := seen[match[1]]; exists {
			continue
		}
		seen[match[1]] = struct{}{}
		paths = append(paths, match[1])
	}
	keyword := "validation"
	for _, candidate := range []string{
		"additionalProperties", "required", "enum", "type", "anyOf", "oneOf",
		"minimum", "maximum", "minItems", "maxItems", "pattern",
	} {
		if strings.Contains(message, candidate+":") {
			keyword = candidate
			break
		}
	}
	if len(paths) == 0 {
		return "path=<unavailable>; keyword=" + keyword
	}
	return "path=" + strings.Join(paths, " -> ") + "; keyword=" + keyword
}

func structuredOutputStructuralFragment(value any) string {
	const maxDepth = 5
	var redact func(any, int) any
	redact = func(current any, depth int) any {
		if depth >= maxDepth {
			return "<depth-limit>"
		}
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			result := make(map[string]any, len(keys))
			for _, key := range keys {
				result[key] = redact(typed[key], depth+1)
			}
			return result
		case []any:
			result := make([]any, 0, min(len(typed), 3))
			for index, child := range typed {
				if index == 3 {
					result = append(result, "<remaining-items>")
					break
				}
				result = append(result, redact(child, depth+1))
			}
			return result
		case string:
			return "<string>"
		case float64:
			return "<number>"
		case bool:
			return "<boolean>"
		case nil:
			return "<null>"
		default:
			return "<unknown>"
		}
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(redact(value, 0)); err != nil {
		return "<unavailable>"
	}
	fragment := strings.TrimSpace(encoded.String())
	if len(fragment) > 2000 {
		return fragment[:2000] + "..."
	}
	return fragment
}

func structuredOutputObjectShapes(value any) string {
	shapes := make([]string, 0, 16)
	var walk func(any, string)
	walk = func(current any, path string) {
		if len(shapes) >= 32 {
			return
		}
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			discriminator := ""
			for _, key := range []string{"action", "type"} {
				if raw, ok := typed[key].(string); ok {
					discriminator += fmt.Sprintf(" %s=%q", key, raw)
				}
			}
			shapes = append(shapes, fmt.Sprintf("%s%s keys=%v", path, discriminator, keys))
			for _, key := range keys {
				walk(typed[key], path+"."+key)
			}
		case []any:
			for index, item := range typed {
				walk(item, fmt.Sprintf("%s[%d]", path, index))
			}
		}
	}
	walk(value, "$")
	if len(shapes) == 0 {
		return "<non-object>"
	}
	result := strings.Join(shapes, "; ")
	if runes := []rune(result); len(runes) > 2000 {
		return string(runes[:2000]) + "..."
	}
	return result
}

func (p *OpenAIProvider) structuredOutputResponseError(
	ctx context.Context,
	req llm.CompletionRequest,
	model string,
	status int,
	body boundedProviderResponseBody,
	provenance llm.CompletionContentProvenance,
	code, message string,
) (*llm.CompletionResponse, error) {
	safeErr := newProviderResponseError(p.name, status, code, message)
	if captureErr := llm.TraceProviderError(
		ctx,
		req,
		p.name,
		model,
		status,
		code,
		string(body.content),
		safeErr,
		provenance,
	); captureErr != nil {
		return nil, captureErr
	}
	return nil, safeErr
}
