package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rstore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	h "github.com/XManCodeing/XMall/internal/modules/telegram/support/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// recoveryFixture retains only public ports and trusted test infrastructure.
// HTTP tests must not access private fields in an infrastructure package.
type recoveryFixture struct {
	store    *ss.Store
	db       *gorm.DB
	cipher   *app.Manager
	message  d.BufferedMessage
	request  r.DeliveryRequest
	envelope r.Message
	now      time.Time
}

type recoveryRoute struct{ binding c.DeliveryBinding }

// CheckSupportRoute validates the fixture's immutable bot/target binding and
// returns denial on stale routes. No production capability checks are replaced.
func (route recoveryRoute) CheckSupportRoute(_ context.Context, bot, target uint, binding c.DeliveryBinding, _ time.Time) error {
	if bot != 1 || target != 1 || binding.ChatID != route.binding.ChatID || binding.RouteVersion != route.binding.RouteVersion {
		return c.ErrDenied
	}
	return nil
}

// setRoute installs a public transaction-bound route reader for deterministic
// route-change tests. binding is trusted test configuration; returns no value.
func (fixture *recoveryFixture) setRoute(binding c.DeliveryBinding) {
	fixture.store.WithRouteReader(func(*gorm.DB) c.RouteReader { return recoveryRoute{binding} })
}

// newRecoveryFixture prepares a real consultation through Accept/Verify/Claim/
// Load/Prepare ports. Only bot/topic infrastructure is seeded; no delivery result
// is fabricated. It returns an isolated SQLite fixture closed by t.Cleanup.
func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = db.AutoMigrate(&rd.Instance{}, &rd.Delivery{}, &d.Session{}, &d.Challenge{}, &d.BufferedMessage{}, &d.SupportBlock{}, &d.Album{}, &d.AlbumItem{}, &d.ReadyDispatch{}, &d.TopicMapping{}, &d.TopicOperation{}, &d.ReplyMapping{}, &d.Downstream{}, &d.DownstreamAlbum{}, &d.ResolutionAudit{}); err != nil {
		t.Fatal(err)
	}
	fixture := &recoveryFixture{db: db, cipher: app.NewManager(nil, nil, "synthetic-recovery-test"), now: time.Now().UTC()}
	fixture.store = ss.New(db, fixture.cipher).WithReadyEncoder(c.EncodeReadyTransfer)
	fixture.setRoute(c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1})
	if err = db.Create(&rd.Instance{ID: 1, TelegramID: 100, Enabled: true, Mode: "internal", State: "running", Version: 1, Generation: 1, TokenCipher: "synthetic"}).Error; err != nil {
		t.Fatal(err)
	}
	source := r.Update{ID: 1, Type: "message", UserID: 10, ChatID: 10, Private: true, MessageID: 9, MessageKind: "text", Text: "original"}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := fixture.cipher.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, err := fixture.store.Accept(ctx, 1, source, encrypted, int64(len(raw)), "ref", c.DefaultPolicy(), fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if out.Challenge == nil {
		t.Fatal("challenge missing")
	}
	if _, err = fixture.store.Verify(ctx, 1, r.Update{ID: 2, UserID: 10, ChatID: 10}, c.VerificationAnswer(*out.Challenge, out.Challenge.AnswerIndex), c.DefaultPolicy(), fixture.now); err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&d.Session{}).Where("id = ?", out.Session.ID).Update("state", "open").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&d.TopicMapping{BotID: 1, SessionID: out.Session.ID, TargetID: 1, ChatID: -100, ThreadID: 20, RouteVersion: 1, State: "open", Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	message, err := fixture.store.ClaimReady(ctx, 1, out.Session.ID, 1, "claim", fixture.now, time.Minute)
	if err != nil || message == nil {
		t.Fatal(message, err)
	}
	loaded, err := fixture.store.LoadReady(ctx, 1, message.ID, 1, "claim", fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.envelope, err = c.EncodeReadyTransfer(loaded.Source, loaded.Binding, c.ReadyBusinessRef(message.SessionID, message.ID))
	if err != nil {
		t.Fatal(err)
	}
	fixture.request, err = fixture.store.PrepareDispatch(ctx, 1, message.ID, 1, "claim", fixture.envelope, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.message = *message
	return fixture
}

// TestReadyForwardSourceProof rejects even matching dispatch ciphertext if its
// source IDs differ from the encrypted inbound Update; old typed sends remain valid.
func TestReadyForwardSourceProof(t *testing.T) {
	for _, scenario := range []string{"native", "legacy", "forged-chat", "forged-message"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRecoveryFixture(t)
			message := fixture.envelope
			if message.Transfer.SourceChatID != 10 || len(message.Transfer.SourceMessageIDs) != 1 || message.Transfer.SourceMessageIDs[0] != 9 {
				t.Fatal("source identity was not projected")
			}
			switch scenario {
			case "legacy":
				message.Transfer.SourceChatID, message.Transfer.SourceMessageIDs = 0, nil
			case "forged-chat":
				message.Transfer.SourceChatID = 11
			case "forged-message":
				message.Transfer.SourceMessageIDs = []int64{99}
			}
			raw, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := fixture.cipher.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err = fixture.db.Model(&d.ReadyDispatch{}).Where("message_id = ?", fixture.message.ID).Update("payload_cipher", cipher).Error; err != nil {
				t.Fatal(err)
			}
			err = fixture.store.AuthorizeTargetAt(context.Background(), 1, message, fixture.now)
			allowed := scenario == "native" || scenario == "legacy"
			if (err == nil) != allowed {
				t.Fatalf("authorization mismatch: %v", err)
			}
		})
	}
}

