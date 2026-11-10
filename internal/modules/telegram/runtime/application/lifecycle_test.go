package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// TestDeliveryAuthorizationCompatibility covers both historical representations,
// wrapped errors, invalidation and the policy-independent durable retry budget.
func TestDeliveryAuthorizationCompatibility(t *testing.T) {
	now := time.Now()
	for _, transient := range []bool{false, true} {
		value := c.DeliveryDeferred{AvailableAt: now.Add(time.Minute), Transient: transient}
		for _, failure := range []error{value, &value, fmt.Errorf("wrapped: %w", value), fmt.Errorf("wrapped: %w", &value)} {
			for _, attempts := range []int{1, 8, 100} {
				state, at, detail := deferredDelivery(failure, &d.Delivery{Attempts: attempts}, now)
				want := "retry"
				if transient && attempts >= 8 {
					want = "failed"
				}
				if state != want || (state == "retry" && !at.Equal(value.AvailableAt)) {
					t.Fatalf("wrong deferral: transient=%t attempts=%d state=%s", transient, attempts, state)
				}
				if !transient && detail != c.DeliveryPolicyDeferred {
					t.Fatal("policy claim consumed failure budget")
				}
			}
		}
	}
	for _, state := range []string{"superseded", "unreachable"} {
		value := c.DeliveryInvalidated{State: state}
		for _, failure := range []error{value, &value, fmt.Errorf("wrapped: %w", value), fmt.Errorf("wrapped: %w", &value)} {
			got, _, _ := deferredDelivery(failure, &d.Delivery{}, now)
			if got != state {
				t.Fatalf("lost invalidation: %s", got)
			}
		}
	}
}

// TestLocalReconcilePanicStaysInRound proves repeated callback panics back off,
// never cancel the generation and remain promptly joinable on shutdown.
func TestLocalReconcilePanicStaysInRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan time.Time, 3)
	runtime := NewRuntime(NewManager(&lifecycleStore{}, nil, "test-key"), nil, nil)
	runtime.ReconcileLocal = func(context.Context, uint) error {
		calls <- time.Now()
		panic("synthetic secret omitted from logs")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.reconcileLocal(ctx, &d.Instance{ID: 1}, cancel)
	}()
	var first time.Time
	select {
	case first = <-calls:
	case <-time.After(time.Second):
		t.Fatal("local work did not start")
	}
	select {
	case second := <-calls:
		if second.Sub(first) < 2*time.Second {
			t.Fatal("panic loop has no backoff")
		}
	case <-ctx.Done():
		t.Fatal("local panic cancelled entire bot")
	case <-time.After(4 * time.Second):
		t.Fatal("next round did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("local retry did not join")
	}
}

type panicInboxStore struct {
	*cleanupStore
	entries []*d.Inbox
	states  []string
	cancel  context.CancelFunc
}

// ClaimInbox returns sequential claims with already charged attempts.
func (store *panicInboxStore) ClaimInbox(_ context.Context, _ uint, claim string, _ time.Time) (*d.Inbox, error) {
	if len(store.entries) == 0 {
		return nil, nil
	}
	entry := store.entries[0]
	store.entries = store.entries[1:]
	entry.Claim = claim
	return entry, nil
}

// FinishInbox captures isolation and stops after two records finish.
func (store *panicInboxStore) FinishInbox(_ context.Context, _ uint, _, state, _ string, _ time.Time) error {
	store.states = append(store.states, state)
	if len(store.states) == 2 {
		store.cancel()
	}
	return nil
}

type panicHandler struct {
	c.Handler
	calls int
}

// Handle simulates an external action followed by panic on the first update.
func (handler *panicHandler) Handle(_ context.Context, _ uint, update c.Update) ([]c.Message, error) {
	handler.calls++
	if update.ID == 1 {
		panic("sensitive panic payload")
	}
	return nil, nil
}

// TestInboxPanicIsolatedAndConsumerContinues proves unknown actions are not retried.
func TestInboxPanicIsolatedAndConsumerContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &panicInboxStore{cleanupStore: &cleanupStore{}, cancel: cancel}
	manager := NewManager(store, nil, "test-key")
	for id := int64(1); id <= 2; id++ {
		raw, err := json.Marshal(c.Update{ID: id, UserID: 12, ChatID: 12, Private: true})
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := manager.Seal(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		store.entries = append(store.entries, &d.Inbox{ID: uint(id), BotID: 1, UpdateID: id, Attempts: 1, PayloadCipher: cipher})
	}
	handler := &panicHandler{}
	NewRuntime(manager, nil, handler).processInbox(ctx, &d.Instance{ID: 1}, "unused")
	if handler.calls != 2 || len(store.states) != 2 || store.states[0] != "unknown" || store.states[1] != "done" {
		t.Fatalf("calls=%d states=%v", handler.calls, store.states)
	}
}

