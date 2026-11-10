package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// chatIDProbe maps requested IDs to read-only observations and records exact calls.
type chatIDProbe struct {
	c.Probe
	results map[int64]c.ProbeResult
	calls   []int64
	bots    []uint
}

// Instance returns a synthetic identity for bot without exposing credentials.
func (probe *chatIDProbe) Instance(context.Context, uint) (c.Identity, error) {
	return c.Identity{Running: true}, nil
}

// Check returns the configured result for chat or a definitive inaccessible result.
func (probe *chatIDProbe) Check(_ context.Context, bot uint, chat int64) c.ProbeResult {
	probe.bots = append(probe.bots, bot)
	probe.calls = append(probe.calls, chat)
	if result, exists := probe.results[chat]; exists {
		return result
	}
	return c.ProbeResult{ErrorClass: "resource_inaccessible"}
}

// TestResolveChatID verifies exact IDs, arithmetic offsets, ambiguity and fail-closed errors.
func TestResolveChatID(t *testing.T) {
	group := c.ProbeResult{Observation: d.Observation{ChatType: "group"}}
	supergroup := c.ProbeResult{Observation: d.Observation{ChatType: "supergroup", Title: "Telegram群名"}}
	for _, test := range []struct {
		name, input string
		results     map[int64]c.ProbeResult
		want        int64
		err         error
	}{
		{"basic", "1234", map[int64]c.ProbeResult{-1234: group}, -1234, nil},
		{"supergroup", "12453021421", map[int64]c.ProbeResult{-1012453021421: supergroup}, -1012453021421, nil},
		{"ten_digits", "1245302142", map[int64]c.ProbeResult{-1001245302142: supergroup}, -1001245302142, nil},
		{"omitted_minus", "1001245302142", map[int64]c.ProbeResult{-1001245302142: supergroup}, -1001245302142, nil},
		{"trimmed", " 1234 ", map[int64]c.ProbeResult{-1234: group}, -1234, nil},
		{"explicit", "-1001245302142", nil, -1001245302142, nil},
		{"min_int64", "-9223372036854775808", nil, -9223372036854775808, nil},
		{"ambiguous", "1234", map[int64]c.ProbeResult{-1234: group, -1000000001234: supergroup}, 0, c.ErrChatAmbiguous},
		{"channel", "1234", map[int64]c.ProbeResult{-1000000001234: {Observation: d.Observation{ChatType: "channel"}}}, 0, c.ErrChatUnresolved},
		{"private", "1234", map[int64]c.ProbeResult{-1234: {Observation: d.Observation{ChatType: "private"}}}, 0, c.ErrChatUnresolved},
		{"wrong_type", "1234", map[int64]c.ProbeResult{-1234: supergroup}, 0, c.ErrChatUnresolved},
		{"not_found", "1234", nil, 0, c.ErrChatUnresolved},
		{"network_after_match", "1234", map[int64]c.ProbeResult{-1234: group, -1000000001234: {ErrorClass: "network"}}, 0, c.ErrChatUnresolved},
		{"rate_limit", "1234", map[int64]c.ProbeResult{-1234: {ErrorClass: "rate_limited"}}, 0, c.ErrChatUnresolved},
		{"large_positive", "9223372036854775807", nil, 0, c.ErrChatUnresolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &chatIDProbe{results: test.results}
			got, _, err := New(nil, probe).resolveChatID(context.Background(), 1, test.input)
			if got != test.want || !errors.Is(err, test.err) {
				t.Fatalf("got %d, %v; want %d, %v", got, err, test.want, test.err)
			}
			if test.input[0] == '-' && len(probe.calls) != 0 {
				t.Fatal("explicit ID unexpectedly queried Telegram")
			}
		})
	}
	for _, input := range []string{"", "0", "-0", "+123", "01", "-01", "1.2", "1e3", "@group", "https://t.me/c/1234", "9223372036854775808", "-9223372036854775809"} {
		probe := &chatIDProbe{}
		if _, _, err := New(nil, probe).resolveChatID(context.Background(), 1, input); !errors.Is(err, c.ErrInvalid) || len(probe.calls) != 0 {
			t.Fatalf("invalid input %q reached Telegram or was accepted: %v", input, err)
		}
	}
}

// chatIDStore captures canonical persistence without altering existing store CAS behavior.
type chatIDStore struct {
	c.Store
	target d.Target
	writes int
}

// Save captures target and assigns a synthetic ID; expected versions remain the service's responsibility.
func (store *chatIDStore) Save(_ context.Context, target *d.Target, _, _ uint64) error {
	store.writes++
	target.ID = 1
	store.target = *target
	return nil
}

