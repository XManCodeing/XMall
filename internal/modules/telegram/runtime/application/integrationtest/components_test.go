package integrationtest

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/productmedia"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	platform "github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// reminderReceiver provides the installed membership event guard and counts actual pre-send checks.
type reminderReceiver struct {
	checks atomic.Int32
	reject error
}

// Handle accepts no manufactured updates; reminders enter through reconciliation only.
func (*reminderReceiver) Handle(context.Context, uint, c.Update) ([]c.Message, error) {
	return nil, nil
}

// AuthorizeReminderDelivery supplies deterministic expiry/quiet policy outcomes for real runtime tests.
func (guard *reminderReceiver) AuthorizeReminderDelivery(context.Context, uint, c.Message) error {
	guard.checks.Add(1)
	return guard.reject
}

type reminderAPI struct {
	idleAPI
	calls   atomic.Int32
	message int64
	failure error
}

// Send simulates only Telegram, while the runtime queue and receipt storage remain real.
func (api *reminderAPI) Send(context.Context, string, c.Message) (int64, error) {
	api.calls.Add(1)
	return api.message, api.failure
}

// Apply preserves the production product-media wrapper's applying capability.
func (api *reminderAPI) Apply(ctx context.Context, token string, message c.Message) (c.DeliveryResult, error) {
	id, err := api.Send(ctx, token, message)
	return c.DeliveryResult{MessageID: id}, err
}

type reminderRateError struct{}

func (reminderRateError) Error() string     { return "synthetic limit" }
func (reminderRateError) StatusCode() int   { return 429 }
func (reminderRateError) RetrySeconds() int { return 5 }
func (reminderRateError) Uncertain() bool   { return false }

type reminderRejectError int

// Error returns only a fixed synthetic failure category.
func (reminderRejectError) Error() string           { return "synthetic permanent rejection" }
func (failure reminderRejectError) StatusCode() int { return int(failure) }
func (reminderRejectError) RetrySeconds() int       { return 0 }
func (reminderRejectError) Uncertain() bool         { return false }

// TestRuntimeReminderReceiptAndDeferral runs actual single-connection runtime workers without network sends.
func TestRuntimeReminderReceiptAndDeferral(t *testing.T) {
	for _, scenario := range []string{"sent", "missing_receipt", "unknown", "expired", "quiet", "rate", "rejected", "blocked"} {
		t.Run(scenario, func(t *testing.T) {
			api := &reminderAPI{message: 321}
			guard := &reminderReceiver{}
			want := "sent"
			switch scenario {
			case "missing_receipt":
				api.message, want = 0, "unknown"
			case "unknown":
				api.failure, want = errors.New("synthetic network loss"), "unknown"
			case "expired":
				guard.reject, want = c.DeliveryInvalidated{State: "superseded"}, "superseded"
			case "quiet":
				guard.reject, want = c.DeliveryDeferred{AvailableAt: time.Now().Add(time.Hour)}, "retry"
			case "rejected":
				api.failure, want = reminderRejectError(400), "failed"
			case "blocked":
				api.failure, want = reminderRejectError(403), "unreachable"
			case "rate":
				api.failure, want = reminderRateError{}, "retry"
			}
			f := setup(t, api, bootstrap.UpdateModules{Events: map[string]bootstrap.UpdateReceiver{"chat_member": guard}})
			must(t, f.store.SaveContact(context.Background(), &d.Contact{BotID: f.bot.ID, TelegramUserID: 12, ChatID: 12, UserID: 7, Reachable: true}))
			message := c.Message{ReminderDelivery: &c.ReminderDeliveryRef{ID: 1}, TargetType: c.TargetShopPrivate, BusinessRef: "membership-reminder:test", Purpose: "membership_reminder", Operation: "send", OwnerUserID: 7, ChatID: 12, Text: "synthetic reminder"}
			raw, err := json.Marshal(message)
			must(t, err)
			cipher, err := f.manager.Seal(string(raw))
			must(t, err)
			must(t, f.store.Enqueue(context.Background(), &d.Delivery{BotID: f.bot.ID, Key: message.BusinessRef, ChatID: 12, TelegramUserID: 12, UserID: 7, PayloadCipher: cipher, Purpose: message.Purpose, Operation: "send", Priority: 20}))
			f.run(t, 300*time.Millisecond, nil)
			status, err := f.store.BusinessDelivery(context.Background(), f.bot.ID, message.BusinessRef)
			must(t, err)
			if status.State != want {
				t.Fatal(status, want)
			}
			if scenario == "rate" {
				var queued d.Delivery
				must(t, f.db.First(&queued, "key = ?", message.BusinessRef).Error)
				if queued.Priority != 20 || queued.Attempts != 1 || !queued.AvailableAt.After(time.Now()) {
					t.Fatal("429 lost priority/backoff", queued.Priority, queued.Attempts, queued.AvailableAt)
				}
			}
			expectedCalls := int32(1)
			if scenario == "expired" || scenario == "quiet" {
				expectedCalls = 0
			}
			if api.calls.Load() != expectedCalls || guard.checks.Load() == 0 {
				t.Fatal("guard/send mismatch", api.calls.Load(), guard.checks.Load())
			}
			if want == "unknown" || want == "failed" || want == "unreachable" {
				f.run(t, 150*time.Millisecond, nil)
				if api.calls.Load() != 1 {
					t.Fatal("unknown automatically replayed")
				}
			}
		})
	}
}

