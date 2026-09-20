package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"github.com/singhand-labs/AegisCrawler/internal/rule"
	"go.uber.org/zap"
)

// EnhanceRequest is the input to the rule enhancement pipeline.
type EnhanceRequest struct {
	Recording    map[string]any
	BaselineRule map[string]any
	UserHint     string
}

// EnhanceResult is the output of the rule enhancement pipeline.
// On failure Rule equals the baseline rule and Error contains the details.
type EnhanceResult struct {
	Rule         map[string]any
	Patch        map[string]any
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CacheHit     bool
	Suggestions  []string
	SafetyFlags  []SafetyFlag
	Error        string
}

// Enhancer orchestrates the LLM rule enhancement pipeline.
type Enhancer struct {
	cfg          *config.Config
	orchestrator *Orchestrator
	budget       *tokenBudget
	logger       *zap.Logger
}

// NewEnhancer creates a new Enhancer.
func NewEnhancer(cfg *config.Config, orch *Orchestrator, logger *zap.Logger) *Enhancer {
	return &Enhancer{
		cfg:          cfg,
		orchestrator: orch,
		budget:       newTokenBudget(cfg, logger),
		logger:       logger,
	}
}

// SetMetrics assigns the LLM metrics collector to the enhancer and its
// underlying orchestrator.
func (e *Enhancer) SetMetrics(m *Metrics) {
	if e.orchestrator != nil {
		e.orchestrator.SetMetrics(m)
	}
}

func (e *Enhancer) enforcedAudit() bool {
	return e != nil && e.cfg != nil && e.cfg.EnforcedLLMPolicy() != nil
}

func (e *Enhancer) logDynamicError(message, class string, err error) {
	if e.enforcedAudit() {
		e.logger.Error(message, HashedAuditFields("error", class, err.Error())...)
		return
	}
	e.logger.Error(message, zap.Error(err))
}

func (e *Enhancer) logDynamicWarning(message, class, value string) {
	if e.enforcedAudit() {
		e.logger.Warn(message, HashedAuditFields("reason", class, value)...)
		return
	}
	e.logger.Warn(message, zap.String("reason", value))
}

