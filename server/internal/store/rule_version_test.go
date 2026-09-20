package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestRuleVersionLifecycleIsImmutableAndProjectsApproval(t *testing.T) {
	s := newTestStore(t)
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "alice", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin},
	})
	now := time.Now().UTC()
	rule := workspaceRule("versioned-rule", now)
	rule.Name = "catalog-before-version"
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	provisional := workspaceRule(rule.ID, now)
	provisional.Name = "approved-v1"
	provisional.Version = "1.0.0"
	provisional.Owner = "spoofed"
	version1, err := s.CreateRuleVersion(ctx, provisional, "")
	if err != nil {
		t.Fatal(err)
	}
	if version1.Version != 1 || version1.Status != models.RuleApprovalPending || version1.Owner != "alice" || version1.Rule.Owner != "alice" {
		t.Fatalf("unexpected provisional version: %+v", version1)
	}
	duplicate, err := s.CreateRuleVersion(ctx, provisional, "")
	if err != nil || duplicate.Version != version1.Version {
		t.Fatalf("same immutable content should be idempotent: version=%+v err=%v", duplicate, err)
	}

	approved, err := s.ApproveRuleVersion(ctx, rule.ID, version1.Version)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != models.RuleApprovalApproved || approved.ApprovedBy != "alice" || approved.ApprovedAt == nil {
		t.Fatalf("unexpected approval state: %+v", approved)
	}
	projected, err := s.GetRuleByID(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Name != "approved-v1" || projected.ApprovalStatus != string(models.RuleApprovalApproved) || !projected.Enabled {
		t.Fatalf("approved version was not projected to legacy rule view: %+v", projected)
	}
	if _, err := s.ApproveRuleVersion(ctx, rule.ID, version1.Version); !errors.Is(err, ErrRuleVersionState) {
		t.Fatalf("expected second approval to fail, got %v", err)
	}
	if _, err := s.db.Exec(`UPDATE rule_versions SET rule_json = '{}' WHERE workspace_id = ? AND rule_id = ? AND version_number = ?`, authz.DefaultWorkspaceID, rule.ID, version1.Version); err == nil {
		t.Fatal("database trigger allowed immutable content update")
	}
	if _, err := s.db.Exec(`DELETE FROM rule_versions WHERE workspace_id = ? AND rule_id = ? AND version_number = ?`, authz.DefaultWorkspaceID, rule.ID, version1.Version); err == nil {
		t.Fatal("database trigger allowed immutable version deletion")
	}

	provisional2 := workspaceRule(rule.ID, now)
	provisional2.Name = "rejected-v2"
	provisional2.Version = "2.0.0"
	version2, err := s.CreateRuleVersion(ctx, provisional2, "")
	if err != nil {
		t.Fatal(err)
	}
	if version2.Version != 2 {
		t.Fatalf("expected version 2, got %+v", version2)
	}
	rejected, err := s.RejectRuleVersion(ctx, rule.ID, version2.Version)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != models.RuleApprovalRejected || rejected.RejectedBy != "alice" || rejected.RejectedAt == nil {
		t.Fatalf("unexpected rejection state: %+v", rejected)
	}
	projected, err = s.GetRuleByID(ctx, rule.ID)
	if err != nil || projected.Name != "approved-v1" {
		t.Fatalf("rejected version changed approved projection: rule=%+v err=%v", projected, err)
	}
	versions, err := s.ListRuleVersions(ctx, rule.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("unexpected version history: versions=%+v err=%v", versions, err)
	}
}

func TestRuleVersionsAreWorkspaceScoped(t *testing.T) {
	s := newTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.CreateRule(ctxA, workspaceRule("rule-a", now)); err != nil {
		t.Fatal(err)
	}
	version, err := s.CreateRuleVersion(ctxA, workspaceRule("rule-a", now), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRuleVersion(ctxB, "rule-a", version.Version); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("cross-workspace version lookup should be hidden, got %v", err)
	}
	if versions, err := s.ListRuleVersions(ctxB, "rule-a"); err != nil || len(versions) != 0 {
		t.Fatalf("cross-workspace version list leaked data: versions=%v err=%v", versions, err)
	}
	if _, err := s.ApproveRuleVersion(ctxB, "rule-a", version.Version); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("cross-workspace approval should be hidden, got %v", err)
	}
}
