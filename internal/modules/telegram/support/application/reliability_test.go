package application

import (
	"context"
	"errors"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"testing"
	"time"
)

type authorizationAPIError int

// Error exposes a fixed safe category; the remaining methods classify the fake API result.
func (authorizationAPIError) Error() string           { return "synthetic authorization response" }
func (failure authorizationAPIError) StatusCode() int { return int(failure) }
func (authorizationAPIError) RetrySeconds() int       { return 999999 }
func (authorizationAPIError) Uncertain() bool         { return false }

// TestAuthorizationRetryClassification preserves definite denials and caps only
// transient backoff; policy deadlines retain their original trusted boundary.
func TestAuthorizationRetryClassification(t *testing.T) {
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	for _, code := range []authorizationAPIError{400, 401, 403, 404} {
		if !errors.Is(deferSupportAuthorization(code, clock), c.ErrDenied) {
			t.Fatal("permanent API denial became retryable", code)
		}
	}
	for _, code := range []authorizationAPIError{429, 500} {
		var deferred r.DeliveryDeferred
		if !errors.As(deferSupportAuthorization(code, clock), &deferred) || !deferred.Transient || !deferred.AvailableAt.Equal(now.Add(5*time.Minute)) {
			t.Fatal("dependency backoff is unbounded", code)
		}
	}
}

type auditMembers struct{ failure error }

// ResolveSupportRoute returns a fixed current route for authorization regression tests.
func (auditMembers) ResolveSupportRoute(context.Context, uint) (g.SupportRoute, error) {
	return g.SupportRoute{ChatID: -100, RouteVersion: 1, Generation: 1}, nil
}

// AuthorizeSupportMember returns the injected live lookup outcome without network I/O.
func (members auditMembers) AuthorizeSupportMember(context.Context, uint, int64, int64, uint64) error {
	return members.failure
}

// TestAuthorizeTargetDefersTemporaryFailures covers the actual outbound entry point.
func TestAuthorizeTargetDefersTemporaryFailures(t *testing.T) {
	now := time.Now().UTC()
	for _, failure := range []error{context.DeadlineExceeded, errors.New("temporary database unavailable"), &r.DeliveryDeferred{AvailableAt: now.Add(time.Minute)}} {
		service := ReplyService{Store: &replyStore{}, Members: auditMembers{failure}, Now: func() time.Time { return now }}
		err := service.AuthorizeTarget(r.WithExecutionFence(context.Background(), &replyFence{}), 1, r.Message{})
		var deferred *r.DeliveryDeferred
		if !errors.As(err, &deferred) || !deferred.AvailableAt.After(now) {
			t.Fatal("temporary authorization permanently failed", err)
		}
	}
	for _, failure := range []error{c.ErrDenied, g.ErrCapability, g.ErrConflict} {
		service := ReplyService{Store: &replyStore{}, Members: auditMembers{failure}, Now: func() time.Time { return now }}
		if err := service.AuthorizeTarget(r.WithExecutionFence(context.Background(), &replyFence{}), 1, r.Message{}); !errors.Is(err, c.ErrDenied) {
			t.Fatal("definite denial became retryable", err)
		}
	}
}

// workerStore embeds unused ports so tests fail loudly if the worker expands its
// authority beyond the recovery/prepare methods explicitly exercised below.
type workerStore struct {
	c.Dispatches
	c.ReadyConsumer
	rows         []d.BufferedMessage
	reconciled   []uint
	claims       int
	prepared     bool
	missingTopic bool
}

func (store *workerStore) ScanReady(context.Context, uint, uint, int) ([]d.BufferedMessage, error) {
	return store.rows, nil
}
func (store *workerStore) ReconcileReady(_ context.Context, _ uint, id uint, _ uint64, _ time.Time) (string, error) {
	store.reconciled = append(store.reconciled, id)
	if store.missingTopic {
		return "topic_missing", nil
	}
	if id == 3 {
		return "unknown", nil
	}
	return "sending", nil
}
func (store *workerStore) PendingDispatch(_ context.Context, _ uint, id uint, _ uint64, _ time.Time) (*r.DeliveryRequest, error) {
	return &r.DeliveryRequest{BotID: 1, Key: c.ReadyBusinessRef(1, id)}, nil
}
func (store *workerStore) ClaimReady(context.Context, uint, uint, uint64, string, time.Time, time.Duration) (*d.BufferedMessage, error) {
	store.claims++
	return &d.BufferedMessage{ID: 1, SessionID: 1}, nil
}
func (store *workerStore) LoadReady(context.Context, uint, uint, uint64, string, time.Time) (c.PreparedReady, error) {
	return c.PreparedReady{Message: d.BufferedMessage{ID: 1, SessionID: 1}}, nil
}
func (store *workerStore) PrepareDispatch(context.Context, uint, uint, uint64, string, r.Message, time.Time) (r.DeliveryRequest, error) {
	store.prepared = true
	return r.DeliveryRequest{BotID: 1, Key: c.ReadyBusinessRef(1, 1)}, nil
}