// Enhance builds a prompt, calls the LLM, applies the returned patch, validates
// the result, optionally verifies the patch via reflection, scans for safety
// issues, and returns the enhanced rule. On any error it returns the baseline
// rule with details in EnhanceResult.Error.
func (e *Enhancer) Enhance(ctx context.Context, req EnhanceRequest) (*EnhanceResult, error) {
	baselineName, baselineNamePresent := req.BaselineRule["name"]
	requestFields := []zap.Field{zap.Bool("llmEnabled", e.cfg.LLMEnabled)}
	if e.enforcedAudit() {
		requestFields = append(requestFields, zap.Bool("baselineNamePresent", baselineNamePresent))
		if baselineNamePresent {
			requestFields = append(requestFields,
				HashedAuditFields("baselineName", "baseline_name", fmt.Sprint(baselineName))...,
			)
		}
		// UserHint becomes provider prompt content. Enforced-mode operational
		// logs record only its presence, never the raw user-supplied value.
		requestFields = append(requestFields, zap.Bool("hintPresent", strings.TrimSpace(req.UserHint) != ""))
	} else {
		requestFields = append(requestFields,
			zap.Any("baselineName", baselineName),
			zap.String("hint", req.UserHint),
		)
	}
	e.logger.Info("rule enhancement requested", requestFields...)

	if !e.cfg.LLMEnabled {
		if err := e.failClosed("LLM enhancement is disabled"); err != nil {
			return nil, err
		}
		res := e.fallback(req.BaselineRule, "")
		e.logCompleted(res)
		return res, nil
	}

	sys, user, err := prompt.BuildWithVersion(req.Recording, req.BaselineRule, req.UserHint, e.cfg.LLMPromptVersion)
	if err != nil {
		e.logDynamicError("build prompt failed, falling back", "prompt_build_failed", err)
		if failErr := e.failClosed(fmt.Sprintf("build prompt: %v", err)); failErr != nil {
			return nil, failErr
		}
		res := e.fallback(req.BaselineRule, fmt.Sprintf("build prompt: %v", err))
		e.logCompleted(res)
		return res, nil
	}

	if e.cfg.LLMAuditPrompts {
		hash, prefix := prompt.AuditPreview(sys, user, e.cfg.LLMModel)
		auditFields := []zap.Field{zap.String("promptHash", hash)}
		if e.cfg.EnforcedLLMPolicy() == nil {
			auditFields = append(auditFields, zap.String("promptPrefix", prefix))
		}
		e.logger.Info("llm prompt audit", auditFields...)
	}

	estimatedInput := estimateInputTokens(sys + user)

	if maxInputTokens := e.maxInputTokens(); maxInputTokens > 0 && estimatedInput > maxInputTokens {
		e.logger.Warn("llm prompt exceeds max input tokens, falling back",
			zap.Int("estimatedInput", estimatedInput),
			zap.Int("maxInputTokens", maxInputTokens),
		)
		failure := fmt.Sprintf("input tokens %d exceed limit %d", estimatedInput, maxInputTokens)
		if err := e.failClosed(failure); err != nil {
			return nil, err
		}
		res := e.fallback(req.BaselineRule, failure)
		res.InputTokens = estimatedInput
		e.logCompleted(res)
		return res, nil
	}

	llmReq := CompletionRequest{
		Model:       e.cfg.LLMModel,
		System:      sys,
		User:        user,
		Temperature: e.cfg.LLMTemperature,
		JSONMode:    true,
	}

	// Try the cache before checking the daily budget so an exhausted budget
	// never blocks a zero-cost cache hit.
	var resp *CompletionResult
	if e.cfg.EnforcedLLMPolicy() != nil {
		// The enforced orchestrator performs the policy-bound cache lookup
		// after resolving dispatch ownership and records cache lineage before
		// returning. It also performs hard-budget admission on a miss.
		resp, err = e.orchestrator.Complete(ctx, llmReq)
		if err != nil {
			e.logDynamicError("llm enhance failed", "provider_completion_failed", err)
			return nil, fmt.Errorf("llm completion: %w", err)
		}
	} else if cached, hit := e.orchestrator.PeekCache(ctx, llmReq); hit {
		resp = cached
	} else {
		if !e.budget.checkBefore(estimatedInput) {
			e.logger.Warn("llm daily cost budget exhausted, falling back")
			res := e.fallback(req.BaselineRule, "daily LLM cost budget exhausted")
			res.InputTokens = estimatedInput
			e.logCompleted(res)
			return res, nil
		}

		var err error
		resp, err = e.orchestrator.Complete(ctx, llmReq)
		if err != nil {
			e.logDynamicError("llm enhance failed, falling back to baseline", "provider_completion_failed", err)
			res := e.fallback(req.BaselineRule, fmt.Sprintf("llm completion: %v", err))
			e.logCompleted(res)
			return res, nil
		}

		if !resp.CacheHit {
			e.budget.record(resp.InputTokens, resp.OutputTokens)
		}
	}

	if maxOutputTokens := e.maxOutputTokens(); maxOutputTokens > 0 && resp.OutputTokens > maxOutputTokens {
		e.logger.Warn("llm output exceeds max tokens, falling back",
			zap.Int("outputTokens", resp.OutputTokens),
			zap.Int("maxOutputTokens", maxOutputTokens),
		)
		failure := fmt.Sprintf("output tokens %d exceed limit %d", resp.OutputTokens, maxOutputTokens)
		if err := e.failClosed(failure); err != nil {
			return nil, err
		}
		res := e.fallback(req.BaselineRule, failure)
		res.InputTokens = resp.InputTokens
		res.OutputTokens = resp.OutputTokens
		e.logCompleted(res)
		return res, nil
	}

	suggestion, err := parseSuggestion(resp.Content)
	if err != nil {
		e.logDynamicError("failed to parse llm suggestion, falling back", "provider_output_parse_failed", err)
		if failErr := e.failClosed(fmt.Sprintf("parse suggestion: %v", err)); failErr != nil {
			return nil, failErr
		}
		res := e.fallback(req.BaselineRule, fmt.Sprintf("parse suggestion: %v", err))
		e.logCompleted(res)
		return res, nil
	}

	contentFlags := ContentFilter(suggestion)
	if reject, reason := UnsafeContentError(contentFlags); reject {
		e.logDynamicWarning("llm suggestion rejected by content filter, falling back", "provider_output_rejected", reason)
		if err := e.failClosed(fmt.Sprintf("content filter: %s", reason)); err != nil {
			return nil, err
		}
		res := e.fallback(req.BaselineRule, fmt.Sprintf("content filter: %s", reason))
		res.SafetyFlags = append(res.SafetyFlags, contentFlags...)
		res.InputTokens = resp.InputTokens
		res.OutputTokens = resp.OutputTokens
		e.logCompleted(res)
		return res, nil
	}

	patched, err := ApplyPatch(req.BaselineRule, suggestion)
	if err != nil {
		e.logDynamicError("failed to apply patch, falling back", "provider_patch_apply_failed", err)
		if failErr := e.failClosed(fmt.Sprintf("apply patch: %v", err)); failErr != nil {
			return nil, failErr
		}
		res := e.fallback(req.BaselineRule, fmt.Sprintf("apply patch: %v", err))
		e.logCompleted(res)
		return res, nil
	}

	if err := rule.Validate(patched); err != nil {
		e.logDynamicError("enhanced rule validation failed, falling back", "provider_patch_validation_failed", err)
		if failErr := e.failClosed(fmt.Sprintf("validate: %v", err)); failErr != nil {
			return nil, failErr
		}
		res := e.fallback(req.BaselineRule, fmt.Sprintf("validate: %v", err))
		e.logCompleted(res)
		return res, nil
	}

	if e.cfg.LLMEnableReflection {
		ok, reason, verifyErr := e.verifyPatch(ctx, req.BaselineRule, patched, req.UserHint, suggestion)
		if verifyErr != nil {
			if e.enforcedAudit() {
				e.logger.Warn("llm reflection failed",
					HashedAuditFields("error", "reflection_failed", verifyErr.Error())...)
			} else {
				e.logger.Warn("llm reflection failed", zap.Error(verifyErr))
			}
			if e.cfg.EnforcedLLMPolicy() != nil {
				return nil, fmt.Errorf("llm reflection: %w", verifyErr)
			}
			res := e.fallback(req.BaselineRule, fmt.Sprintf("verification: %v", verifyErr))
			e.logCompleted(res)
			return res, nil
		}
		if !ok {
			e.logDynamicWarning("llm reflection rejected patch, falling back", "reflection_rejected", reason)
			if err := e.failClosed(fmt.Sprintf("verification: %s", reason)); err != nil {
				return nil, err
			}
			res := e.fallback(req.BaselineRule, fmt.Sprintf("verification: %s", reason))
			e.logCompleted(res)
			return res, nil
		}
	}

	flags := ScanSafety(patched)
	flags = append(flags, contentFlags...)
	patch := map[string]any{
		"selectors":   suggestion.Selectors,
		"steps":       suggestion.Steps,
		"variables":   suggestion.Variables,
		"suggestions": suggestion.Suggestions,
	}

	result := &EnhanceResult{
		Rule:         patched,
		Patch:        patch,
		Provider:     resp.Provider,
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CacheHit:     resp.CacheHit,
		Suggestions:  suggestion.Suggestions,
		SafetyFlags:  flags,
	}
	e.logCompleted(result)
	return result, nil
}

