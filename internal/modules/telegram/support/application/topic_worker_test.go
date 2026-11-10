package application

import (
	"context"
	"errors"
	"testing"
	"time"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

type topicEventStore struct {
	c.Topics
	events []c.TopicEvent
}

func (store *topicEventStore) ObserveTopic(_ context.Context, event c.TopicEvent, _ time.Time) error {
	store.events = append(store.events, event)
	return nil
}

func TestTopicReceiverConsumesServicesWithoutSending(t *testing.T) {
	store := &topicEventStore{}
	platform := &topicWorkerPlatform{}
	routes := &topicWorkerRoutes{route: g.SupportRoute{BotID: 2, TargetID: 1, ChatID: -100, RouteVersion: 1, Generation: 7}}
	worker := TopicWorker{Topics: store, Routes: routes, Platform: platform, Now: time.Now}
	ctx := r.WithExecutionFence(context.Background(), topicWorkerFence{})
	update := r.Update{ChatID: -100, MessageKind: "service", TopicService: &r.TopicServiceEvent{State: "closed"}}
	if handled, err := worker.ReceiveTopic(ctx, 2, 7, update); !handled || err != nil || len(store.events) != 1 {
		t.Fatal(handled, err, store.events)
	}
	for _, test := range []struct {
		ctx        context.Context
		generation uint64
		chat       int64
	}{
		{context.Background(), 7, -100}, {ctx, 6, -100}, {ctx, 7, -200},
	} {
		update.ChatID = test.chat
		if handled, err := worker.ReceiveTopic(test.ctx, 2, test.generation, update); !handled || err == nil {
			t.Fatal("rejected event fell into reply", handled, err)
		}
	}
	update.TopicService = nil
	if handled, err := worker.ReceiveTopic(ctx, 2, 7, update); !handled || err != nil {
		t.Fatal("ordinary service fell into reply", handled, err)
	}
	update.MessageKind = "text"
	if handled, err := worker.ReceiveTopic(ctx, 2, 7, update); handled || err != nil {
		t.Fatal("ordinary text swallowed", handled, err)
	}
	if platform.calls != 0 || len(store.events) != 1 {
		t.Fatal("service caused external effect")
	}
}

type closedTopicStore struct {
	topicWorkerStore
	reopens int
}

func (store *closedTopicStore) BeginTopicAction(_ context.Context, _, _ uint, _, _ uint64, action, claim string, _ time.Time, _ time.Duration) (c.TopicClaim, error) {
	if action != "reopen" {
		return c.TopicClaim{}, c.ErrDenied
	}
	store.reopens++
	return c.TopicClaim{Execute: true, Operation: d.TopicOperation{ID: 4, Action: action, Claim: claim}, Mapping: store.job.Mapping}, nil
}

func TestEnsureReopensClosedMappingAfterServiceEvent(t *testing.T) {
	for _, state := range []string{"succeeded", "failed"} {
		store := &closedTopicStore{topicWorkerStore: topicWorkerStore{job: c.TopicClaim{
			Operation: d.TopicOperation{ID: 4, State: state}, Mapping: &d.TopicMapping{ID: 3, ThreadID: 42, State: "closed", Version: 2}}}}
		platform := &topicWorkerPlatform{result: c.TopicResult{State: "succeeded"}}
		worker := TopicWorker{Topics: store, Routes: &topicWorkerRoutes{route: g.SupportRoute{BotID: 2, TargetID: 1, ChatID: -100, Generation: 7, RouteVersion: 1}},
			Platform: platform, NewClaim: func() string { return "reopen" }, Now: time.Now}
		if err := worker.Ensure(r.WithExecutionFence(context.Background(), topicWorkerFence{}), 2, 9); err != nil {
			t.Fatal(err)
		}
		if store.reopens != 1 || platform.calls != 1 || store.finishes != 1 {
			t.Fatal("closed mapping not reopened", state)
		}
	}
}

type topicWorkerRoutes struct {
	route  g.SupportRoute
	calls  int
	change bool
}

func (p *topicWorkerRoutes) ResolveSupportRoute(context.Context, uint) (g.SupportRoute, error) {
	p.calls++
	out := p.route
	if p.change && p.calls > 1 {
		out.RouteVersion++
	}
	return out, nil
}

type topicWorkerFence struct{ err error }

func (p topicWorkerFence) Check(context.Context, uint, uint64) error { return p.err }

type topicWorkerStore struct {
	c.Topics
	job              c.TopicClaim
	claims, finishes int
	result           c.TopicResult
	finishCtxErr     error
}

func (s *topicWorkerStore) ClaimTopic(context.Context, uint, uint, uint, uint64, uint64, int64, string, time.Time, time.Duration) (c.TopicClaim, error) {
	s.claims++
	return s.job, nil
}
func (s *topicWorkerStore) FinishTopic(ctx context.Context, _ uint, _ uint, _ uint64, _ string, result c.TopicResult, _ time.Time) error {
	s.finishes++
	s.result = result
	s.finishCtxErr = ctx.Err()
	return nil
}

type topicWorkerPlatform struct {
	calls  int
	result c.TopicResult
	cancel context.CancelFunc
}

func (p *topicWorkerPlatform) ExecuteTopic(context.Context, g.SupportRoute, string, int64, string) (c.TopicResult, error) {
	p.calls++
	if p.cancel != nil {
		p.cancel()
	}
	return p.result, nil
}
func TestTopicWorkerPersistsUnknownAndNeverReexecutesExistingClaim(t *testing.T) {
	for _, name := range []string{"success", "empty_receipt", "unknown_claim", "route_changed", "missing_lease", "lost_lease", "cancel_after_success", "rate_limit"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &topicWorkerStore{job: c.TopicClaim{Execute: true, Operation: d.TopicOperation{ID: 4, Action: "create", Claim: "claim"}}}
			routes := &topicWorkerRoutes{route: g.SupportRoute{TargetID: 1, BotID: 2, ChatID: -100, RouteVersion: 1, Generation: 7}}
			api := &topicWorkerPlatform{result: c.TopicResult{State: "succeeded", ThreadID: 8}}
			fence := topicWorkerFence{}
			switch name {
			case "rate_limit":
				api.result = c.TopicResult{State: "failed", RetryAfterSeconds: 37}
			case "empty_receipt":
				api.result.ThreadID = 0
			case "unknown_claim":
				store.job.Execute = false
				store.job.Operation.State = "unknown"
			case "route_changed":
				routes.change = true
			case "lost_lease":
				fence.err = errors.New("lease lost")
			case "cancel_after_success":
				api.cancel = cancel
			}
			if name != "missing_lease" {
				ctx = r.WithExecutionFence(ctx, fence)
			}
			worker := TopicWorker{Topics: store, Routes: routes, Platform: api, NewClaim: func() string { return "claim" }, Now: time.Now}
			_ = worker.Ensure(ctx, 2, 3)
			wantCall := name == "success" || name == "empty_receipt" || name == "cancel_after_success" || name == "rate_limit"
			if (api.calls == 1) != wantCall {
				t.Fatalf("calls=%d", api.calls)
			}
			if wantCall {
				want := "succeeded"
				if name == "rate_limit" {
					want = "failed"
					if store.result.RetryAfterSeconds != 37 {
						t.Fatal("worker discarded retry_after", store.result)
					}
				}
				if name == "empty_receipt" {
					want = "unknown"
				}
				if store.finishes != 1 || store.result.State != want || store.finishCtxErr != nil {
					t.Fatalf("finish=%d state=%s err=%v", store.finishes, store.result.State, store.finishCtxErr)
				}
			} else if store.finishes != 0 {
				t.Fatal("fabricated platform outcome")
			}
		})
	}
}
