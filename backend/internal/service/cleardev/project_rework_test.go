package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// These are regression tests using the explicit provider/source/desktop doubles
// from project_planning_test.go and real migrated SQLite, not native UI proof.
func TestGenericProjectNativeConfirmationRejectsSourceDrift(t *testing.T) {
	for _, drift := range []string{"commit", "origin", "inspection-error", "registered-path"} {
		for _, alreadyOpen := range []bool{false, true} {
			t.Run(drift+"/"+map[bool]string{false: "before-offer", true: "open-window"}[alreadyOpen], func(t *testing.T) {
				ctx := context.Background()
				f := newProjectPlanningFixture(t, "EXISTING")
				selected := f.choose(t, f.create(t), "existing")
				_, child := f.prepare(t, selected)
				var offer core.HumanDecisionOffer
				if alreadyOpen {
					var found bool
					var err error
					offer, found, err = f.s.IssueHumanDecisionOffer(ctx, "project-rework-desktop")
					if err != nil || !found {
						t.Fatalf("current source offer: found=%v err=%v", found, err)
					}
				}
				switch drift {
				case "commit":
					f.h.source.BaseCommitSHA = forty("b")
				case "origin":
					f.h.source.RepositoryURL = "https://example.org/other/repository.git"
				case "inspection-error":
					f.h.sourceErr = errors.New("source is unavailable")
				case "registered-path":
					project, _, err := f.store.GetProject(ctx, "notes-project")
					if err != nil {
						t.Fatal(err)
					}
					project.Path = "/tmp/replaced-project"
					if err := f.store.UpsertProject(ctx, project); err != nil {
						t.Fatal(err)
					}
				}
				if alreadyOpen {
					err := f.s.ApplyHumanDecisionResult(ctx, core.HumanDecisionResult{
						ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID,
						RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion,
						Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
					})
					if err == nil {
						t.Error("old native window approved after source drift")
					}
				} else if _, found, err := f.s.IssueHumanDecisionOffer(ctx, "project-rework-desktop"); err != nil || found {
					t.Errorf("stale source was offered: found=%v err=%v", found, err)
				}
				after := mustGetComplex(t, f.s, child.Requirement.ID)
				if after.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation || len(after.ComplexPlanning.Plans) != 0 {
					t.Error("source drift changed the specification or created a plan")
				}
				assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
			})
		}
	}
}

func TestGenericProjectPlanStorageRejectsMissingProtocol(t *testing.T) {
	f := newProjectPlanningFixture(t, "EXISTING")
	ctx := context.Background()
	selected := f.choose(t, f.create(t), "existing")
	_, child := f.prepare(t, selected)
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if len(child.ComplexPlanning.Plans) != 1 {
		t.Fatal("valid planning-only plan missing")
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, bad := range []string{`{}`, `{"schemaVersion":null}`, `{"schemaVersion":2}`, `{"schemaVersion":2,"kind":null}`, `{"schemaVersion":3}`, `{"schemaVersion":3,"kind":"PRODUCT_CLARIFICATION_REQUIRED"}`, `{"schemaVersion":"3","kind":"COMPLEX_ENGINEERING_PLAN"}`} {
		t.Run(bad, func(t *testing.T) {
			_, err := raw.ExecContext(ctx, `INSERT INTO cleardev_complex_engineering_plans(id,development_project_id,plan_json) VALUES('invalid-project-plan',?,?)`, child.Requirement.ID, bad)
			if err == nil || !strings.Contains(err.Error(), "project plan") {
				t.Errorf("project protocol SQL guard did not reject %s: %v", bad, err)
			}
			plan := child.ComplexPlanning.Plans[0]
			var payload map[string]any
			if err := json.Unmarshal([]byte(bad), &payload); err != nil {
				t.Fatal(err)
			}
			plan.ID, plan.PlanJSON, plan.PlanSHA256 = "invalid-project-plan", bad, coreDigest([]byte(bad))
			if err := f.store.CreateClearDevComplexEngineeringPlan(ctx, core.CreateComplexPlanCommand{Plan: plan}); err == nil {
				t.Error("store accepted an unvalidated project protocol")
			}
		})
	}
}