type panicApplyAPI struct {
	c.API
	calls int
}

// Apply records the external attempt then panics without a receipt.
func (api *panicApplyAPI) Apply(context.Context, string, c.Message) (c.DeliveryResult, error) {
	api.calls++
	panic("transport outcome unavailable")
}

// TestDeliveryPanicAfterExternalActionIsUnknown covers sends and mutable effects.
func TestDeliveryPanicAfterExternalActionIsUnknown(t *testing.T) {
	for _, operation := range []string{"send", "edit"} {
		t.Run(operation, func(t *testing.T) {
			store := &deliveryGuardStore{cancel: func() {}}
			api := &panicApplyAPI{}
			manager := NewManager(store, api, "test-key")
			raw, err := json.Marshal(c.Message{ChatID: 12, Operation: operation})
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			item := &d.Delivery{ID: 1, BotID: 1, ChatID: 12, TelegramUserID: 12, UserID: 7, PayloadCipher: cipher, Attempts: 1}
			runtime := NewRuntime(manager, nil, acceptingPanelHandler{})
			runtime.processDelivery(context.Background(), &d.Instance{ID: 1}, "unused", &lifecycleLease{}, func() {}, item, "claim", map[int64]time.Time{})
			if api.calls != 1 || store.status != "unknown" {
				t.Fatalf("calls=%d state=%s", api.calls, store.status)
			}
		})
	}
}

type restartStore struct {
	*lifecycleStore
	generations atomic.Int32
	restarted   chan struct{}
}

// List supplies enabled desired state to the supervisor.
func (store *restartStore) List(context.Context) ([]d.Instance, error) {
	return []d.Instance{store.instance}, nil
}

// BeginGeneration signals replacement of the failed worker generation.
func (store *restartStore) BeginGeneration(context.Context, uint, uint64) (*d.Instance, error) {
	instance := store.instance
	instance.Generation = uint64(store.generations.Add(1))
	if instance.Generation == 2 {
		close(store.restarted)
	}
	return &instance, nil
}

// ClaimInbox injects a loop-level panic before claiming a record.
func (store *restartStore) ClaimInbox(context.Context, uint, string, time.Time) (*d.Inbox, error) {
	if store.generations.Load() == 1 {
		panic("claim failed")
	}
	return nil, nil
}

type blockingPollAPI struct{ c.API }

// Webhook permits polling without a platform connection.
func (blockingPollAPI) Webhook(context.Context, string) (string, error) { return "", nil }