// downstreamSource completes the original consultation through runtime queue,
// claim and receipt ports. It returns a native reply referencing that real proof.
func (fixture *recoveryFixture) downstreamSource(t *testing.T) r.Update {
	t.Helper()
	ctx := context.Background()
	queue := rstore.New(fixture.db)
	if err := queue.EnqueueRequest(ctx, &fixture.request); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, "sent", r.DeliveryReceipt{MessageIDs: []int64{77}}, "", fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.ReconcileReady(ctx, 1, fixture.message.ID, 1, fixture.now); err != nil {
		t.Fatal(err)
	}
	return r.Update{ID: 90, Type: "message", UserID: 55, ChatID: -100, MessageID: 88, ThreadID: 20, IsTopicMessage: true, MessageKind: "photo", Media: []r.Media{{Type: "photo", FileID: "large"}}, Reply: &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 20}}
}

// recoveryHTTP wires the real application, HTTP handlers and transaction stores.
// Tests supply only an authenticated synthetic actor; receipt/state are never
// passed through HTTP. Returns a router with no external production connections.
func recoveryHTTP(t *testing.T, fixture *recoveryFixture) *gin.Engine {
	t.Helper()
	store, db := fixture.store, fixture.db
	store.WithDeliveryRecovery(func(tx *gorm.DB) r.DeliveryRecoveryStore { return rstore.New(tx) })
	admin := a.NewAdmin(ss.NewAdmin(db), fixture.cipher, func(context.Context, uint, string, bool) error { return nil }, nil)
	generation := func(context.Context, uint) (uint64, error) { return 1, nil }
	admin.WithDownstreams(ss.NewAdmin(db), nil, generation).WithRecovery(ss.NewAdmin(db), nil, generation)
	admin.WithDeliveryRecovery(store, func(_ context.Context, actor uint, path string) error {
		if actor != 9 || (!strings.HasSuffix(path, "/retry") && !strings.HasSuffix(path, "/reconcile")) {
			return c.ErrDenied
		}
		return nil
	})
	router := gin.New()
	group := router.Group("/admin", func(ctx *gin.Context) { ctx.Set("admin_id", uint(9)) })
	h.RegisterRoutes(group, h.New(admin))
	return router
}

