package application

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// replacementStore provides only the reads exercised by preflight; writes would panic.
type replacementStore struct {
	c.Store
	target   d.Target
	occupied []d.Target
}

// Get returns a copy so source snapshots cannot mutate while the probe executes.
func (store *replacementStore) Get(context.Context, uint) (*d.Target, error) {
	copy := store.target
	return &copy, nil
}

// List returns exact physical occupancy for the requested chat.
func (store *replacementStore) List(_ context.Context, filter c.Filter) ([]d.Target, int64, error) {
	rows := []d.Target{}
	for _, row := range store.occupied {
		if row.TelegramChatID == filter.ChatID {
			rows = append(rows, row)
		}
	}
	return rows, int64(len(rows)), nil
}

// replacementProbe simulates a read-only platform lookup and an optional concurrent update.
type replacementProbe struct {
	identity c.Identity
	result   c.ProbeResult
	after    func()
	calls    int
}

// Instance returns the current runtime identity, without exposing credentials.
func (probe *replacementProbe) Instance(context.Context, uint) (c.Identity, error) {
	return probe.identity, nil
}

// Check records the lookup and applies a concurrent change before returning its observation.
func (probe *replacementProbe) Check(context.Context, uint, int64) c.ProbeResult {
	probe.calls++
	if probe.after != nil {
		probe.after()
	}
	return probe.result
}

// TestReplacementPreviewRejectsConflictsAndLateChanges covers source CAS, physical occupancy,
// exact destination identities, permissions and generation drift without performing writes.
func TestReplacementPreviewRejectsConflictsAndLateChanges(t *testing.T) {
	for _, scenario := range []string{"valid", "bot_only", "noncanonical_chat", "same_route", "occupied", "stale_version", "stopped", "no_permission", "not_member", "network", "changed_source", "changed_generation", "late_occupancy"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
			store := &replacementStore{target: d.Target{ID: 9, Version: 3, RouteVersion: 2, Purpose: "membership", BotInstanceID: 1, TelegramChatID: -100}}
			probe := &replacementProbe{identity: c.Identity{TelegramID: 99, Generation: 7, Running: true}, result: c.ProbeResult{Observation: d.Observation{BotTelegramID: 99, Generation: 7, ChatType: "supergroup", IsMember: true, MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true}}}
			input := c.ReplacementInput{TargetID: 9, ExpectedVersion: 3, ExpectedRouteVersion: 2, NewBotInstanceID: 1, NewTelegramChatID: "-200"}
			switch scenario {
			case "bot_only":
				input.NewBotInstanceID, input.NewTelegramChatID = 2, "-100"
				store.occupied = []d.Target{store.target}
			case "noncanonical_chat":
				input.NewTelegramChatID = "-0200"
			case "same_route":
				input.NewTelegramChatID = "-100"
			case "occupied":
				store.occupied = []d.Target{{ID: 10, TelegramChatID: -200}}
			case "stale_version":
				input.ExpectedVersion--
			case "stopped":
				probe.identity.Running = false
			case "no_permission":
				probe.result.Observation.CanRestrictMembers = false
			case "not_member":
				probe.result.Observation.IsMember = false
			case "network":
				probe.result.ErrorClass = "network"
			case "changed_source":
				probe.after = func() { store.target.Version++ }
			case "changed_generation":
				probe.after = func() { probe.identity.Generation++ }
			case "late_occupancy":
				probe.after = func() { store.occupied = []d.Target{{ID: 10, TelegramChatID: -200}} }
			}
			service := New(store, probe)
			service.Now = func() time.Time { return now }
			preview, err := service.PreviewReplacement(context.Background(), input)
			valid := scenario == "valid" || scenario == "bot_only"
			if (err == nil) != valid {
				t.Fatalf("preview=%+v error=%v", preview, err)
			}
			if valid && (!preview.ExpiresAt.Equal(now.Add(Freshness)) || preview.Source.TelegramChatID != -100) {
				t.Fatalf("incorrect snapshot: %+v", preview)
			}
			if scenario == "occupied" && (!errors.Is(err, c.ErrConflict) || probe.calls != 0) {
				t.Fatal("occupied destination reached platform")
			}
		})
	}
}