func (e *Enhancer) logCompleted(res *EnhanceResult) {
	fields := []zap.Field{
		zap.String("provider", res.Provider),
		zap.String("model", res.Model),
		zap.Bool("cacheHit", res.CacheHit),
		zap.Int("safetyFlags", len(res.SafetyFlags)),
	}
	if e.enforcedAudit() {
		if res.Error != "" {
			fields = append(fields, HashedAuditFields("error", "enhancement_result_error", res.Error)...)
		}
	} else {
		fields = append(fields, zap.String("error", res.Error))
	}
	e.logger.Info("rule enhancement completed", fields...)
}

func (e *Enhancer) fallback(baseline map[string]any, errMsg string) *EnhanceResult {
	r := &EnhanceResult{
		Rule:        copyMap(baseline),
		Patch:       map[string]any{},
		Provider:    "baseline",
		Model:       "baseline",
		Suggestions: []string{"LLM enhancement failed or unavailable; returned baseline rule"},
		SafetyFlags: ScanSafety(baseline),
	}
	if errMsg != "" {
		r.Error = errMsg
	}
	return r
}

func (e *Enhancer) failClosed(message string) error {
	if e.cfg.EnforcedLLMPolicy() != nil {
		return fmt.Errorf("enforced LLM enhancement failed: %s", message)
	}
	return nil
}