// postRecovery executes the actual strict HTTP endpoint and returns its DTO.
// want is the expected HTTP status; unexpected bodies fail the test.
func postRecovery(t *testing.T, router http.Handler, direction string, id uint, action, body string, want int) c.AdminDeliveryRecovery {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/telegram-bot/support/%ss/%d/%s", direction, id, action), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, request)
	if writer.Code != want {
		t.Fatalf("status=%d want=%d body=%s", writer.Code, want, writer.Body.String())
	}
	var result struct {
		Data c.AdminDeliveryRecovery `json:"data"`
	}
	if want == 200 {
		if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result.Data
}

// TestDeliveryRecoveryHTTPFailedRetryAndReceipt proves the real queue -> claim ->
// receipt -> HTTP CAS retry -> claim -> receipt -> HTTP reconcile path. SQL is
// not used to manufacture failure/success; the existing runtime receipt port is.
func TestDeliveryRecoveryHTTPFailedRetryAndReceipt(t *testing.T) {
	fixture := newRecoveryFixture(t)
	store, db, now, source := fixture.store, fixture.db, fixture.now, fixture.downstreamSource(t)
	ctx := context.Background()
	row, err := store.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.DownstreamRequest(ctx, 1, row.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	queue := rstore.New(db)
	if err = queue.EnqueueRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, "failed", r.DeliveryReceipt{}, "telegram rejected message", now); err != nil {
		t.Fatal(err)
	}
	router := recoveryHTTP(t, fixture)
	input := `{"expected_version":1,"evidence":"ticket:failed"}`
	out := postRecovery(t, router, "downstream", row.ID, "reconcile", input, 200)
	if out.State != "failed" || out.RuntimeState != "failed" || out.Version != 2 {
		t.Fatal(out)
	}
	postRecovery(t, router, "downstream", row.ID, "retry", input, 409)
	input = `{"expected_version":2,"evidence":"ticket:retry"}`
	out = postRecovery(t, router, "downstream", row.ID, "retry", input, 200)
	if !out.RetryQueued || out.State != "pending" || out.RuntimeState != "retry" {
		t.Fatal(out)
	}
	retry, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || retry == nil || retry.ID != claim.ID || retry.Attempts != 2 {
		t.Fatal(retry, err)
	}
	plain, err := fixture.cipher.Open(retry.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	var envelope r.Message
	if err = json.Unmarshal([]byte(plain), &envelope); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CheckDownstream(ctx, 1, envelope, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, retry.ID, retry.Claim, "sent", r.DeliveryReceipt{MessageIDs: []int64{801}}, "", now); err != nil {
		t.Fatal(err)
	}
	out = postRecovery(t, router, "downstream", row.ID, "reconcile", `{"expected_version":3,"evidence":"audit:receipt"}`, 200)
	if out.State != "delivered" || out.Version != 4 || out.ManualReview {
		t.Fatal(out)
	}
	var audits int64
	if err = db.Model(&d.ResolutionAudit{}).Where("resource = ? AND resource_id = ?", "downstream", row.ID).Count(&audits).Error; err != nil || audits != 3 {
		t.Fatal(audits, err)
	}
	var persisted rd.Delivery
	if err = db.First(&persisted, retry.ID).Error; err != nil || persisted.PayloadHeld {
		t.Fatal(persisted.ID, err)
	}
}

// TestDeliveryRecoveryHTTPUpstreamRetry preserves source/key and blocks a stale
// route before retry. It covers automatic failed mapping and explicit HTTP retry.
func TestDeliveryRecoveryHTTPUpstreamRetry(t *testing.T) {
	fixture := newRecoveryFixture(t)
	store, db, message, request, envelope, now := fixture.store, fixture.db, fixture.message, fixture.request, fixture.envelope, fixture.now
	queue := rstore.New(db)
	ctx := context.Background()
	if err := queue.EnqueueRequest(ctx, &request); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, "failed", r.DeliveryReceipt{}, "telegram rejected message", now); err != nil {
		t.Fatal(err)
	}
	if state, err := store.ReconcileReady(ctx, 1, message.ID, 1, now); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
	if next, err := store.ClaimReady(ctx, 1, message.SessionID, 1, "forbidden", now, time.Minute); err != nil || next != nil {
		t.Fatal("failed reclaimed", next, err)
	}
	router := recoveryHTTP(t, fixture)
	input := `{"expected_version":1,"evidence":"ticket:retry"}`
	fixture.setRoute(c.DeliveryBinding{ChatID: -200, RouteVersion: 2})
	postRecovery(t, router, "upstream", message.ID, "retry", input, 403)
	var proof rd.Delivery
	if err = db.First(&proof, claim.ID).Error; err != nil || proof.Status != "failed" {
		t.Fatal(proof.Status, err)
	}
	fixture.setRoute(c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	out := postRecovery(t, router, "upstream", message.ID, "retry", input, 200)
	if out.State != "sending" || !out.RetryQueued || out.Version != 2 {
		t.Fatal(out)
	}
	if err = store.AuthorizeTargetAt(ctx, 1, envelope, now); err != nil {
		t.Fatal(err)
	}
	retry, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || retry == nil || retry.ID != claim.ID || retry.Key != request.Key {
		t.Fatal(retry, err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, retry.ID, retry.Claim, "sent", r.DeliveryReceipt{MessageIDs: []int64{901}}, "", now); err != nil {
		t.Fatal(err)
	}
	out = postRecovery(t, router, "upstream", message.ID, "reconcile", `{"expected_version":2,"evidence":"audit:receipt"}`, 200)
	if out.State != "delivered" || out.Version != 3 {
		t.Fatal(out)
	}
}

// TestDeliveryRecoveryHTTPLateReceipt closes the actual unknown-review path:
// sweeper marks unknown, HTTP retains manual review, original runtime completion
// proves rejection, then the same HTTP retry safely requeues that exact row.
func TestDeliveryRecoveryHTTPLateReceipt(t *testing.T) {
	fixture := newRecoveryFixture(t)
	db, message, request, now := fixture.db, fixture.message, fixture.request, fixture.now
	queue := rstore.New(db)
	ctx := context.Background()
	if err := queue.EnqueueRequest(ctx, &request); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if next, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, now.Add(2*time.Minute), nil); err != nil || next != nil {
		t.Fatal(next, err)
	}
	router := recoveryHTTP(t, fixture)
	out := postRecovery(t, router, "upstream", message.ID, "reconcile", `{"expected_version":1,"evidence":"case:late"}`, 200)
	if !out.ManualReview || out.State != "unknown" {
		t.Fatal(out)
	}
	postRecovery(t, router, "upstream", message.ID, "retry", `{"expected_version":2,"evidence":"case:late"}`, 409)
	if err = queue.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, "failed", r.DeliveryReceipt{}, "telegram rejected message", now); err != nil {
		t.Fatal(err)
	}
	out = postRecovery(t, router, "upstream", message.ID, "retry", `{"expected_version":2,"evidence":"case:late"}`, 200)
	if out.ManualReview || !out.RetryQueued || out.DeliveryID != claim.ID {
		t.Fatal(out)
	}
}

// TestDeliveryRecoveryHTTPUnknownCannotBeForged exercises a genuine unknown
// receipt and strict JSON rejection. It verifies failed retries leave no audit
// and no claimable task; manual review keeps the original source ciphertext.
func TestDeliveryRecoveryHTTPUnknownCannotBeForged(t *testing.T) {
	fixture := newRecoveryFixture(t)
	db, message, request, now := fixture.db, fixture.message, fixture.request, fixture.now
	queue := rstore.New(db)
	ctx := context.Background()
	if err := queue.EnqueueRequest(ctx, &request); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = queue.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, "unknown", r.DeliveryReceipt{}, "delivery outcome unknown", now); err != nil {
		t.Fatal(err)
	}
	router := recoveryHTTP(t, fixture)
	for _, injected := range []string{`"message_ids":[99]`, `"runtime_state":"failed"`, `"generation":1`, `"delivery_id":1`, `"actor_id":9`, `"no_effect":true`} {
		postRecovery(t, router, "upstream", message.ID, "reconcile", `{"expected_version":1,"evidence":"case:review",`+injected+`}`, 400)
	}
	out := postRecovery(t, router, "upstream", message.ID, "reconcile", `{"expected_version":1,"evidence":"case:review"}`, 200)
	if out.State != "unknown" || !out.ManualReview || out.RetryQueued || out.EvidenceSource != "runtime_delivery" {
		t.Fatal(out)
	}
	postRecovery(t, router, "upstream", message.ID, "retry", `{"expected_version":2,"evidence":"case:review"}`, 409)
	if next, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().UTC(), nil); err != nil || next != nil {
		t.Fatal(next, err)
	}
	if err = db.First(&message, message.ID).Error; err != nil || message.PayloadCipher == "" || message.State != "unknown" {
		t.Fatal(message.ID, err)
	}
}
