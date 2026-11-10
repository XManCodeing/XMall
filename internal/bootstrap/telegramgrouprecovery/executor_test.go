package telegramgrouprecovery

import (
	"context"
	"errors"
	"testing"

	domain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
)

// moduleCoordinatorStore executes the narrow proof using one isolated transaction.
type moduleCoordinatorStore struct {
	recovery.Store
	db       *gorm.DB
	snapshot domain.Snapshot
	calls    int
}

// RetryGroupModule supplies its persisted synthetic snapshot, never caller JSON.
func (store *moduleCoordinatorStore) RetryGroupModule(ctx context.Context, input recovery.LocalGroupRetry, proof func(*gorm.DB, domain.Snapshot) error) (*domain.Task, error) {
	store.calls++
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return proof(tx, store.snapshot) })
	return &domain.Task{ID: input.TaskID}, err
}

// moduleCoordinatorProbe captures the exact immutable route passed to the evidence port.
type moduleCoordinatorProbe struct {
	receiptProbe
	checked membership.Request
}

// CheckGroupResume reads on the coordinator transaction and never the global connection.
func (probe *moduleCoordinatorProbe) CheckGroupResume(ctx context.Context, tx *gorm.DB, input membership.Request) error {
	probe.checked = input
	var count int64
	return tx.WithContext(ctx).Table("telegram_group_targets").Count(&count).Error
}

// TestModuleCoordinatorRejectsRevocationAndRouteDrift exercises the production bridge.
func TestModuleCoordinatorRejectsRevocationAndRouteDrift(t *testing.T) {
	for _, mode := range []string{"valid", "revoked", "purpose", "version", "cross_bot"} {
		t.Run(mode, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			if err := db.Exec("CREATE TABLE telegram_group_targets(id integer, version integer, purpose text)").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO telegram_group_targets VALUES (2,9,'membership')").Error; err != nil {
				t.Fatal(err)
			}
			snapshot := domain.Snapshot{}
			snapshot.Old.TargetID, snapshot.New.TargetID = 2, 2
			snapshot.Old.BotID, snapshot.New.BotID = 1, 1
			snapshot.Old.ChatID, snapshot.New.ChatID = -10, -20
			snapshot.ExpectedOld.Config, snapshot.ExpectedOld.Route, snapshot.ExpectedNew.Generation = 7, 4, 6
			if mode == "cross_bot" {
				snapshot.New.BotID = 9
			}
			if mode == "purpose" {
				if err := db.Exec("UPDATE telegram_group_targets SET purpose='support'").Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "version" {
				snapshot.ExpectedOld.Config++
			}
			store := &moduleCoordinatorStore{db: db, snapshot: snapshot}
			probe := &moduleCoordinatorProbe{}
			executor := &Executor{Store: store, Membership: probe, Authorize: func(context.Context, uint, string) error {
				if mode == "revoked" {
					return recovery.ErrInvalid
				}
				return nil
			}}
			_, err = executor.ResumeGroupModule(context.Background(), recovery.LocalGroupRetry{TaskID: 3, TaskVersion: 1, StepID: 4, StepVersion: 1, ActorID: 7})
			if mode != "valid" {
				if err == nil || probe.checked.TaskID != 0 {
					t.Fatal("unsafe bridge allowed proof", err)
				}
				if mode == "revoked" && store.calls != 0 {
					t.Fatal("revoked actor reached transaction")
				}
				return
			}
			if err != nil || probe.checked != (membership.Request{TaskID: 3, ActorID: 7, TargetID: 2, BotID: 1, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 6, Purpose: "membership"}) {
				t.Fatal("incorrect snapshot translation", probe.checked, err)
			}
		})
	}
}

// receiptProbe returns explicit test evidence and captures the exact translated request.
type receiptProbe struct {
	request membership.Request
	result  membership.Result
	err     error
}

// RecoverRoute captures request and returns the configured evidence without platform or database I/O.
func (probe *receiptProbe) RecoverRoute(_ context.Context, request membership.Request) (membership.Result, error) {
	probe.request = request
	return probe.result, probe.err
}

