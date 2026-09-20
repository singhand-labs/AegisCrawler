package store

import (
	"context"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func newEnhanceTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetRuleEnhancement(t *testing.T) {
	s := newEnhanceTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Name:           "Test",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Steps:          models.JSON(`[]`),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	e := &models.RuleEnhancement{
		ID:           "enh-1",
		RuleID:       "rule-1",
		Baseline:     models.JSON(`{"id":"rule-1"}`),
		Enhanced:     models.JSON(`{"id":"rule-1","selectors":{}}`),
		Patch:        models.JSON(`{"steps":[]}`),
		UserHint:     "hint",
		Provider:     "fake",
		Model:        "fake-model",
		InputTokens:  10,
		OutputTokens: 20,
		Suggestions:  models.JSON(`["suggestion"]`),
		SafetyFlags:  models.JSON(`[]`),
		Status:       string(models.EnhancementStatusPending),
		CreatedAt:    now,
	}
	if err := s.CreateRuleEnhancement(ctx, e); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != e.ID {
		t.Fatalf("expected id %s, got %s", e.ID, got.ID)
	}
	if got.Status != e.Status {
		t.Fatalf("expected status %s, got %s", e.Status, got.Status)
	}
	if string(got.Baseline) != string(e.Baseline) {
		t.Fatalf("expected baseline %s, got %s", e.Baseline, got.Baseline)
	}
}

func TestUpdateRuleEnhancementStatus(t *testing.T) {
	s := newEnhanceTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Name:           "Test",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Steps:          models.JSON(`[]`),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	e := &models.RuleEnhancement{
		ID:        "enh-1",
		RuleID:    "rule-1",
		Baseline:  models.JSON(`{}`),
		Enhanced:  models.JSON(`{}`),
		Patch:     models.JSON(`{}`),
		Provider:  "fake",
		Model:     "fake-model",
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: now,
	}
	if err := s.CreateRuleEnhancement(ctx, e); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateRuleEnhancementStatus(ctx, "enh-1", string(models.EnhancementStatusApproved)); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(models.EnhancementStatusApproved) {
		t.Fatalf("expected approved, got %s", got.Status)
	}
}
