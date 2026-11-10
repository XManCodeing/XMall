package gormstore_test

import (
	"context"
	"errors"
	"testing"

	ga "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	gs "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// executionProbe performs a DB read during simulated HTTP to expose single-connection deadlocks.
type executionProbe struct {
	db         *gorm.DB
	generation uint64
	checks     int
	denied     bool
}

// Instance returns the synthetic runtime identity for bot; no secrets or live Bot is used.
func (probe *executionProbe) Instance(context.Context, uint) (gc.Identity, error) {
	return gc.Identity{Running: true, Generation: probe.generation, TelegramID: 44}, nil
}

// Check verifies DB availability while returning purpose-complete synthetic permissions.
func (probe *executionProbe) Check(ctx context.Context, bot uint, chat int64) gc.ProbeResult {
	probe.checks++
	var count int64
	if err := probe.db.WithContext(ctx).Model(&d.Task{}).Count(&count).Error; err != nil {
		return gc.ProbeResult{ErrorClass: "unavailable"}
	}
	if probe.denied {
		return gc.ProbeResult{ErrorClass: "permission_denied"}
	}
	return gc.ProbeResult{Observation: gd.Observation{Generation: probe.generation, BotTelegramID: 44,
		ChatType: "supergroup", Title: "Synthetic group", IsForum: true, IsMember: true, MemberStatus: "administrator",
		CanManageTopics: true, CanInviteUsers: true, CanRestrictMembers: true}}
}

// executionAuthority records permission checks and can model a revoked administrator grant.
type executionAuthority struct {
	denied bool
	calls  int
}

// AuthorizeGroupRecovery validates actor and target and returns denial when revoked.
func (authority *executionAuthority) AuthorizeGroupRecovery(_ context.Context, actor, target uint) error {
	authority.calls++
	if authority.denied || actor == 0 || target == 0 {
		return c.ErrInvalid
	}
	return nil
}

// executionModule supplies explicit proof or models unknown/leftover module work.
type executionModule struct {
	checkpointed bool
	after        func()
	actor        uint
	calls        int
	remaining    uint64
	unknown      uint64
	err          error
	verified     bool
}

// RecoverGroup echoes immutable route ownership and never changes membership terms/orders.
func (module *executionModule) RecoverGroup(_ context.Context, request c.GroupRecoveryRequest) (c.GroupRecoveryResult, error) {
	module.calls++
	module.actor = request.ActorID
	if module.after != nil {
		module.after()
	}
	return c.GroupRecoveryResult{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion,
		Checkpointed: module.checkpointed, Verified: module.verified, Remaining: module.remaining, Unknown: module.unknown}, module.err
}

// executionFixture wires real group and recovery repositories over one SQLite connection.
type executionFixture struct {
	*harness
	executor  *a.GroupExecutor
	groups    *ga.Service
	routes    *gs.Store
	probe     *executionProbe
	authority *executionAuthority
	module    *executionModule
	task      *d.Task
}

// setupExecution creates a pending same-Bot task for purpose using only synthetic resources.
func setupExecution(t *testing.T, purpose string) *executionFixture {
	t.Helper()
	h := setup(t)
	if err := h.db.AutoMigrate(&gd.Target{}, &gd.Route{}, &gd.Operation{}, &gd.ModuleConfig{}, &gd.RecoveryFence{}); err != nil {
		t.Fatal(err)
	}
	fixture := &executionFixture{harness: h, routes: gs.New(h.db), probe: &executionProbe{db: h.db, generation: 1}, authority: &executionAuthority{}, module: &executionModule{verified: true}}
	fixture.groups = ga.New(fixture.routes, fixture.probe)
	fixture.executor = a.NewGroupExecutor(h.store, fixture.groups, fixture.routes, fixture.probe, fixture.authority, fixture.module)
	target, err := fixture.groups.Save(h.ctx, 0, gc.Input{Name: "synthetic", Purpose: purpose, BotInstanceID: 1, TelegramChatID: "-100"})
	if err != nil {
		t.Fatal(err)
	}
	service := &a.Service{Store: h.store, Groups: fixture.groups}
	fixture.task, err = service.PreviewGroup(h.ctx, 7, a.GroupPreflightInput{ReplacementInput: gc.ReplacementInput{TargetID: target.ID, ExpectedVersion: target.Version, ExpectedRouteVersion: target.RouteVersion, NewBotInstanceID: 1, NewTelegramChatID: "-200"}, RequestKey: "execute", Reason: "synthetic replacement"})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

// confirmExecution confirms fixture's immutable preflight and updates its parent CAS.
func confirmExecution(t *testing.T, fixture *executionFixture) {
	t.Helper()
	var err error
	fixture.task, err = fixture.executor.ConfirmGroup(fixture.ctx, fixture.task.ID, c.Confirmation{PreflightRef: fixture.task.PreflightRef, ExpectedVersion: fixture.task.Version, ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGroupExecutionCompletesRealPhases verifies real freeze/switch/check/release and receipts.
func TestGroupExecutionCompletesRealPhases(t *testing.T) {
	fixture := setupExecution(t, "support")
	confirmExecution(t, fixture)
	task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
	if err != nil || task.State != d.Completed {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	detail, err := fixture.groups.Detail(fixture.ctx, 1)
	if err != nil || detail.RecoveryTaskID != 0 || detail.TelegramChatID != -200 || detail.RouteVersion != 2 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	steps, err := fixture.store.ListSteps(fixture.ctx, task.ID, 0, 20)
	if err != nil || len(steps.Items) != 5 {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	for _, step := range steps.Items {
		if step.State != d.StepSucceeded {
			t.Fatal(step)
		}
	}
	checks := fixture.probe.checks
	if _, err = fixture.executor.ExecuteGroup(fixture.ctx, task.ID, 7); err != nil || fixture.module.calls != 1 || fixture.probe.checks != checks {
		t.Fatal("completed task repeated effects", err)
	}
}

// TestGroupConfirmationRevalidates tests destination permission, generation and actor drift.
func TestGroupConfirmationRevalidates(t *testing.T) {
	for _, reason := range []string{"permission", "generation", "actor", "version"} {
		t.Run(reason, func(t *testing.T) {
			fixture := setupExecution(t, "membership")
			switch reason {
			case "permission":
				fixture.probe.denied = true
			case "generation":
				fixture.probe.generation++
			case "actor":
				fixture.authority.denied = true
			case "version":
				if err := fixture.db.Model(&gd.Target{}).Where("id = 1").Update("version", 2).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := fixture.executor.ConfirmGroup(fixture.ctx, fixture.task.ID, c.Confirmation{PreflightRef: fixture.task.PreflightRef, ExpectedVersion: fixture.task.Version, ActorID: 7})
			if err == nil {
				t.Fatal("stale/unauthorized confirmation accepted")
			}
			stored, err := fixture.store.Get(fixture.ctx, fixture.task.ID)
			if err != nil || stored.Task.State != d.AwaitingConfirmation {
				t.Fatal(stored, err)
			}
		})
	}
}

// TestGroupMissingOrIncompleteModuleKeepsFence prevents false success for both purposes.
func TestGroupMissingOrIncompleteModuleKeepsFence(t *testing.T) {
	for _, mode := range []string{"support_missing", "membership_missing", "membership_leftover", "support_zero", "support_unknown", "membership_error"} {
		t.Run(mode, func(t *testing.T) {
			purpose := "support"
			if mode == "membership_missing" || mode == "membership_leftover" || mode == "membership_error" {
				purpose = "membership"
			}
			fixture := setupExecution(t, purpose)
			switch mode {
			case "support_missing", "membership_missing":
				fixture.executor.Modules = nil
			case "membership_leftover":
				fixture.module.remaining = 1
			case "support_zero":
				fixture.module.verified = false
			case "support_unknown":
				fixture.module.unknown = 1
			case "membership_error":
				fixture.module.err = errors.New("ambiguous synthetic response")
			}
			confirmExecution(t, fixture)
			task, err := fixture.executor.ExecuteGroup(fixture.ctx, fixture.task.ID, 7)
			if err == nil || task.State == d.Completed {
				t.Fatal("incomplete module claimed complete", task, err)
			}
			target, err := fixture.routes.Get(fixture.ctx, 1)
			if err != nil || target.RecoveryTaskID != fixture.task.ID {
				t.Fatal("incomplete module released fence", target, err)
			}
			calls := fixture.module.calls
			if _, err = fixture.executor.ExecuteGroup(fixture.ctx, task.ID, 7); !errors.Is(err, a.ErrRecoveryReview) || fixture.module.calls != calls {
				t.Fatal("blind retry", err)
			}
		})
	}
}