type identity struct {
	shop.Identity
	mu     sync.Mutex
	stamp  string
	checks int
}

func (i *identity) Resolve(context.Context, shop.Actor, bool) (uint, error) { return 7, nil }
func (i *identity) Generation(context.Context, shop.Actor) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.checks++
	return i.stamp, nil
}
func (i *identity) revoke() { i.mu.Lock(); defer i.mu.Unlock(); i.stamp = "binding-new" }

type lease struct {
	renew   func()
	release func()
}

func (l lease) Renew(context.Context) error {
	if l.renew != nil {
		l.renew()
	}
	return nil
}
func (l lease) Release(context.Context) error {
	if l.release != nil {
		l.release()
	}
	return nil
}

type leaser struct{ lease c.Lease }

func (l leaser) Acquire(context.Context, string) (c.Lease, error) { return l.lease, nil }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Suppress network only in tests not exercising the HTTP adapter. The real
// subscription and send wrapper tests below use httptest through botapi.Adapter.
type idleAPI struct{ c.API }

func (idleAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (idleAPI) PollWithSubscription(ctx context.Context, _ string, _ int64, _ []string) ([]c.Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (idleAPI) Send(context.Context, string, c.Message) (int64, error) {
	return 0, errors.New("unexpected send")
}

type fixture struct {
	db         *gorm.DB
	store      *gormstore.Store
	manager    *app.Manager
	bot        *d.Instance
	identity   *identity
	dispatcher *bootstrap.UpdateDispatcher
}

func setup(t *testing.T, api c.API, modules bootstrap.UpdateModules) *fixture {
	t.Helper()
	// Cancellation can make database/sql discard SQLite's only connection.
	// A named shared-memory database then vanishes even before pool.Close;
	// a per-fixture file survives reconnects without adding concurrent writers.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runtime.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	must(t, err)
	pool, err := db.DB()
	must(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { must(t, pool.Close()) })
	must(t, db.AutoMigrate(&d.Instance{}, &d.GenerationExit{}, &d.Contact{}, &d.Session{}, &d.Inbox{}, &d.Delivery{}))
	store := gormstore.New(db)
	manager := bootstrap.NewManagerWithMedia(store, "test-key", t.TempDir())
	if _, ok := manager.API.(c.SubscriptionAPI); !ok {
		t.Fatal("container manager lost subscription wrapper")
	}
	if api == nil {
		api = idleAPI{}
	}
	manager.API = productmedia.New(api, t.TempDir(), nil)
	bot := &d.Instance{TelegramID: 123, Enabled: true, Mode: "internal", Version: 1}
	bot.TokenCipher, err = manager.Seal("123:test")
	must(t, err)
	must(t, store.Create(context.Background(), bot))
	id := &identity{stamp: "binding-original"}
	service := shopapp.New(shopapp.Dependencies{Store: store, Identity: id, Secret: "test-key"})
	return &fixture{db: db, store: store, manager: manager, bot: bot, identity: id, dispatcher: bootstrap.NewUpdateDispatcher(service, service, modules)}
}
func TestFixtureSurvivesCancelledTransaction(t *testing.T) {
	f := setup(t, nil, bootstrap.UpdateModules{})
	pool, err := f.db.DB()
	must(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	must(t, err)
	var original driver.Conn
	must(t, conn.Raw(func(raw any) error {
		original = raw.(driver.Conn)
		_, resetter := raw.(driver.SessionResetter)
		_, validator := raw.(driver.Validator)
		t.Logf("SQLite driver %T: SessionResetter=%t Validator=%t", raw, resetter, validator)
		return nil
	}))
	must(t, conn.Close())
	txCtx, cancelTx := context.WithCancel(ctx)
	defer cancelTx()
	_, err = pool.BeginTx(txCtx, nil)
	must(t, err)
	cancelTx()
	// MaxOpenConns=1 makes acquisition a barrier: database/sql must finish
	// its cancellation rollback and release/discard the original connection.
	// Do not call Rollback here: it could win the race against awaitDone and
	// retain the connection, hiding the in-memory database lifetime bug.
	conn, err = pool.Conn(ctx)
	must(t, err)
	defer func() { must(t, conn.Close()) }()
	must(t, conn.Raw(func(raw any) error {
		if raw == original {
			return errors.New("cancelled transaction did not replace the SQLite connection")
		}
		return nil
	}))
	var id uint
	must(t, conn.QueryRowContext(ctx, "SELECT id FROM telegram_bot_instances WHERE id = ?", f.bot.ID).Scan(&id))
	if id != f.bot.ID {
		t.Fatalf("bot changed after connection replacement: %d", id)
	}
}

type shutdownHandler struct {
	c.Handler
	entered chan struct{}
	exited  chan error
	store   *gormstore.Store
}

func (h shutdownHandler) Handle(ctx context.Context, bot uint, _ c.Update) ([]c.Message, error) {
	close(h.entered)
	<-ctx.Done()
	// The DB must remain usable while a cancelled worker drains.
	_, err := h.store.Get(context.Background(), bot)
	h.exited <- err
	return nil, ctx.Err()
}

func TestRuntimeStopJoinsWorkersBeforeFixtureCleanup(t *testing.T) {
	f := setup(t, nil, bootstrap.UpdateModules{})
	f.accept(t, c.Update{ID: 1, Type: "message", Private: true, ChatType: "private", UserID: 12, ChatID: 12, MessageID: 1})
	h := shutdownHandler{Handler: f.dispatcher, entered: make(chan struct{}), exited: make(chan error, 1), store: f.store}
	released := make(chan error, 1)
	r := app.NewRuntime(f.manager, leaser{lease{release: func() {
		select {
		case err := <-h.exited:
			released <- err
		default:
			released <- errors.New("lease released before inbox worker exited")
		}
	}}}, h)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- r.Start(ctx) }()
	// Registered after setup's DB cleanup, so Stop/join always precedes Close,
	// including assertion failures. No sleep is used to infer worker completion.
	t.Cleanup(func() {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		must(t, r.Stop(stopCtx))
	})
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("inbox worker did not enter handler")
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	must(t, r.Stop(stopCtx))
	select {
	case err := <-started:
		must(t, err)
	case <-stopCtx.Done():
		t.Fatal("Start did not return after Stop")
	}
	select {
	case err := <-released:
		must(t, err)
	default:
		t.Fatal("Stop returned before worker drain and lease release")
	}
	bot, err := f.store.Get(context.Background(), f.bot.ID)
	must(t, err)
	if bot.State != "stopped" {
		t.Fatalf("final heartbeat not persisted before Stop: %s", bot.State)
	}
}

func (f *fixture) run(t *testing.T, duration time.Duration, l c.Lease) {
	t.Helper()
	if l == nil {
		l = lease{}
	}
	r := app.NewRuntime(f.manager, leaser{l}, f.dispatcher)
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	// Start blocks until cancellation and joins every runtime worker. Keep it
	// synchronous: assertions, HTTP server shutdown and DB cleanup happen only
	// after the join, not merely after the duration has elapsed.
	must(t, r.Start(ctx))
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	must(t, r.Stop(stopCtx))
	var err error
	f.bot, err = f.store.Get(context.Background(), f.bot.ID)
	must(t, err)
}
func (f *fixture) accept(t *testing.T, updates ...c.Update) {
	t.Helper()
	var entries []d.Inbox
	var offset int64
	for _, u := range updates {
		raw, err := json.Marshal(u)
		must(t, err)
		cipher, err := f.manager.Seal(string(raw))
		must(t, err)
		entries = append(entries, d.Inbox{BotID: f.bot.ID, UpdateID: u.ID, PayloadCipher: cipher, Status: "pending", AvailableAt: time.Now()})
		offset = u.ID + 1
	}
	must(t, f.store.Accept(context.Background(), f.bot.ID, f.bot.Generation, entries, offset))
}
func (f *fixture) enqueue(t *testing.T, key string, chat int64, priority int, m c.Message) {
	t.Helper()
	raw, err := json.Marshal(m)
	must(t, err)
	cipher, err := f.manager.Seal(string(raw))
	must(t, err)
	must(t, f.store.Enqueue(context.Background(), &d.Delivery{BotID: f.bot.ID, Key: key, TargetType: string(m.TargetType), ChatID: chat, TelegramUserID: chat, UserID: 7, Priority: priority, PayloadCipher: cipher, Purpose: m.Purpose, Operation: m.Operation, AvailableAt: time.Now().Add(-time.Second)}))
}
func (f *fixture) contact(t *testing.T, chat int64) {
	t.Helper()
	must(t, f.store.SaveContact(context.Background(), &d.Contact{BotID: f.bot.ID, TelegramUserID: chat, ChatID: chat, UserID: 7, Reachable: true}))
}

type subscriptions []string

func (s subscriptions) AllowedUpdates(context.Context, uint) ([]string, error) {
	return []string(s), nil
}

type noSubscription struct{ c.API }

func (noSubscription) Poll(context.Context, string, int64) ([]c.Update, error) {
	panic("capability loss must not fallback")
}

func TestIntegratedDispatcherMediaAdapterSubscription(t *testing.T) {
	var mu sync.Mutex
	var offsets []int64
	var allowed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getWebhookInfo") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"url":""}}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Errorf("unexpected method %s", r.URL.Path)
			http.Error(w, "unexpected", 400)
			return
		}
		var received struct {
			Offset  int64    `json:"offset"`
			Allowed []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		mu.Lock()
		offsets = append(offsets, received.Offset)
		allowed = received.Allowed
		n := len(offsets)
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":81,"chat_member":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":12},"old_chat_member":{"status":"left","user":{"id":12}},"new_chat_member":{"status":"member","user":{"id":12}}}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	f := setup(t, botapi.Adapter{Options: []platform.Option{platform.WithBaseURL(server.URL)}}, bootstrap.UpdateModules{Subscriptions: []bootstrap.UpdateSubscriptions{subscriptions{"chat_member"}}})
	must(t, f.store.Accept(context.Background(), f.bot.ID, f.bot.Generation, nil, 81))
	f.run(t, 400*time.Millisecond, nil)
	mu.Lock()
	gotOffsets := append([]int64(nil), offsets...)
	gotAllowed := append([]string(nil), allowed...)
	mu.Unlock()
	if len(gotOffsets) < 2 || gotOffsets[0] != 81 || gotOffsets[1] != 82 || !reflect.DeepEqual(gotAllowed, []string{"message", "callback_query", "chat_member"}) || f.bot.Offset != 82 {
		t.Fatalf("broken poll chain offsets=%v allowed=%v persisted=%d", gotOffsets, gotAllowed, f.bot.Offset)
	}
	var row d.Inbox
	must(t, f.db.Where("update_id = ?", 81).First(&row).Error)
	if row.Status != "done" {
		t.Fatalf("unregistered event not safely consumed: %s", row.Status)
	}
	wrapper := f.manager.API.(*productmedia.API)
	wrapper.API = noSubscription{}
	_, err := wrapper.PollWithSubscription(context.Background(), "123:test", 82, gotAllowed)
	if !errors.Is(err, c.ErrSubscriptionUnsupported) {
		t.Fatalf("capability error %v", err)
	}
}