// supportReceiptProbe captures support routing without invoking membership.
type supportReceiptProbe struct {
	request recovery.GroupRecoveryRequest
}

// RecoverGroup returns a durable partial checkpoint bound to the input route.
func (probe *supportReceiptProbe) RecoverGroup(_ context.Context, request recovery.GroupRecoveryRequest) (recovery.GroupRecoveryResult, error) {
	probe.request = request
	return recovery.GroupRecoveryResult{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion, Checkpointed: true, Remaining: 3}, nil
}

// TestSupportReceiptDispatch proves the support port is independent and cannot
// accidentally fall back to the membership scanner or lose the execution actor.
func TestSupportReceiptDispatch(t *testing.T) {
	probe := &supportReceiptProbe{}
	member := &receiptProbe{}
	executor := &Executor{Support: probe, Membership: member}
	request := recovery.GroupRecoveryRequest{TaskID: 1, ActorID: 7, TargetID: 2, BotID: 3, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 6, Purpose: "support"}
	result, err := executor.RecoverGroup(context.Background(), request)
	if err != nil || probe.request != request || result.Remaining != 3 || !result.Checkpointed || result.Verified || member.request.TaskID != 0 {
		t.Fatalf("support dispatch altered evidence: %+v %v", result, err)
	}
}

// entryReceiptProbe offers the optional production path without removing legacy support.
type entryReceiptProbe struct {
	receiptProbe
	entryRequest membership.Request
}

// SwitchEntry captures the exact entry request and returns separate entry proof.
func (probe *entryReceiptProbe) SwitchEntry(_ context.Context, request membership.Request) (membership.Result, error) {
	probe.entryRequest = request
	return membership.Result{TaskID: request.TaskID, TargetID: request.TargetID, RouteVersion: request.NewRouteVersion, EntryReady: true, Remaining: 2}, nil
}

// TestMembershipPrefersEntrySwitch proves the production bridge never invokes
// legacy admission/cleanup when the optional entry-only implementation is present.
func TestMembershipPrefersEntrySwitch(t *testing.T) {
	probe := &entryReceiptProbe{}
	executor := &Executor{Membership: probe}
	request := recovery.GroupRecoveryRequest{TaskID: 3, ActorID: 7, TargetID: 2, BotID: 1, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 6, Purpose: "membership"}
	result, err := executor.RecoverGroup(context.Background(), request)
	if err != nil || probe.entryRequest != membership.Request(request) || probe.request.TaskID != 0 || !result.EntryReady || result.Verified || result.Remaining != 2 {
		t.Fatalf("entry switch dispatch lost proof: %+v %v", result, err)
	}
}

// TestMembershipReceiptTranslation preserves route ownership, incomplete evidence and errors unchanged.
func TestMembershipReceiptTranslation(t *testing.T) {
	request := recovery.GroupRecoveryRequest{TaskID: 1, TargetID: 2, BotID: 3, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 6, Purpose: "membership"}
	failure := errors.New("synthetic scan failure")
	for _, scenario := range []string{"empty", "verified", "remaining", "unknown", "error"} {
		t.Run(scenario, func(t *testing.T) {
			probe := &receiptProbe{}
			switch scenario {
			case "verified":
				probe.result = membership.Result{TaskID: 1, TargetID: 2, RouteVersion: 5, Verified: true}
			case "remaining":
				probe.result.Remaining = 3
			case "unknown":
				probe.result.Unknown = 4
			case "error":
				probe.err = failure
			}
			executor := &Executor{Membership: probe}
			result, err := executor.RecoverGroup(context.Background(), request)
			if probe.request != membership.Request(request) || result != recovery.GroupRecoveryResult(probe.result) || !errors.Is(err, probe.err) {
				t.Fatalf("evidence altered: result=%+v error=%v", result, err)
			}
		})
	}
	for _, executor := range []*Executor{nil, {}, {Membership: &receiptProbe{}}} {
		unsupported := request
		unsupported.Purpose = "support"
		result, err := executor.RecoverGroup(context.Background(), unsupported)
		if !errors.Is(err, app.ErrRecoveryIncomplete) || result.Verified {
			t.Fatal("unsupported purpose accepted", result, err)
		}
	}
}
