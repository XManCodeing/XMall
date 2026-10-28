package cutover

import (
	"context"
	"testing"
)

type readyFake struct {
	value   Inventory
	err     error
	revoked int
}

func (r *readyFake) Inspect(context.Context, uint) (Inventory, error) {
	if r.value.Scope == "" {
		r.value.Scope = "test"
		r.value.Checked = true
	}
	return r.value, r.err
}
func (r *readyFake) RevokeCredentials(context.Context, uint) error { r.revoked++; return r.err }

type storeFake struct{ i Instance }

func (s *storeFake) Target(context.Context, uint) (Instance, error) { return s.i, nil }
func (s *storeFake) ByChannel(_ context.Context, id uint) (*Instance, error) {
	if id != s.i.ChannelID {
		return nil, nil
	}
	i := s.i
	return &i, nil
}
func (s *storeFake) Switch(_ context.Context, i Instance, mode string, enabled bool) error {
	if s.i != i {
		return ErrBlocked
	}
	s.i.Mode = mode
	s.i.Enabled = enabled
	s.i.Version++
	return nil
}

type networkFake struct {
	hook    bool
	onCheck func()
	deletes int
	drop    bool
}

func (n *networkFake) HasWebhook(context.Context, uint) (bool, error) {
	if n.onCheck != nil {
		n.onCheck()
	}
	return n.hook, nil
}
func (n *networkFake) DeleteWebhook(_ context.Context, _ uint, drop bool) error {
	n.deletes++
	n.drop = drop
	n.hook = false
	return nil
}
func TestPreflightAndConfirmedCutover(t *testing.T) {
	ctx := context.Background()
	store := &storeFake{Instance{BotID: 4, ChannelID: 9, Version: 1, Mode: "external"}}
	network := &networkFake{hook: true}
	s := &Service{Store: store, Network: network, Readiness: &readyFake{}}
	r := Request{BotID: 4, ChannelID: 9, Version: 1}
	p, err := s.Preflight(ctx, r)
	if err != nil || p.NetworkChecked || network.deletes != 0 {
		t.Fatal(p, err)
	}
	if s.Apply(ctx, r) == nil || store.i.Mode != "external" {
		t.Fatal("unconfirmed takeover")
	}
	r.ConfirmExternalStopped = true
	r.ConfirmRevokeCredentials = true
	if s.Apply(ctx, r) == nil || store.i.Mode != "cutover_pending" || network.deletes != 0 {
		t.Fatal("webhook must require confirmation")
	}
	allowed, err := s.LegacyAllowed(ctx, 9)
	if err != nil || allowed {
		t.Fatal("pending channel not fenced")
	}
	allowed, err = s.LegacyAllowed(ctx, 10)
	if err != nil || !allowed {
		t.Fatal("unrelated channel fenced")
	}
	r.Version = store.i.Version
	r.ConfirmDeleteWebhook = true
	if err = s.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	if store.i.Mode != "internal" || store.i.Enabled || network.drop || network.deletes != 1 {
		t.Fatal("unsafe takeover")
	}
	r.Version = store.i.Version
	store.i.Enabled = true
	if err = s.Apply(ctx, r); err != nil || !store.i.Enabled || network.deletes != 1 {
		t.Fatal("completed takeover is not an idempotent no-op", err)
	}
	r.Version--
	if err = s.Apply(ctx, r); err == nil {
		t.Fatal("stale request accepted")
	}
}
func TestCutoverRevalidatesBeforeWebhookDeletion(t *testing.T) {
	store := &storeFake{Instance{BotID: 4, ChannelID: 9, Version: 1, Mode: "external"}}
	network := &networkFake{hook: true, onCheck: func() { store.i.Version++ }}
	s := &Service{Store: store, Network: network, Readiness: &readyFake{}}
	err := s.Apply(context.Background(), Request{BotID: 4, ChannelID: 9, Version: 1, ConfirmExternalStopped: true, ConfirmRevokeCredentials: true, ConfirmDeleteWebhook: true})
	if err == nil || network.deletes != 0 || store.i.Mode != "cutover_pending" {
		t.Fatal("concurrent config allowed webhook deletion", err)
	}
}