// Poll waits for cancellation so sibling joins are observable.
func (blockingPollAPI) Poll(ctx context.Context, _ string, _ int64) ([]c.Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestSupervisorRestartsPanickedConsumer verifies recovery rather than silent exit.
func TestSupervisorRestartsPanickedConsumer(t *testing.T) {
	store := &restartStore{lifecycleStore: &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}}, restarted: make(chan struct{})}
	manager := NewManager(store, blockingPollAPI{}, "test-key")
	cipher, err := manager.Seal("dummy")
	if err != nil {
		t.Fatal(err)
	}
	store.instance.TokenCipher = cipher
	runtime := NewRuntime(manager, lifecycleLeaser{&lifecycleLease{}}, lifecycleHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Start(ctx) }()
	select {
	case <-store.restarted:
	case <-ctx.Done():
		t.Fatal("supervisor did not restart consumer")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestEncodeFailurePreservesOffset verifies batches cannot acknowledge data loss.
func TestEncodeFailurePreservesOffset(t *testing.T) {
	runtime := NewRuntime(&Manager{key: []byte("invalid")}, nil, nil)
	batch, next, err := runtime.encodeUpdates(1, 10, []c.Update{{ID: 20}})
	if err == nil || batch != nil || next != 10 {
		t.Fatalf("batch=%v next=%d err=%v", batch, next, err)
	}
}

// TestPublicAuthorizationTransientErrorsDeferred preserves explicit denials.
func TestPublicAuthorizationTransientErrorsDeferred(t *testing.T) {
	for _, input := range []error{context.DeadlineExceeded, c.DeliveryDeferred{AvailableAt: time.Now().Add(time.Minute)}} {
		var deferred c.DeliveryDeferred
		if !errors.As(deferPublicAuthorization(input), &deferred) {
			t.Fatal("transient authorization became terminal")
		}
	}
	denied := errors.New("denied")
	if deferPublicAuthorization(denied) != denied {
		t.Fatal("denial became retryable")
	}
}

type transientPublicHandler struct {
	c.Handler
	checks int
}

// AuthorizePublicReply injects a transient dependency error at final revalidation.
func (handler *transientPublicHandler) AuthorizePublicReply(context.Context, uint, c.Message) error {
	handler.checks++
	if handler.checks == 3 {
		return context.DeadlineExceeded
	}
	return nil
}

// TestPublicFinalAuthorizationDefersWithoutSending covers the last pre-send guard.
func TestPublicFinalAuthorizationDefersWithoutSending(t *testing.T) {
	store := &deliveryGuardStore{cancel: func() {}}
	api := &panicApplyAPI{}
	manager := NewManager(store, api, "test-key")
	message := c.Message{TargetType: c.TargetShopPrivate, ChatID: 12, Purpose: "public_notice", PublicReply: &c.PublicReplyRef{TelegramID: 12}}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := manager.Seal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	item := &d.Delivery{ID: 1, BotID: 1, ChatID: 12, TelegramUserID: 12, PayloadCipher: cipher, Purpose: message.Purpose}
	handler := &transientPublicHandler{}
	NewRuntime(manager, nil, handler).processDelivery(context.Background(), &d.Instance{ID: 1}, "unused", &lifecycleLease{}, func() {}, item, "claim", map[int64]time.Time{})
	if api.calls != 0 || store.status != "retry" || handler.checks != 3 {
		t.Fatalf("calls=%d status=%s checks=%d", api.calls, store.status, handler.checks)
	}
}

type panicAuthorizationHandler struct{ c.Handler }

// AuthorizeDelivery panics before transport so the bounded retry budget applies.
func (panicAuthorizationHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	panic("authorization dependency panic")
}

// TestUnsentDeliveryPanicHonorsAttemptBudget verifies retry then final isolation.
func TestUnsentDeliveryPanicHonorsAttemptBudget(t *testing.T) {
	for _, attempt := range []int{1, 8} {
		store := &deliveryGuardStore{cancel: func() {}}
		api := &panicApplyAPI{}
		manager := NewManager(store, api, "test-key")
		cipher, err := manager.Seal(`{"chat_id":12}`)
		if err != nil {
			t.Fatal(err)
		}
		item := &d.Delivery{ID: 1, BotID: 1, ChatID: 12, TelegramUserID: 12, UserID: 7, PayloadCipher: cipher, Attempts: attempt}
		NewRuntime(manager, nil, panicAuthorizationHandler{}).processDelivery(context.Background(), &d.Instance{ID: 1}, "unused", &lifecycleLease{}, func() {}, item, "claim", map[int64]time.Time{})
		want := "retry"
		if attempt == 8 {
			want = "failed"
		}
		if store.status != want || api.calls != 0 {
			t.Fatalf("attempt=%d status=%s calls=%d", attempt, store.status, api.calls)
		}
	}
}

type panicAnswerAPI struct {
	c.API
	calls atomic.Int32
}

// Answer records a one-shot external call then panics.
func (api *panicAnswerAPI) Answer(context.Context, string, string, string) error {
	api.calls.Add(1)
	panic("callback result unknown")
}

// TestCallbackPanicKeepsDedupAndAllowsNextCallback verifies one-shot isolation.
func TestCallbackPanicKeepsDedupAndAllowsNextCallback(t *testing.T) {
	api := &panicAnswerAPI{}
	scope := newCallbackScope(context.Background(), api, "unused")
	defer scope.close()
	first := scope.responder("first")
	first("")
	first("")
	scope.responder("second")("")
	if api.calls.Load() != 2 {
		t.Fatalf("calls=%d", api.calls.Load())
	}
}

type encodingPollAPI struct {
	c.API
	polls  int
	cancel context.CancelFunc
}

// Poll returns an encodable update until the retry proves the generation survived.
func (api *encodingPollAPI) Poll(context.Context, string, int64) ([]c.Update, error) {
	api.polls++
	if api.polls == 2 {
		api.cancel()
	}
	return []c.Update{{ID: 20}}, nil
}

// TestPollingEncodingFailureRetriesWithinGeneration verifies encryption failure
// neither advances the offset nor exits and churns the bot lease/generation.
func TestPollingEncodingFailureRetriesWithinGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api := &encodingPollAPI{cancel: cancel}
	store := &lifecycleStore{}
	runtime := NewRuntime(&Manager{Store: store, API: api, key: []byte("invalid")}, nil, nil)
	runtime.pollGeneration(ctx, &d.Instance{ID: 1, Offset: 10}, "unused", &lifecycleLease{})
	if api.polls != 2 {
		t.Fatalf("encoding failure exited generation after %d poll(s)", api.polls)
	}
}

type lifecycleStore struct {
	c.Store
	instance   d.Instance
	began      chan struct{}
	heartbeats atomic.Int32
}

