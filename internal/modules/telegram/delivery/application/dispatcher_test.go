package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	events "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

type eventStoreStub struct {
	rows            []events.Event
	marked, retried int
	markErr         error
}

func (s *eventStoreStub) ScanBot(bot uint, now time.Time, limit int) ([]events.Event, error) {
	rows := []events.Event{}
	for _, row := range s.rows {
		if row.BotID == bot {
			rows = append(rows, row)
		}
	}
	return rows, nil
}
func (s *eventStoreStub) Scan(time.Time, int) ([]events.Event, error) { return s.rows, nil }
func (s *eventStoreStub) Claim(uint, string, time.Time, time.Duration) (bool, error) {
	return true, nil
}
func (s *eventStoreStub) MarkDelivered(uint, string, time.Time) error    { s.marked++; return s.markErr }
func (s *eventStoreStub) Retry(uint, string, time.Time, time.Time) error { s.retried++; return nil }

type renderStub struct{ err error }

func (r renderStub) Render(context.Context, events.Event) (runtime.Message, error) {
	return runtime.Message{Text: "sensitive delivery"}, r.err
}

type followupRenderer struct {
	renderStub
	queue *enqueueStub
	err   error
	calls int
}

func (r *followupRenderer) AfterEnqueue(_ context.Context, _ events.Event) error {
	if len(r.queue.keys) == 0 {
		return errors.New("notification must be enqueued first")
	}
	r.calls++
	return r.err
}
func TestPanelRefreshFailureDoesNotDuplicateNotification(t *testing.T) {
	store := &eventStoreStub{rows: []events.Event{{ID: 7, BotID: 2}}}
	queue := &enqueueStub{keys: map[string]string{}}
	renderer := &followupRenderer{queue: queue, err: errors.New("CAS conflict")}
	dispatcher := Dispatcher{Store: store, Renderer: renderer, Outbox: queue, Secret: "test-key"}
	if err := dispatcher.Reconcile(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if store.marked != 0 || store.retried != 1 || renderer.calls != 1 || len(queue.keys) != 1 {
		t.Fatal("refresh failure discarded event or notification")
	}
	renderer.err = nil
	if err := dispatcher.Reconcile(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if store.marked != 1 || renderer.calls != 2 || len(queue.keys) != 1 {
		t.Fatal("refresh retry duplicated notification")
	}
}

type enqueueStub struct {
	keys map[string]string
	err  error
}

func (q *enqueueStub) EnqueueMessage(_ context.Context, _ uint, key string, _ int64, _ uint, cipher string) error {
	if q.err != nil {
		return q.err
	}
	q.keys[key] = cipher
	return nil
}
func TestCommitAfterEnqueueFailureRetriesWithoutDuplicateMessage(t *testing.T) {
	store := &eventStoreStub{rows: []events.Event{{ID: 7, BotID: 2, TelegramID: 12, PrivateChatID: 12, UserID: 3}}, markErr: errors.New("lost ack")}
	queue := &enqueueStub{keys: map[string]string{}}
	dispatcher := Dispatcher{Store: store, Renderer: renderStub{}, Outbox: queue, Secret: "test-key"}
	if err := dispatcher.Reconcile(context.Background(), 2); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	store.markErr = nil
	if err := dispatcher.Reconcile(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(queue.keys) != 1 || store.marked != 2 {
		t.Fatal("event did not preserve deterministic key")
	}
	for _, cipher := range queue.keys {
		plain, err := crypto.Decrypt(crypto.DeriveKey("test-key"), cipher)
		if err != nil || plain == "" || cipher == plain {
			t.Fatal("message not encrypted")
		}
	}
}
func TestUnauthorizedOrUnavailableEventRemainsRecoverable(t *testing.T) {
	store := &eventStoreStub{rows: []events.Event{{ID: 7, BotID: 2}}}
	queue := &enqueueStub{keys: map[string]string{}}
	dispatcher := Dispatcher{Store: store, Renderer: renderStub{err: errors.New("denied")}, Outbox: queue, Secret: "test-key"}
	if err := dispatcher.Reconcile(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(queue.keys) != 0 || store.marked != 0 || store.retried != 1 {
		t.Fatal("unauthorized event leaked or was discarded")
	}
}