func TestApplyRequiresCompleteInventoryAndExplicitIsolation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		readiness        *readyFake
		revoke, archived bool
		blocked          bool
	}{
		{"missing", nil, true, true, true},
		{"orders", &readyFake{value: Inventory{UnfinishedOrders: 1}}, true, true, true},
		{"pending", &readyFake{value: Inventory{Pending: 1}}, true, true, true},
		{"retry", &readyFake{value: Inventory{Retry: 1}}, true, true, true},
		{"scheduled", &readyFake{value: Inventory{Scheduled: 1}}, true, true, true},
		{"active", &readyFake{value: Inventory{Active: 1}}, true, true, true},
		{"archive_unconfirmed", &readyFake{value: Inventory{Archived: 1}}, true, false, true},
		{"revocation_unconfirmed", &readyFake{}, false, true, true},
		{"archive_confirmed", &readyFake{value: Inventory{Archived: 1}}, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &storeFake{Instance{BotID: 4, ChannelID: 9, Version: 1, Mode: "external"}}
			s := &Service{Store: store, Network: &networkFake{}}
			if tc.readiness != nil {
				s.Readiness = tc.readiness
			}
			r := Request{BotID: 4, ChannelID: 9, Version: 1, ConfirmExternalStopped: true, ConfirmRevokeCredentials: tc.revoke, ConfirmArchivedIsolation: tc.archived}
			p, err := s.Preflight(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if tc.readiness == nil && (p.Inventory.Checked || len(p.Blockers) == 0) {
				t.Fatal("missing inventory reported ready")
			}
			err = s.Apply(context.Background(), r)
			if (err != nil) != tc.blocked {
				t.Fatal(err)
			}
			if tc.blocked && store.i.Mode != "external" {
				t.Fatal("blocked apply changed instance")
			}
			if !tc.blocked && tc.readiness.revoked != 1 {
				t.Fatal("credential not revoked")
			}
		})
	}
}

type changingReadiness struct {
	calls     int
	revokeErr error
}

func (r *changingReadiness) Inspect(context.Context, uint) (Inventory, error) {
	r.calls++
	v := Inventory{Checked: true, Scope: "test"}
	if r.calls > 1 {
		v.Pending = 1
	}
	return v, nil
}
func (r *changingReadiness) RevokeCredentials(context.Context, uint) error { return r.revokeErr }
func TestInventoryRaceKeepsPendingFenceBeforeNetwork(t *testing.T) {
	store := &storeFake{Instance{BotID: 4, ChannelID: 9, Version: 1, Mode: "external"}}
	network := &networkFake{onCheck: func() { t.Fatal("network called before queue drained") }}
	s := &Service{Store: store, Network: network, Readiness: &changingReadiness{}}
	if err := s.Apply(context.Background(), Request{BotID: 4, ChannelID: 9, Version: 1, ConfirmExternalStopped: true, ConfirmRevokeCredentials: true}); err == nil || store.i.Mode != "cutover_pending" {
		t.Fatal("race did not fence", err)
	}
	s.Readiness = &readyFake{}
	if err := s.Apply(context.Background(), Request{BotID: 4, ChannelID: 9, Version: 1, ConfirmExternalStopped: true, ConfirmRevokeCredentials: true}); err == nil {
		t.Fatal("stale resume accepted")
	}
	network.onCheck = nil
	if err := s.Apply(context.Background(), Request{BotID: 4, ChannelID: 9, Version: 2, ConfirmExternalStopped: true, ConfirmRevokeCredentials: true}); err != nil {
		t.Fatal("resume failed", err)
	}
}

func TestRejectUnrelatedTarget(t *testing.T) {
	s := &Service{Store: &storeFake{Instance{BotID: 4, ChannelID: 9, Version: 1, Mode: "external"}}}
	if _, err := s.Preflight(context.Background(), Request{BotID: 4, ChannelID: 8, Version: 1}); err == nil {
		t.Fatal("accepted wrong association")
	}
}