// Get returns the last persisted target for Detail.
func (store *chatIDStore) Get(context.Context, uint) (*d.Target, error) { return &store.target, nil }

// History returns empty histories for this isolated save test.
func (store *chatIDStore) History(context.Context, uint) ([]d.Route, []d.Operation, error) {
	return nil, nil, nil
}

// checkedChatIDStore adds checked writes without changing the basic capability-gate fixture.
type checkedChatIDStore struct{ chatIDStore }

// SaveChecked captures target and CAS arguments through the ordinary test store.
func (store *checkedChatIDStore) SaveChecked(ctx context.Context, target *d.Target, version, route, _ uint64) error {
	return store.Save(ctx, target, version, route)
}

// TestCreateUsesTelegramTitle verifies missing/forged names, both ID forms and
// disabled, preview and enabled save paths with exactly one matching probe per request.
func TestCreateUsesTelegramTitle(t *testing.T) {
	for _, chat := range []string{"-1000000001234", "1234"} {
		for _, supplied := range []string{"", "Forged", strings.Repeat("x", 121)} {
			for _, operation := range []string{"disabled", "preview", "enabled"} {
				t.Run(chat+"/"+supplied+"/"+operation, func(t *testing.T) {
					store := &checkedChatIDStore{}
					const title = "  Telegram真实群名  "
					probe := &chatIDProbe{results: map[int64]c.ProbeResult{-1000000001234: {Observation: d.Observation{Title: title, ChatType: "supergroup", IsMember: true, MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true}}}}
					service := New(store, probe)
					input := c.Input{Name: supplied, BotInstanceID: 9, Purpose: "membership", TelegramChatID: chat, Enabled: operation != "disabled"}
					var view *c.View
					var err error
					if operation == "preview" {
						view, err = service.PreviewEnable(context.Background(), 0, input)
					} else {
						var detail *c.Detail
						detail, err = service.Save(context.Background(), 0, input)
						if detail != nil {
							view = &detail.View
						}
					}
					if err != nil || view.Name != title || view.TelegramChatID != -1000000001234 || view.Enabled != input.Enabled {
						t.Fatalf("title/route/enable mismatch: %+v, %v", view, err)
					}
					wantCalls, wantWrites := 1, 1
					if chat == "1234" {
						wantCalls = 2
					}
					if operation == "preview" {
						wantWrites = 0
					}
					if len(probe.calls) != wantCalls || store.writes != wantWrites {
						t.Fatalf("unexpected probes/writes: %v/%d", probe.calls, store.writes)
					}
					for _, bot := range probe.bots {
						if bot != input.BotInstanceID {
							t.Fatalf("probed wrong bot: %d", bot)
						}
					}
				})
			}
		}
	}
}

// TestCreateRejectsUnverifiedTitle ensures a negative ID cannot bypass title/type
// checks and the existing schema limit is enforced by characters without truncation.
func TestCreateRejectsUnverifiedTitle(t *testing.T) {
	for _, test := range []struct {
		name, title, kind, failure string
		want                       error
	}{
		{"network", "Fake", "supergroup", "network", c.ErrChatUnresolved},
		{"inaccessible", "Fake", "supergroup", "resource_inaccessible", c.ErrChatUnresolved},
		{"empty", "", "group", "", c.ErrChatUnresolved},
		{"whitespace", " \t ", "group", "", c.ErrChatUnresolved},
		{"channel", "Channel", "channel", "", c.ErrChatUnresolved},
		{"private", "Private", "private", "", c.ErrChatUnresolved},
		{"over_limit", strings.Repeat("群", 121), "supergroup", "", c.ErrInvalid},
		{"telegram_limit", strings.Repeat("x", 128), "group", "", c.ErrInvalid},
		{"at_limit", strings.Repeat("群", 120), "group", "", nil},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(test.name, func(t *testing.T) {
				store := &checkedChatIDStore{}
				probe := &chatIDProbe{results: map[int64]c.ProbeResult{-123: {Observation: d.Observation{Title: test.title, ChatType: test.kind}, ErrorClass: test.failure}}}
				result, err := New(store, probe).Save(context.Background(), 0, c.Input{Name: "Forged", BotInstanceID: 1, Purpose: "membership", TelegramChatID: "-123", Enabled: enabled})
				if !errors.Is(err, test.want) || (err != nil && store.writes != 0) {
					t.Fatalf("invalid creation wrote %d rows: %v", store.writes, err)
				}
				if err == nil && (store.writes != 1 || result.Name != test.title) {
					t.Fatalf("valid title was truncated or not saved: %+v", result)
				}
			})
		}
	}
}