func TestIntegratedDefaultShoppingAndPrivateMemberIsolation(t *testing.T) {
	f := setup(t, nil, bootstrap.UpdateModules{})
	allowed, err := f.dispatcher.AllowedUpdates(context.Background(), f.bot.ID)
	must(t, err)
	if !reflect.DeepEqual(allowed, []string{"message", "callback_query"}) {
		t.Fatal(allowed)
	}
	f.accept(t, c.Update{ID: 1, Type: "message", ChatType: "private", Private: true, UserID: 12, ChatID: 12, MessageID: 3, Text: "普通咨询", Locale: "zh-CN"})
	f.run(t, 400*time.Millisecond, nil)
	contact, err := f.store.Contact(context.Background(), f.bot.ID, 12)
	must(t, err)
	if contact == nil || contact.UserID != 7 || !contact.Reachable {
		t.Fatalf("shop contact %+v", contact)
	}
	var rows []d.Delivery
	must(t, f.db.Find(&rows).Error)
	if len(rows) == 0 {
		t.Fatal("default consultation lost shopping response")
	}
	for _, row := range rows {
		raw, err := f.manager.Open(row.PayloadCipher)
		must(t, err)
		var m c.Message
		must(t, json.Unmarshal([]byte(raw), &m))
		if m.TargetType.Normalize() != c.TargetShopPrivate || m.IdentityStamp != "binding-original" || m.Purpose == "cleanup" {
			t.Fatalf("unsafe response %+v", m)
		}
	}
	contact.Reachable = false
	must(t, f.store.SaveContact(context.Background(), contact))
	var updates []c.Update
	for _, tg := range []int64{12, 13} {
		updates = append(updates, c.Update{ID: tg, Type: "my_chat_member", ChatType: "private", Private: true, UserID: tg, ChatID: tg, Member: &c.MemberUpdate{Subject: c.UserReference{ID: 123}}})
	}
	f.accept(t, updates...)
	f.run(t, 400*time.Millisecond, nil)
	contact, err = f.store.Contact(context.Background(), f.bot.ID, 12)
	must(t, err)
	if contact.Reachable {
		t.Fatal("membership refreshed shop reachability")
	}
	contact, err = f.store.Contact(context.Background(), f.bot.ID, 13)
	must(t, err)
	if contact != nil {
		t.Fatal("membership created shop contact")
	}
}

