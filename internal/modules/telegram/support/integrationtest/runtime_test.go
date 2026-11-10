package integrationtest

import (
	"context"
	"encoding/json"
	boot "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type uncertain struct{}

func (uncertain) Error() string     { return "unknown" }
func (uncertain) StatusCode() int   { return 0 }
func (uncertain) RetrySeconds() int { return 0 }
func (uncertain) Uncertain() bool   { return true }

type fakeAPI struct {
	r.API
	sends atomic.Int32
}

func (*fakeAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (*fakeAPI) Poll(ctx context.Context, _ string, _ int64) ([]r.Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (f *fakeAPI) Send(context.Context, string, r.Message) (int64, error) {
	f.sends.Add(1)
	return 0, uncertain{}
}
func (f *fakeAPI) Apply(context.Context, string, r.Message) (r.DeliveryResult, error) {
	f.sends.Add(1)
	return r.DeliveryResult{}, uncertain{}
}
func (*fakeAPI) Answer(context.Context, string, string, string) error { return nil }

type testLease struct{}

func (testLease) Renew(context.Context) error   { return nil }
func (testLease) Release(context.Context) error { return nil }

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, string) (r.Lease, error) { return testLease{}, nil }

type noCreateIdentity struct {
	shop.Identity
	t *testing.T
}

func (i noCreateIdentity) Resolve(_ context.Context, _ shop.Actor, create bool) (uint, error) {
	if create {
		i.t.Error("consultation created shop account")
	}
	return 0, nil
}
func TestRealDispatcherRuntimeUnknownAndInboxCleanup(t *testing.T) {
	ctx := context.Background()
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runtime.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.AutoMigrate(&rd.Instance{}, &rd.GenerationExit{}, &rd.Contact{}, &rd.Session{}, &rd.Inbox{}, &rd.Delivery{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.SupportBlock{}, &d.Album{}, &d.AlbumItem{}, &d.ReadyDispatch{}, &d.Downstream{}, &d.OperationReference{}, &d.OperationControl{}, &d.DownstreamAlbum{}, &d.OperationAudit{}))
	store := rs.New(db)
	api := &fakeAPI{}
	manager := app.NewManager(store, api, "support-integration-key")
	token, e := manager.Seal("123:test")
	must(e)
	bot := &rd.Instance{TelegramID: 123, Enabled: true, Mode: "internal", Version: 1, TokenCipher: token}
	must(store.Create(ctx, bot))
	repo := ss.New(db)
	service := a.New(repo, manager, modules{}, store)
	service.DeliveryReady = capability{}
	shopService := shopapp.New(shopapp.Dependencies{Store: store, Identity: noCreateIdentity{t: t}, Secret: "support-integration-key"})
	dispatcher := boot.NewUpdateDispatcher(shopService, shopService, boot.UpdateModules{Support: service, SupportCallbacks: service, Targets: map[r.TargetType]r.TargetAuthorizer{r.TargetSupportPrivate: service}})
	// Corrupt/expired legacy code state must be isolated by the actual shop router.
	must(store.SaveSession(ctx, &rd.Session{BotID: bot.ID, TelegramUserID: 42, ChatID: 42, State: "code", DataCipher: "corrupt", ExpiresAt: time.Now().Add(-time.Hour)}, 0))
	if _, e = dispatcher.Handle(ctx, bot.ID, r.Update{ID: 1, Private: true, Type: "message", ChatType: "private", UserID: 42, ChatID: 42, MessageID: 1, Text: "private-code"}); e != nil {
		t.Fatal(e)
	}
	var count int64
	must(db.Model(&d.BufferedMessage{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("old code entered support")
	}
	u := r.Update{ID: 2, Private: true, Type: "message", ChatType: "private", UserID: 43, ChatID: 43, MessageID: 2, Text: "consultation"}
	raw, e := json.Marshal(u)
	must(e)
	cipher, e := manager.Seal(string(raw))
	must(e)
	must(store.Accept(ctx, bot.ID, 0, []rd.Inbox{{BotID: bot.ID, UpdateID: u.ID, PayloadCipher: cipher, Status: "pending", AvailableAt: time.Now()}}, 3))
	run := func() {
		runtime := app.NewRuntime(manager, testLeaser{}, dispatcher)
		runCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		defer cancel()
		must(runtime.Start(runCtx))
		stopCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		must(runtime.Stop(stopCtx))
	}
	run()
	var delivery rd.Delivery
	must(db.Where("bot_id = ? AND purpose = ?", bot.ID, "support_gate").First(&delivery).Error)
	if delivery.Status != "unknown" || api.sends.Load() != 1 {
		t.Fatalf("status=%s sends=%d", delivery.Status, api.sends.Load())
	}
	// Reconstruct recovery service; duplicate enqueue must preserve unknown.
	must(db.Model(&d.Challenge{}).Where("bot_id = ?", bot.ID).Update("notice_pending", true).Error)
	service = a.New(ss.New(db), manager, modules{}, store)
	must(service.Reconcile(ctx, bot.ID))
	run()
	if api.sends.Load() != 1 {
		t.Fatal("unknown challenge resent")
	}
	// Simulate completed-inbox payload cleanup; business snapshot survives.
	must(store.Cleanup(ctx, bot.ID))
	var inbox rd.Inbox
	must(db.Where("bot_id = ?", bot.ID).First(&inbox).Error)
	if inbox.PayloadCipher != "" {
		t.Fatal("done inbox not cleaned")
	}
	var buffered d.BufferedMessage
	must(db.Where("bot_id = ?", bot.ID).First(&buffered).Error)
	plain, e := manager.Open(buffered.PayloadCipher)
	must(e)
	var restored r.Update
	must(json.Unmarshal([]byte(plain), &restored))
	if restored.Text != u.Text {
		t.Fatal("cleanup lost consultation")
	}
	service.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	must(service.Reconcile(ctx, bot.ID))
	var feedback []rd.Delivery
	must(db.Where("bot_id = ?", bot.ID).Find(&feedback).Error)
	if len(feedback) < 2 {
		t.Fatal("expiry feedback not durable")
	}
}
