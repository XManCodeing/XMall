package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type forbiddenGroup struct{}

func (forbiddenGroup) Error() string     { return "forbidden" }
func (forbiddenGroup) StatusCode() int   { return 403 }
func (forbiddenGroup) RetrySeconds() int { return 0 }
func (forbiddenGroup) Uncertain() bool   { return false }
func TestGroupForbiddenNeverMarksShopContactUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// No Contact implementation: any attempt to modify a shop contact panics.
	store := &deliveryGuardStore{cancel: cancel}
	api := &panelApplyAPI{err: forbiddenGroup{}}
	m := NewManager(store, api, "test-key")
	raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, BusinessRef: "s", RouteVersion: 1, ThreadID: 2, Operation: "send"})
	cipher, _ := m.Seal(string(raw))
	store.item = &d.Delivery{ID: 1, TargetType: "support_group", ChatID: -100, TelegramUserID: 12, PayloadCipher: cipher, Operation: "send"}
	h := &targetHandler{routedHandler: &routedHandler{}}
	NewRuntime(m, nil, h).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if store.status != "failed" || h.checks != 2 || api.calls != 1 {
		t.Fatalf("status=%s checks=%d calls=%d", store.status, h.checks, api.calls)
	}
}
func TestNewTargetDoesNotFallBackToLegacySend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &deliveryGuardStore{cancel: cancel}
	m := NewManager(store, &apiStub{}, "test-key")
	raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportPrivate, ChatID: 12, BusinessRef: "s"})
	cipher, _ := m.Seal(string(raw))
	store.item = &d.Delivery{ID: 1, TargetType: "support_private", ChatID: 12, PayloadCipher: cipher}
	NewRuntime(m, nil, &targetHandler{routedHandler: &routedHandler{}}).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if store.status != "failed" {
		t.Fatalf("state %s", store.status)
	}
}

type receiptAPI struct {
	c.API
	cancel    context.CancelFunc
	messageID int64
	calls     int
}

func (a *receiptAPI) Apply(context.Context, string, c.Message) (c.DeliveryResult, error) {
	a.calls++
	if a.cancel != nil {
		a.cancel()
	}
	return c.DeliveryResult{MessageID: a.messageID}, nil
}

type receiptStore struct {
	*deliveryGuardStore
	attempts  int
	messageID int64
	stale     bool
}

func (s *receiptStore) FinishDelivery(ctx context.Context, _ uint, _ string, status string, id int64, _ string, _ time.Time) error {
	s.attempts++
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.stale {
		return c.ErrStale
	}
	if s.attempts == 1 {
		return errors.New("temporary storage outage")
	}
	s.status, s.messageID = status, id
	s.cancel()
	return nil
}

// TestSupportCleanupRuntimeDelivery verifies delete envelopes traverse the normal worker.
// t supplies a mock transport; authorization and durable receipt persistence remain enabled.
func TestSupportCleanupRuntimeDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &deliveryGuardStore{cancel: cancel}
	api := &receiptAPI{messageID: 80}
	manager := NewManager(store, api, "test-key")
	message := c.Message{TargetType: c.TargetSupportPrivate, ChatID: 42, BusinessRef: "support:gate-cleanup:3", Purpose: "support_gate_cleanup", Operation: "delete", TargetMessageID: 80}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := manager.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	store.item = &d.Delivery{ID: 1, TargetType: "support_private", ChatID: 42, TelegramUserID: 42, PayloadCipher: cipher, Purpose: message.Purpose, Operation: message.Operation}
	handler := &targetHandler{routedHandler: &routedHandler{}}
	NewRuntime(manager, nil, handler).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if store.status != "sent" || api.calls != 1 || handler.checks != 2 {
		t.Fatal("cleanup did not traverse authorized worker", store.status, api.calls, handler.checks)
	}
}

