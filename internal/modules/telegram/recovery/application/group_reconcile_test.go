package application

import (
	"context"
	"errors"
	"testing"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// entryGroupProbe exposes only the already persisted synthetic checked route.
type entryGroupProbe struct {
	GroupExecutionGroups
	detail groups.Detail
}

// Detail returns the fixture's current route for the requested logical target.
func (probe *entryGroupProbe) Detail(context.Context, uint) (*groups.Detail, error) {
	return &probe.detail, nil
}

// entryIdentityProbe supplies a trusted running generation without any API call.
type entryIdentityProbe struct{}

// Instance returns the synthetic bot identity used by the persisted observation.
func (entryIdentityProbe) Instance(context.Context, uint) (groups.Identity, error) {
	return groups.Identity{TelegramID: 99, Generation: 7, Running: true}, nil
}

// entryModuleProbe returns configured evidence without mutating any route.
type entryModuleProbe struct{ result c.GroupRecoveryResult }

// RecoverGroup returns only the fixture's explicit result, preserving bad proofs.
func (probe entryModuleProbe) RecoverGroup(context.Context, c.GroupRecoveryRequest) (c.GroupRecoveryResult, error) {
	return probe.result, nil
}

// TestRecoverModuleEntryProofIsMembershipAndRouteBound verifies release gating:
// old liabilities may remain only for membership; unknown and substituted proof fail.
func TestRecoverModuleEntryProofIsMembershipAndRouteBound(t *testing.T) {
	for _, scenario := range []string{"entry", "support", "unknown", "task", "target", "route", "missing", "stale_destination"} {
		t.Run(scenario, func(t *testing.T) {
			probe := &entryGroupProbe{}
			current := &probe.detail
			current.ID, current.RecoveryTaskID = 2, 3
			current.Version, current.RouteVersion = 9, 5
			current.BotInstanceID, current.TelegramChatID, current.Purpose = 1, -20, "membership"
			current.ObservationSeq = 1
			observed, fresh := time.Now().UTC(), time.Now().UTC().Add(time.Minute)
			current.FreshUntil = &fresh
			current.Observation.State, current.Observation.IsMember = "ready", true
			current.Observation.Generation, current.Observation.BotTelegramID = 7, 99
			current.Observation.RouteVersion, current.Observation.Sequence = 5, 1
			current.Observation.ObservedAt = &observed
			snapshot := d.Snapshot{}
			snapshot.New.TargetID, snapshot.New.BotID, snapshot.New.ChatID = 2, 1, -20
			snapshot.Old.ChatID = -10
			snapshot.ExpectedOld.Config, snapshot.ExpectedOld.Route, snapshot.ExpectedNew.Generation = 7, 4, 7
			result := c.GroupRecoveryResult{TaskID: 3, TargetID: 2, RouteVersion: 5, EntryReady: true, Checkpointed: true, Remaining: 2}
			switch scenario {
			case "support":
				current.Purpose = "support"
			case "unknown":
				result.Unknown = 1
			case "task":
				result.TaskID++
			case "target":
				result.TargetID++
			case "route":
				result.RouteVersion++
			case "missing":
				result.EntryReady = false
			case "stale_destination":
				current.TelegramChatID = -30
			}
			executor := &GroupExecutor{Groups: probe, Identity: entryIdentityProbe{}, Modules: entryModuleProbe{result: result}}
			err := executor.recoverModule(context.Background(), 3, snapshot, 7)
			if scenario == "entry" && err != nil || scenario != "entry" && err == nil {
				t.Fatalf("incorrect release gate for %s: %v", scenario, err)
			}
			if scenario == "unknown" && !errors.Is(err, ErrRecoveryReview) {
				t.Fatalf("unknown bypassed review: %v", err)
			}
		})
	}
}
