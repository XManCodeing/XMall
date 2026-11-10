package gormstore_test

import (
	"context"
	"errors"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

type probe struct {
	result c.ProbeResult
	check  func()
}

func (*probe) Instance(context.Context, uint) (c.Identity, error) {
	return c.Identity{TelegramID: 123, Running: true, Generation: 1}, nil
}
func (p *probe) Check(context.Context, uint, int64) c.ProbeResult {
	if p.check != nil {
		p.check()
	}
	return p.result
}
func setup(t *testing.T) (*a.Service, *s.Store, *gorm.DB, *probe) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { sql.Close() })
	if err = db.AutoMigrate(&d.Target{}, &d.Route{}, &d.Operation{}, &d.ModuleConfig{}); err != nil {
		t.Fatal(err)
	}
	store := s.New(db)
	p := &probe{result: c.ProbeResult{Observation: d.Observation{BotTelegramID: 123, Generation: 1, ChatType: "supergroup", Title: "Support", IsMember: true, IsForum: true, MemberStatus: "administrator", CanManageTopics: true, CanInviteUsers: true, CanRestrictMembers: true}}}
	return a.New(store, p), store, db, p
}
func create(t *testing.T, svc *a.Service) *c.Detail {
	t.Helper()
	v, e := svc.Save(context.Background(), 0, c.Input{Name: "support", Purpose: "support", BotInstanceID: 1, TelegramChatID: "-1001234567890123"})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestChecksSingleConnectionAndCapabilities(t *testing.T) {
	svc, _, db, p := setup(t)
	v := create(t, svc)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p.check = func() {
		var count int64
		if err := db.WithContext(ctx).Model(&d.Target{}).Count(&count).Error; err != nil {
			t.Fatal("HTTP invoked with connection held", err)
		}
	}
	v, err := svc.Check(ctx, v.ID, 1, 7, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Fresh || v.Observation.State != "ready" || v.Observation.Sequence != 1 {
		t.Fatalf("observation not persisted: %+v", v)
	}
	if len(v.Operations) != 1 || v.Operations[0].ActorAdminID != 7 {
		t.Fatal(v.Operations)
	}
	enabled, err := svc.Save(ctx, v.ID, c.Input{Name: v.Name, Purpose: v.Purpose, BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true, ExpectedVersion: 1, ExpectedRouteVersion: 1})
	if err != nil || !enabled.Enabled {
		t.Fatalf("enable %v %+v", err, enabled)
	}
	if _, err = svc.SaveModule(ctx, 1, 7, c.ModuleInput{Partition: "support", TargetID: v.ID, Enabled: true}); err != nil {
		t.Fatal("enable module", err)
	}
	_, err = svc.Check(ctx, v.ID, 1, 7, "manual")
	if !errors.Is(err, c.ErrBusy) {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if svc.View(v.Target).Fresh {
		t.Fatal("stale still fresh")
	}
}

// TestAnonymousAdminObservationAndRecovery checks durable diagnostics and explicit recheck recovery.
// Parameter t reports failures; the test returns no value.
func TestAnonymousAdminObservationAndRecovery(t *testing.T) {
	svc, _, _, platform := setup(t)
	target := create(t, svc)
	ctx := context.Background()
	ready := platform.result
	platform.result.ErrorClass = "anonymous_admin"
	checked, err := svc.Check(ctx, target.ID, target.RouteVersion, 7, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if checked.Fresh || checked.Observation.State != "missing_permissions" || checked.Observation.ErrorClass != "anonymous_admin" || checked.Observation.LastFailureAt == nil || checked.Observation.LastSuccessAt != nil {
		t.Fatalf("anonymous administrator incorrectly ready: %+v", checked)
	}
	if len(checked.Operations) != 1 || checked.Operations[0].ErrorClass != "anonymous_admin" {
		t.Fatal("diagnostic missing from audit", checked.Operations)
	}
	input := c.Input{Name: target.Name, Purpose: target.Purpose, BotInstanceID: 1, TelegramChatID: "-1001234567890123", Enabled: true, ExpectedVersion: target.Version, ExpectedRouteVersion: target.RouteVersion}
	checked, err = svc.Save(ctx, target.ID, input)
	if err != nil || checked.Enabled || !checked.EnableRejected {
		t.Fatal("anonymous administrator could enable group", err)
	}
	// Membership events invalidate existing evidence but never trust the event operator as the bot.
	before := checked.Observation.Sequence
	for _, subject := range []int64{999, 123} {
		_, err := svc.Handle(ctx, 1, r.Update{Type: "my_chat_member", ChatID: target.TelegramChatID, Member: &r.MemberUpdate{Subject: r.UserReference{ID: subject}, Operator: r.UserReference{ID: 999}, New: r.ChatMember{Status: "administrator", IsAnonymous: false}}})
		if err != nil {
			t.Fatal(err)
		}
		checked, err = svc.Detail(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if subject == 999 && checked.Observation.Sequence != before {
			t.Fatal("another member invalidated the bot observation")
		}
		if subject == 123 && (checked.Fresh || checked.Observation.State != "unknown") {
			t.Fatal("event directly admitted group without fresh probe")
		}
	}
	platform.result = ready
	svc.Now = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	checked, err = svc.Check(ctx, target.ID, target.RouteVersion, 7, "manual")
	if err != nil || !checked.Fresh || checked.Observation.State != "ready" || checked.Observation.ErrorClass != "" {
		t.Fatal("non-anonymous recheck did not recover", checked, err)
	}
}

func TestOccupancyCASAndModulePartitions(t *testing.T) {
	svc, _, _, _ := setup(t)
	v := create(t, svc)
	ctx := context.Background()
	for _, in := range []c.Input{{Name: "other", Purpose: "membership", BotInstanceID: 1, TelegramChatID: "-1001234567890123"}, {Name: "other", Purpose: "support", BotInstanceID: 2, TelegramChatID: "-1001234567890123"}} {
		if _, err := svc.Save(ctx, 0, in); !errors.Is(err, c.ErrConflict) {
			t.Fatal("occupied chat accepted", err)
		}
	}
	if _, err := svc.Save(ctx, v.ID, c.Input{Name: "x", Purpose: "support", BotInstanceID: 1, TelegramChatID: "-1001234567890123", ExpectedVersion: 99, ExpectedRouteVersion: 1}); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	for _, partition := range []string{"support", "membership"} {
		if _, err := svc.SaveModule(ctx, 1, 7, c.ModuleInput{Partition: partition}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SaveModule(ctx, 1, 7, c.ModuleInput{Partition: "support"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("partition CAS failed", err)
	}
	if _, err := svc.SaveModule(ctx, 2, 7, c.ModuleInput{Partition: "support"}); err != nil {
		t.Fatal("instance isolation", err)
	}
}
func TestOldRouteAndSequenceCannotWin(t *testing.T) {
	svc, store, _, _ := setup(t)
	v := create(t, svc)
	ctx := context.Background()
	now := time.Now().UTC()
	_, op, err := store.BeginCheck(ctx, v.ID, 1, "manual", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Invalidate(ctx, 1, v.TelegramChatID, now); err != nil {
		t.Fatal(err)
	}
	o := d.Observation{State: "ready", RouteVersion: 1, Sequence: op.Sequence, ObservedAt: &now}
	applied, err := store.FinishCheck(ctx, op, o, now)
	if err != nil || applied {
		t.Fatalf("old sequence applied=%v err=%v", applied, err)
	}
	v, err = svc.Save(ctx, v.ID, c.Input{Name: "new", Purpose: "support", BotInstanceID: 1, TelegramChatID: "-1002", ExpectedVersion: 1, ExpectedRouteVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	applied, err = store.FinishCheck(ctx, op, o, now)
	if err != nil || applied {
		t.Fatal("old route won", err)
	}
	if v.RouteVersion != 2 || len(v.Routes) != 2 || v.Routes[1].RetiredAt == nil {
		t.Fatal(v.Routes)
	}
}
func TestNetworkUnknownEventAndRecovery(t *testing.T) {
	svc, store, _, p := setup(t)
	v := create(t, svc)
	ctx := context.Background()
	p.result = c.ProbeResult{ErrorClass: "network"}
	v, err := svc.Check(ctx, v.ID, 1, 1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if v.Observation.State != "unknown" || v.Fresh || v.Observation.LastFailureAt == nil {
		t.Fatal(v.Observation)
	}
	_, err = svc.Handle(ctx, 1, r.Update{Type: "my_chat_member", ChatID: v.TelegramChatID, Member: &r.MemberUpdate{Subject: r.UserReference{ID: 123}, Operator: r.UserReference{ID: 999}}})
	if err != nil {
		t.Fatal(err)
	}
	p.result = c.ProbeResult{Observation: d.Observation{ChatType: "supergroup", IsForum: false, MemberStatus: "administrator"}}
	svc.Now = func() time.Time { return time.Now().Add(time.Minute) }
	restarted := a.New(store, p)
	restarted.Now = svc.Now
	if err = restarted.Reconcile(ctx, 1); err != nil {
		t.Fatal(err)
	}
	v, _ = restarted.Detail(ctx, v.ID)
	if v.Observation.State != "missing_permissions" {
		t.Fatal(v.Observation)
	}
}
