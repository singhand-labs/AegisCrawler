package intent

import (
	"context"
	"errors"
	"fmt"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"go.uber.org/zap"
)

// Predictor predicts user intent from a PageAgent recording.
type Predictor struct {
	cfg          *config.Config
	orchestrator *llm.Orchestrator
	logger       *zap.Logger
}

// NewPredictor creates a new intent predictor.
func NewPredictor(cfg *config.Config, orchestrator *llm.Orchestrator, logger *zap.Logger) *Predictor {
	return &Predictor{
		cfg:          cfg,
		orchestrator: orchestrator,
		logger:       logger,
	}
}

// Predict returns candidate intents for the given recording.
func (p *Predictor) Predict(ctx context.Context, recording map[string]any) (*PredictionResult, error) {
	if !p.cfg.LLMEnabled || p.orchestrator == nil {
		p.logger.Info("llm disabled, returning fallback intent prediction")
		return p.fallbackResult(recording), nil
	}

	p.logger.Info("intent prediction phase: building prompt")
	sys, user, err := BuildPredictPrompt(recording)
	if err != nil {
		p.logger.Error("intent prediction phase failed: build prompt", zap.Error(err))
		if !p.cfg.AllowDegradedFallback() {
			return nil, fmt.Errorf("build intent prompt: %w", err)
		}
		return p.fallbackResult(recording), nil
	}

	req := llm.CompletionRequest{
		Model:       p.cfg.LLMModel,
		System:      sys,
		User:        user,
		Temperature: p.cfg.LLMTemperature,
		JSONMode:    true,
	}

	p.logger.Info("intent prediction phase: calling llm", zap.String("model", req.Model))
	resp, err := p.orchestrator.Complete(ctx, req)
	if err != nil {
		p.logger.Error("intent prediction phase failed: llm complete", zap.Error(err))
		if !p.cfg.AllowDegradedFallback() {
			return nil, err
		}
		return p.fallbackResult(recording), nil
	}

	p.logger.Info("intent prediction phase: parsing candidates",
		zap.String("model", p.cfg.LLMModel),
		zap.Bool("cacheHit", resp.CacheHit),
		zap.Int("contentLength", len(resp.Content)))
	candidates, err := ParseCandidates(resp.Content)
	if err != nil {
		p.logger.Error("intent prediction phase failed: parse candidates", zap.Error(err))
		if !p.cfg.AllowDegradedFallback() {
			return nil, fmt.Errorf("parse intent candidates: %w", err)
		}
		return p.fallbackResult(recording), nil
	}

	// Tag LLM-derived candidates; padCandidates will preserve these.
	for i := range candidates {
		if candidates[i].Source == "" {
			candidates[i].Source = "llm"
		}
	}

	candidates = FilterCandidates(candidates)
	if len(candidates) == 0 {
		p.logger.Warn("all candidates filtered as unsafe, returning fallback")
		if !p.cfg.AllowDegradedFallback() {
			return nil, errors.New("all LLM intent candidates were rejected by the safety policy")
		}
		return p.fallbackResult(recording), nil
	}

	if len(candidates) < 3 {
		p.logger.Warn("too few candidates from llm, padding with generic",
			zap.Int("got", len(candidates)))
		candidates = padCandidates(candidates)
	}

	p.logger.Info("intent prediction phase: completed",
		zap.Int("candidateCount", len(candidates)),
		zap.String("model", p.cfg.LLMModel),
		zap.Bool("cacheHit", resp.CacheHit))
	return &PredictionResult{
		Candidates:     candidates,
		FallbackIntent: customFallback(),
		Model:          p.cfg.LLMModel,
		CacheHit:       resp.CacheHit,
	}, nil
}

func (p *Predictor) fallbackResult(recording map[string]any) *PredictionResult {
	meta, _ := recording["meta"].(map[string]any)
	title, _ := meta["title"].(string)
	if title == "" {
		title = "当前页面"
	}
	return &PredictionResult{
		Candidates: []Candidate{
			{
				ID:          "generic",
				Label:       fmt.Sprintf("采集 %s 数据", title),
				Description: "根据录制操作生成基础采集脚本。",
				Confidence:  0.5,
				Source:      "synthetic",
			},
			{
				ID:          "c_navigate",
				Label:       "导航并采集目标页数据",
				Description: "点击链接进入目标页面后抓取数据。",
				Confidence:  0.3,
				Source:      "synthetic",
			},
			{
				ID:          "c_search",
				Label:       "搜索关键词并采集结果",
				Description: "在搜索框输入关键词后抓取搜索结果。",
				Confidence:  0.2,
				Source:      "synthetic",
			},
		},
		FallbackIntent: customFallback(),
		Model:          "baseline",
		CacheHit:       false,
	}
}

func customFallback() Candidate {
	return Candidate{
		ID:          "custom",
		Label:       "其他目的（自行输入）",
		Description: "请手动描述你的采集目的。",
		Confidence:  0.0,
	}
}

func padCandidates(candidates []Candidate) []Candidate {
	// Tag LLM-derived candidates so the UI can distinguish them from padding.
	for i := range candidates {
		if candidates[i].Source == "" {
			candidates[i].Source = "llm"
		}
	}
	defaults := []Candidate{
		{ID: "c_generic", Label: "采集页面可见数据", Description: "抓取页面上可见的文本、链接等数据。", Confidence: 0.3, Source: "synthetic"},
		{ID: "c_navigate", Label: "导航并采集目标页数据", Description: "点击链接进入目标页面后抓取数据。", Confidence: 0.2, Source: "synthetic"},
		{ID: "c_search", Label: "搜索关键词并采集结果", Description: "在搜索框输入关键词后抓取搜索结果。", Confidence: 0.2, Source: "synthetic"},
	}
	for _, d := range defaults {
		if len(candidates) >= 3 {
			break
		}
		exists := false
		for _, c := range candidates {
			if c.Label == d.Label {
				exists = true
				break
			}
		}
		if !exists {
			candidates = append(candidates, d)
		}
	}
	return candidates
}