// TestSaveRechecksTitleAfterPreview proves the preview cannot authorize a later
// create: Telegram may rename the chat or become inaccessible before submission.
func TestSaveRechecksTitleAfterPreview(t *testing.T) {
	store := &checkedChatIDStore{}
	probe := &chatIDProbe{results: map[int64]c.ProbeResult{-123: {Observation: d.Observation{ChatType: "group", Title: "Preview title"}}}}
	service := New(store, probe)
	input := c.Input{BotInstanceID: 1, Purpose: "membership", TelegramChatID: "-123", Enabled: true}
	preview, err := service.PreviewEnable(context.Background(), 0, input)
	if err != nil || preview.Name != "Preview title" || store.writes != 0 {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	probe.results[-123] = c.ProbeResult{Observation: d.Observation{ChatType: "group", Title: "Saving title"}}
	saved, err := service.Save(context.Background(), 0, input)
	if err != nil || saved.Name != "Saving title" || len(probe.calls) != 2 {
		t.Fatalf("save reused stale preview: %+v, %v", saved, err)
	}
	probe.results[-123] = c.ProbeResult{ErrorClass: "network"}
	if _, err = service.Save(context.Background(), 0, input); !errors.Is(err, c.ErrChatUnresolved) || store.writes != 1 {
		t.Fatalf("failed recheck created target: %v", err)
	}
}

// TestEditKeepsOperatorName proves existing targets remain independently named,
// while enabled edits retain their fresh check and disabled edits need no title read.
func TestEditKeepsOperatorName(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := &checkedChatIDStore{chatIDStore: chatIDStore{target: d.Target{ID: 1, Name: "Legacy", BotInstanceID: 1, TelegramChatID: -123, Purpose: "membership", Version: 3, RouteVersion: 2}}}
		probe := &chatIDProbe{results: map[int64]c.ProbeResult{-123: {Observation: d.Observation{Title: "Renamed by Telegram", ChatType: "group"}}}}
		input := c.Input{Name: strings.Repeat("群", 120), BotInstanceID: 1, TelegramChatID: "-123", Purpose: "membership", Enabled: enabled, ExpectedVersion: 3, ExpectedRouteVersion: 2}
		saved, err := New(store, probe).Save(context.Background(), 1, input)
		if err != nil || saved.Name != input.Name || store.writes != 1 || saved.RouteVersion != 2 {
			t.Fatalf("edit renamed target: %+v, %v", saved, err)
		}
		wantCalls := 0
		if enabled {
			wantCalls = 1
		}
		if len(probe.calls) != wantCalls {
			t.Fatalf("edit probe behavior changed: %v", probe.calls)
		}
	}
}

// TestSaveResolvesPositiveChatID proves normalized persistence and zero writes on lookup failure.
func TestSaveResolvesPositiveChatID(t *testing.T) {
	store := &chatIDStore{}
	probe := &chatIDProbe{results: map[int64]c.ProbeResult{-1012453021421: {Observation: d.Observation{ChatType: "supergroup", Title: "Telegram群名"}}}}
	service := New(store, probe)
	input := c.Input{Name: "Group", BotInstanceID: 1, Purpose: "membership", TelegramChatID: "12453021421"}
	result, err := service.Save(context.Background(), 0, input)
	if err != nil || result.TelegramChatID != -1012453021421 || store.writes != 1 || result.Enabled || result.Name != "Telegram群名" || len(probe.calls) != 2 {
		t.Fatalf("save: %+v, %v", result, err)
	}
	probe.results = nil
	if _, err = service.Save(context.Background(), 0, input); !errors.Is(err, c.ErrChatUnresolved) || store.writes != 1 {
		t.Fatalf("unverified ID persisted: %v", err)
	}
	probe.results = map[int64]c.ProbeResult{-1012453021421: {Observation: d.Observation{ChatType: "supergroup", Title: "Telegram群名"}}}
	input.Enabled = true
	if _, err = service.Save(context.Background(), 0, input); !errors.Is(err, c.ErrCapability) || store.writes != 1 {
		t.Fatalf("resolution bypassed capability gate: %v", err)
	}
	input.Enabled = false
	store.target.Version = 2
	if _, err = service.Save(context.Background(), 1, input); !errors.Is(err, c.ErrConflict) || store.writes != 1 {
		t.Fatalf("resolution bypassed CAS: %v", err)
	}
}