func (e *Enhancer) maxInputTokens() int {
	if policy := e.cfg.EnforcedLLMPolicy(); policy != nil {
		return policy.Primary.MaxInputTokens
	}
	return e.cfg.LLMMaxInputTokens
}

func (e *Enhancer) maxOutputTokens() int {
	if policy := e.cfg.EnforcedLLMPolicy(); policy != nil {
		return policy.Primary.MaxOutputTokens
	}
	return e.cfg.LLMMaxOutputTokens
}

func parseSuggestion(content string) (*EnhancementSuggestion, error) {
	content = stripMarkdownFences(content)
	var s EnhancementSuggestion
	if err := json.Unmarshal([]byte(content), &s); err != nil {
		return nil, err
	}
	for i, op := range s.Steps {
		if op.Op == "" || op.Path == "" {
			return nil, fmt.Errorf("step %d missing op/path", i)
		}
	}
	return &s, nil
}

// verifyPatch asks the LLM to verify that a successfully applied/validated patch
// satisfies the user intent. It returns false plus a reason when the verifier
// reports valid: false.
func (e *Enhancer) verifyPatch(ctx context.Context, baseline, patched map[string]any, userHint string, suggestion *EnhancementSuggestion) (bool, string, error) {
	baselineJSON, _ := json.MarshalIndent(baseline, "", "  ")
	patchedJSON, _ := json.MarshalIndent(patched, "", "  ")
	suggestionJSON, _ := json.MarshalIndent(suggestion.Suggestions, "", "  ")
	user := fmt.Sprintf(`请作为校验器检查下面的增强补丁是否能满足用户意图。

用户意图：%s

Baseline 规则：
%s

增强后规则：
%s

AI 建议：
%s

请返回 JSON：{"valid": true/false, "issues": ["..."]}
`, userHint, baselineJSON, patchedJSON, suggestionJSON)
	sys := "你是一位严格的规则校验器，只返回 JSON，不要解释。"
	// Reflection is a second physical call, so it needs its own dispatch
	// identity. Reusing the enhancement identity would collide with the
	// already-dispatched primary call and be refused as a duplicate.
	res, err := e.orchestrator.Complete(verificationDispatchContext(ctx), CompletionRequest{
		Model:       e.cfg.LLMModel,
		System:      sys,
		User:        user,
		Temperature: 0.0,
		JSONMode:    true,
	})
	if err != nil {
		return false, "", err
	}
	var verdict struct {
		Valid  bool     `json:"valid"`
		Issues []string `json:"issues"`
	}
	content := stripMarkdownFences(res.Content)
	if err := json.Unmarshal([]byte(content), &verdict); err != nil {
		return false, "", err
	}
	if !verdict.Valid && len(verdict.Issues) > 0 {
		return false, verdict.Issues[0], nil
	}
	return verdict.Valid, "", nil
}

func copyMap(m map[string]any) map[string]any {
	out, err := deepCopyMap(m)
	if err != nil {
		out = make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// stripMarkdownFences removes leading/trailing ```json ... ``` code fences from
// LLM output so it can be parsed as JSON.
func stripMarkdownFences(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	// Drop the opening fence line (e.g. ```json).
	if idx := strings.Index(content, "\n"); idx >= 0 {
		content = content[idx+1:]
	}
	content = strings.TrimSpace(content)
	if strings.HasSuffix(content, "```") {
		if idx := strings.LastIndex(content, "\n"); idx >= 0 {
			content = content[:idx]
		}
	}
	return strings.TrimSpace(content)
}