func TestSupportReceiptPersistsAfterCancellationWithoutResending(t *testing.T) {
	for _, id := range []int64{0, 42} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		store := &receiptStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel}}
		api := &receiptAPI{cancel: cancel, messageID: id}
		m := NewManager(store, api, "test-key")
		raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, ThreadID: 2, RouteVersion: 1, BusinessRef: "support:ready:1:2", Operation: "send"})
		cipher, _ := m.Seal(string(raw))
		store.item = &d.Delivery{ID: 1, TargetType: "support_group", ChatID: -100, TelegramUserID: 12, PayloadCipher: cipher, Operation: "send"}
		NewRuntime(m, nil, &targetHandler{routedHandler: &routedHandler{}}).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
		cancel()
		want := "sent"
		if id == 0 {
			want = "unknown"
		}
		if api.calls != 1 || store.attempts != 2 || store.status != want || store.messageID != id {
			t.Fatalf("id=%d sends=%d writes=%d state=%s receipt=%d", id, api.calls, store.attempts, store.status, store.messageID)
		}
	}
}
func TestSupportReceiptPersistenceStopsAtStaleClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &receiptStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel}, stale: true}
	api := &receiptAPI{cancel: cancel, messageID: 42}
	m := NewManager(store, api, "test-key")
	raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportPrivate, ChatID: 12, BusinessRef: "support:reply:1", Operation: "send"})
	cipher, _ := m.Seal(string(raw))
	store.item = &d.Delivery{ID: 1, TargetType: "support_private", ChatID: 12, PayloadCipher: cipher, Operation: "send"}
	NewRuntime(m, nil, &targetHandler{routedHandler: &routedHandler{}}).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if api.calls != 1 || store.attempts != 1 {
		t.Fatalf("sends=%d writes=%d", api.calls, store.attempts)
	}
}

func TestStampedLegacyTaskRequiresStampGuard(t *testing.T) {
	r := NewRuntime(nil, nil, acceptingPanelHandler{})
	if err := r.authorizeMessage(context.Background(), 1, &d.Delivery{ChatID: 12, TelegramUserID: 12}, c.Message{IdentityStamp: "original-binding"}); err == nil {
		t.Fatal("missing stamp guard accepted sensitive task")
	}
}

type fencedClaimStore struct {
	*deliveryGuardStore
	generation uint64
	now        time.Time
	claim      string
}

func (s *fencedClaimStore) ClaimDeliveryExcluding(_ context.Context, _ uint, generation uint64, now time.Time, _ []int64) (*d.Delivery, error) {
	s.generation = generation
	s.now = now
	row := s.item
	s.item = nil
	if row != nil {
		row.Claim = "store-generated"
	}
	return row, nil
}
func (s *fencedClaimStore) FinishDelivery(ctx context.Context, id uint, claim, status string, messageID int64, detail string, next time.Time) error {
	s.claim = claim
	return s.deliveryGuardStore.FinishDelivery(ctx, id, claim, status, messageID, detail, next)
}
func TestExcludingClaimCarriesGenerationAndUsesStoreClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &fencedClaimStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel, item: &d.Delivery{ID: 1, PayloadCipher: "invalid"}}}
	NewRuntime(NewManager(store, &apiStub{}, "test-key"), nil, acceptingPanelHandler{}).deliver(ctx, &d.Instance{ID: 1, Generation: 19}, "dummy", &lifecycleLease{}, cancel)
	if store.generation != 19 || store.claim != "store-generated" || time.Since(store.now) < 0 || time.Since(store.now) > time.Second {
		t.Fatalf("generation=%d claim=%s now=%v", store.generation, store.claim, store.now)
	}
}

type deferredClaimStore struct {
	*deliveryGuardStore
	available time.Time
}

func (s *deferredClaimStore) FinishDelivery(_ context.Context, _ uint, _ string, status string, _ int64, _ string, next time.Time) error {
	s.status = status
	s.available = next
	return nil
}
func TestLegacyClaimDefersCoolingChatWithoutWaiting(t *testing.T) {
	until := time.Now().Add(time.Hour)
	store := &deferredClaimStore{deliveryGuardStore: &deliveryGuardStore{item: &d.Delivery{ID: 1, ChatID: 12}}}
	r := NewRuntime(NewManager(store, nil, "test-key"), nil, nil)
	start := time.Now()
	item, err := r.claimReadyDelivery(context.Background(), 1, 2, "claim", map[int64]time.Time{12: until})
	if err != nil || item != nil || store.status != "retry" || !store.available.Equal(until) || time.Since(start) > time.Second {
		t.Fatal("cooling target blocks scheduler")
	}
}
func TestNonShopCleanupCannotEnqueueShopActions(t *testing.T) {
	store := &cleanupStore{}
	m := NewManager(store, nil, "test-key")
	r := NewRuntime(m, nil, nil)
	raw, _ := json.Marshal(c.Message{TargetType: c.TargetSupportGroup, ChatID: -100, BusinessRef: "s", RouteVersion: 1, ThreadID: 2, Operation: "send"})
	cipher, _ := m.Seal(string(raw))
	item := &d.Delivery{ID: 1, TargetType: "support_group", ChatID: -100, TelegramUserID: 12, PayloadCipher: cipher, PreviousMessageID: 8}
	r.enqueuePanelCleanup(context.Background(), item)
	r.enqueueClearButtons(context.Background(), item)
	if len(store.queued) != 0 {
		t.Fatal("group entered shop cleanup")
	}
}
