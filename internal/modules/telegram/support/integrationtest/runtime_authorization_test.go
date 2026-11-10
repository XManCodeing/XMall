package integrationtest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	ra "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// replyRuntimeAPI forbids outbound delivery while allowing the local poll loop.
type replyRuntimeAPI struct {
	r.API
	calls atomic.Int32
}

// Webhook returns the local test's no-webhook configuration without network I/O.
func (*replyRuntimeAPI) Webhook(context.Context, string) (string, error) { return "", nil }

// PollWithSubscription waits for cancellation; no external updates are manufactured.
func (*replyRuntimeAPI) PollWithSubscription(ctx context.Context, _ string, _ int64, _ []string) ([]r.Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Send records forbidden delivery attempts and returns a deterministic failure.
func (api *replyRuntimeAPI) Send(context.Context, string, r.Message) (int64, error) {
	api.calls.Add(1)
	return 0, errors.New("unexpected send")
}

// Transfer detects rich-media sends as well as the ordinary Send path.
func (api *replyRuntimeAPI) Transfer(context.Context, string, r.TransferRequest) (r.TransferResult, error) {
	api.calls.Add(1)
	return r.TransferResult{}, errors.New("unexpected transfer")
}

type replyRuntimeLease struct{}

// Renew and Release are local deterministic lease operations.
func (replyRuntimeLease) Renew(context.Context) error   { return nil }
func (replyRuntimeLease) Release(context.Context) error { return nil }

type replyRuntimeLeaser struct{}

// Acquire grants the single runtime worker used by this test.
func (replyRuntimeLeaser) Acquire(context.Context, string) (r.Lease, error) {
	return replyRuntimeLease{}, nil
}

// TestReplyAuthorizationPersistsRuntimeRetry covers real support authorization,
// encrypted runtime delivery and single-connection SQL completion, without HTTP.
func TestReplyAuthorizationPersistsRuntimeRetry(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		failure   error
		attempts  int
		want      string
		remaining int
	}{
		{"temporary", errors.New("temporary database outage"), 0, "retry", 1},
		{"route_expired", nil, 0, "retry", 1},
		{"exhausted", errors.New("unknown dependency failure"), 7, "failed", 8},
		{"denied", c.ErrDenied, 0, "failed", 1},
		{"quiet", &r.DeliveryDeferred{AvailableAt: time.Now().Add(time.Hour)}, 7, "retry", 7},
		{"wrapped_quiet", fmt.Errorf("wrapped: %w", &r.DeliveryDeferred{AvailableAt: time.Now().Add(time.Hour)}), 7, "retry", 7},
		{"invalidated", fmt.Errorf("wrapped: %w", &r.DeliveryInvalidated{State: "superseded"}), 0, "superseded", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture, replies, ctx := topicReplyFixture(t)
			_, err := fixture.dispatcher.Handle(ctx, 1, topicStaffMessage(400, 20))
			mustGuest(t, err)
			var delivery rd.Delivery
			mustGuest(t, fixture.db.Where("purpose = ?", "support_reply").First(&delivery).Error)
			mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("id <> ?", delivery.ID).Update("status", "superseded").Error)
			mustGuest(t, fixture.db.Model(&delivery).Update("attempts", scenario.attempts).Error)
			replies.Members = routeCheckMembers{err: scenario.failure}
			if scenario.name == "route_expired" {
				mustGuest(t, fixture.db.Table("telegram_group_targets").Where("id = 1").Update("obs_observed_at", time.Now().Add(-6*time.Minute)).Error)
			}
			mustGuest(t, fixture.db.AutoMigrate(&rd.GenerationExit{}, &rd.Inbox{}, &rd.Contact{}))
			token, err := fixture.manager.Seal("123:test")
			mustGuest(t, err)
			mustGuest(t, fixture.db.Model(&rd.Instance{}).Where("id = 1").Updates(map[string]any{"token_cipher": token, "version": 1, "generation": 0}).Error)
			api := &replyRuntimeAPI{}
			fixture.manager.API = api
			runtime := ra.NewRuntime(fixture.manager, replyRuntimeLeaser{}, fixture.dispatcher)
			runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			done := make(chan error, 1)
			go func() { done <- runtime.Start(runCtx) }()
			defer func() { cancel(); mustGuest(t, <-done) }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				mustGuest(t, fixture.db.First(&delivery, delivery.ID).Error)
				if delivery.Status == scenario.want {
					break
				}
				select {
				case <-runCtx.Done():
					t.Fatalf("delivery did not finish: %s", delivery.Status)
				case <-ticker.C:
				}
			}
			if delivery.Attempts != scenario.remaining || api.calls.Load() != 0 || delivery.MessageID != 0 {
				t.Fatalf("authorization budget/send mismatch: attempts=%d sends=%d", delivery.Attempts, api.calls.Load())
			}
			if scenario.want == "retry" && !delivery.AvailableAt.After(time.Now()) {
				t.Fatal("retry deadline missing")
			}
		})
	}
}