func (s *lifecycleStore) BeginGeneration(context.Context, uint, uint64) (*d.Instance, error) {
	i := s.instance
	i.Generation++
	close(s.began)
	return &i, nil
}
func (s *lifecycleStore) Heartbeat(context.Context, uint, uint64, string, string, uint64) error {
	s.heartbeats.Add(1)
	return nil
}
func (s *lifecycleStore) Get(context.Context, uint) (*d.Instance, error) {
	i := s.instance
	i.Generation++
	return &i, nil
}
func (s *lifecycleStore) ClaimInbox(context.Context, uint, string, time.Time) (*d.Inbox, error) {
	return nil, nil
}
func (s *lifecycleStore) ClaimDelivery(context.Context, uint, string, time.Time) (*d.Delivery, error) {
	return nil, nil
}

type lifecycleLease struct {
	released atomic.Bool
	lose     bool
}

func (l *lifecycleLease) Renew(context.Context) error {
	if l.lose {
		return errors.New("lost")
	}
	return nil
}
func (l *lifecycleLease) Release(context.Context) error { l.released.Store(true); return nil }

type lifecycleLeaser struct{ lease *lifecycleLease }

func (l lifecycleLeaser) Acquire(context.Context, string) (c.Lease, error) { return l.lease, nil }

type lifecycleAPI struct {
	c.API
	polled chan struct{}
}

func (a *lifecycleAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (a *lifecycleAPI) Poll(ctx context.Context, _ string, _ int64) ([]c.Update, error) {
	close(a.polled)
	<-ctx.Done()
	return nil, ctx.Err()
}

type lifecycleHandler struct{ c.Handler }

type deliveryGuardStore struct {
	c.Store
	item   *d.Delivery
	cancel context.CancelFunc
	status string
}

func (s *deliveryGuardStore) ClaimDelivery(context.Context, uint, string, time.Time) (*d.Delivery, error) {
	item := s.item
	s.item = nil
	return item, nil
}
func (s *deliveryGuardStore) FinishDelivery(_ context.Context, _ uint, _ string, status string, _ int64, _ string, _ time.Time) error {
	s.status = status
	s.cancel()
	return nil
}

type deliveryGuardHandler struct {
	c.Handler
	checks int
}

func (h *deliveryGuardHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	h.checks++
	if h.checks > 1 {
		return errors.New("binding revoked")
	}
	return nil
}
func TestDeliveryRechecksAuthorizationImmediatelyBeforeSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &deliveryGuardStore{cancel: cancel}
	manager := NewManager(store, &apiStub{}, "test-key")
	cipher, err := manager.Seal(`{"text":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	store.item = &d.Delivery{ID: 1, BotID: 1, TelegramUserID: 12, ChatID: 12, UserID: 7, PayloadCipher: cipher}
	handler := &deliveryGuardHandler{}
	runtime := NewRuntime(manager, nil, handler)
	// apiStub.Send is deliberately absent: reaching it would panic.
	runtime.deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
	if handler.checks != 2 || store.status != "failed" {
		t.Fatalf("checks=%d status=%s", handler.checks, store.status)
	}
}

func TestCancelLongPollJoinsBeforeLeaseRelease(t *testing.T) {
	s := &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}
	api := &lifecycleAPI{polled: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	cipher, err := m.Seal("dummy")
	if err != nil {
		t.Fatal(err)
	}
	s.instance.TokenCipher = cipher
	lease := &lifecycleLease{}
	runtime := NewRuntime(m, lifecycleLeaser{lease}, lifecycleHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runtime.runBot(ctx, s.instance); close(done) }()
	select {
	case <-api.polled:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	if lease.released.Load() {
		t.Fatal("released lease during active poll")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not join children")
	}
	if !lease.released.Load() {
		t.Fatal("lease not released after draining")
	}
}
func TestLostLeaseDoesNotBeginPolling(t *testing.T) {
	s := &lifecycleStore{instance: d.Instance{ID: 1, TelegramID: 42, Enabled: true, Mode: "internal", Version: 1}, began: make(chan struct{})}
	api := &lifecycleAPI{polled: make(chan struct{})}
	m := NewManager(s, api, "test-key")
	cipher, _ := m.Seal("dummy")
	s.instance.TokenCipher = cipher
	lease := &lifecycleLease{lose: true}
	runtime := NewRuntime(m, lifecycleLeaser{lease}, lifecycleHandler{})
	runtime.runBot(context.Background(), s.instance)
	select {
	case <-api.polled:
		t.Fatal("lost owner polled Telegram")
	default:
	}
	if !lease.released.Load() {
		t.Fatal("lease cleanup omitted")
	}
}