func TestIntegratedDispatcherRetainsLegacyStampAtBothChecks(t *testing.T) {
	for _, revokeOnRenew := range []bool{false, true} {
		t.Run(fmt.Sprint(revokeOnRenew), func(t *testing.T) {
			f := setup(t, nil, bootstrap.UpdateModules{})
			f.contact(t, 12)
			f.enqueue(t, "old", 12, 200, c.Message{IdentityStamp: "binding-original", ChatID: 12, Text: "secret"})
			if !revokeOnRenew {
				f.identity.revoke()
			}
			// runBot renews once before Poll. Revoke only after the first stamp check,
			// so the delivery-specific lease renewal exercises the second authorization.
			l := lease{renew: func() {
				f.identity.mu.Lock()
				checked := f.identity.checks > 0
				f.identity.mu.Unlock()
				if checked {
					f.identity.revoke()
				}
			}}
			f.run(t, 400*time.Millisecond, l)
			var row d.Delivery
			must(t, f.db.First(&row).Error)
			want := 1
			if revokeOnRenew {
				want = 2
			}
			if row.Status != "failed" || f.identity.checks != want {
				t.Fatalf("status=%s checks=%d", row.Status, f.identity.checks)
			}
		})
	}
}

func TestIntegratedSchedulerSkipsCoolingTargetAndUnknownNeverHTTP(t *testing.T) {
	var mu sync.Mutex
	var chats []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"url":""}}`))
			return
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		var payload struct {
			Chat int64 `json:"chat_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		chats = append(chats, payload.Chat)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	}))
	defer server.Close()
	f := setup(t, botapi.Adapter{Options: []platform.Option{platform.WithBaseURL(server.URL)}}, bootstrap.UpdateModules{})
	f.contact(t, 12)
	f.contact(t, 13)
	f.enqueue(t, "unknown", 12, 400, c.Message{TargetType: "future", ChatID: 12, Text: "must not send"})
	f.enqueue(t, "unregistered-support", 12, 375, c.Message{TargetType: c.TargetSupportPrivate, BusinessRef: "session:1", ChatID: 12, Text: "must not bypass dispatcher target registry"})
	f.enqueue(t, "unknown-media", 12, 350, c.Message{TargetKind: "video", IdentityStamp: "binding-original", ChatID: 12, Text: "must not degrade"})
	f.enqueue(t, "first", 12, 300, c.Message{IdentityStamp: "binding-original", ChatID: 12, Text: "one"})
	f.enqueue(t, "cooling", 12, 250, c.Message{IdentityStamp: "binding-original", ChatID: 12, Text: "two"})
	f.enqueue(t, "other-transaction", 13, 200, c.Message{IdentityStamp: "binding-original", ChatID: 13, Text: "three"})
	f.run(t, 650*time.Millisecond, nil)
	mu.Lock()
	got := append([]int64(nil), chats...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []int64{12, 13}) {
		t.Fatalf("HTTP order %v", got)
	}
	var rows []d.Delivery
	must(t, f.db.Order("id").Find(&rows).Error)
	want := []string{"failed", "failed", "failed", "sent", "pending", "sent"}
	for n, row := range rows {
		if row.Status != want[n] {
			t.Fatalf("%s=%s want %s", row.Key, row.Status, want[n])
		}
	}
}