type workerBuilder struct{}

func (workerBuilder) BuildReady(c.PreparedReady) (r.Message, error) { return r.Message{}, nil }

type workerQueue struct {
	calls   int
	failure error
}

func (queue *workerQueue) EnqueueDelivery(context.Context, r.DeliveryRequest) error {
	queue.calls++
	return queue.failure
}

func TestReadyWorkerNeverAcknowledgesEnqueue(t *testing.T) {
	store := &workerStore{rows: []d.BufferedMessage{{ID: 1, SessionID: 1, State: "ready"}, {ID: 2, State: "sending"}, {ID: 3, State: "unknown"}}}
	queue := &workerQueue{}
	worker := ReadyWorker{Store: store, Consumer: store, Queue: queue, Builder: workerBuilder{}, NewClaim: func() string { return "claim" }}
	cursor, err := worker.Tick(r.WithExecutionFence(context.Background(), &replyFence{}), 1, 1, 0, 3, time.Now())
	if err != nil || cursor != 3 || queue.calls != 2 || store.claims != 1 || !store.prepared || len(store.reconciled) != 2 {
		t.Fatal(cursor, err, queue, store)
	}
	// AckReady is deliberately not implemented: any enqueue-as-delivered shortcut
	// would panic here. Unknown is reconciled, never re-enqueued automatically.
}
func TestReadyWorkerEnqueueFailureRetainsPreparation(t *testing.T) {
	store := &workerStore{rows: []d.BufferedMessage{{ID: 1, SessionID: 1, State: "ready"}}}
	failure := errors.New("queue unavailable")
	queue := &workerQueue{failure: failure}
	worker := ReadyWorker{Store: store, Consumer: store, Queue: queue, Builder: workerBuilder{}, NewClaim: func() string { return "claim" }}
	cursor, err := worker.Tick(r.WithExecutionFence(context.Background(), &replyFence{}), 1, 1, 0, 10, time.Now())
	if !errors.Is(err, failure) || cursor != 0 || !store.prepared {
		t.Fatal(cursor, err)
	}
}

type denyResolver struct{ calls int }

func (authorizer *denyResolver) AuthorizeSupportResolution(context.Context, uint, uint) error {
	authorizer.calls++
	return c.ErrDenied
}
func TestResolverAuthorizesEveryAttempt(t *testing.T) {
	authorizer := &denyResolver{}
	resolver := Resolver{Topics: &topicPort{}, Dispatches: &workerStore{}, Authorizer: authorizer}
	for range 2 {
		if err := resolver.ResolveTopic(context.Background(), 1, 1, c.Resolution{ActorID: 9}, time.Now()); err != c.ErrDenied {
			t.Fatal(err)
		}
		if err := resolver.ResolveReady(context.Background(), 1, 1, 1, c.Resolution{ActorID: 9}, time.Now()); err != c.ErrDenied {
			t.Fatal(err)
		}
	}
	if authorizer.calls != 4 {
		t.Fatal(authorizer.calls)
	}
}

type topicPort struct{ c.Topics }

// Ensure simulates a completed replacement for bot/session and returns no error.
// The worker must reconcile again instead of acknowledging the original message.
func (store *workerStore) Ensure(_ context.Context, bot, session uint) error {
	if bot != 1 || session != 1 {
		return c.ErrDenied
	}
	store.missingTopic = false
	return nil
}

// TestReadyWorkerRestoresMissingTopic verifies one replacement and reconciliation.
// t supplies an in-memory fake; queue insertion is never considered delivery.
func TestReadyWorkerRestoresMissingTopic(t *testing.T) {
	store := &workerStore{missingTopic: true, rows: []d.BufferedMessage{{ID: 1, SessionID: 1, State: "sending"}}}
	queue := &workerQueue{}
	worker := ReadyWorker{Store: store, Consumer: store, Queue: queue, Builder: workerBuilder{}, Topics: store, NewClaim: func() string { return "claim" }}
	_, err := worker.Tick(r.WithExecutionFence(context.Background(), &replyFence{}), 1, 1, 0, 10, time.Now())
	if err != nil || store.missingTopic || len(store.reconciled) != 2 || queue.calls != 1 || store.claims != 0 {
		t.Fatal("missing topic recovery not scheduled", err)
	}
}
