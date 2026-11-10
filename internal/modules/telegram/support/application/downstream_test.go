package application

import (
	"context"
	"errors"
	"fmt"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"testing"
	"time"
)

type replyStore struct {
	c.Downstreams
	checks   int
	accepted int
	notes    int
}

func (store *replyStore) CheckDownstream(context.Context, uint, r.Message, time.Time) (*d.Downstream, error) {
	store.checks++
	return &d.Downstream{SourceChatID: -100, ActorID: 55, RouteVersion: 1}, nil
}
func (store *replyStore) AcceptDownstream(_ context.Context, _ uint, _ r.Update, note bool, _ time.Time) (*d.Downstream, error) {
	store.accepted++
	if note {
		store.notes++
	}
	return &d.Downstream{ID: 1}, nil
}
func (store *replyStore) DownstreamRequest(context.Context, uint, uint, time.Time) (*r.DeliveryRequest, error) {
	return &r.DeliveryRequest{BotID: 1, Key: "support:reply:1"}, nil
}

type nativeReplyStore struct{ c.NativeReplies }

func (nativeReplyStore) ResolveNativeReply(context.Context, uint, r.Update, time.Time) (*d.ReplyMapping, error) {
	return &d.ReplyMapping{ChatID: -100, RouteVersion: 1}, nil
}

type failedReplyResolution struct {
	c.NativeReplies
	failure error
}

// ResolveNativeReply injects a classified failure without persisting any reply.
// The unused inputs mirror the production resolver; the configured error is returned.
func (resolver failedReplyResolution) ResolveNativeReply(context.Context, uint, r.Update, time.Time) (*d.ReplyMapping, error) {
	return nil, resolver.failure
}

// TestReplyRejectionsDoNotBlockInbox checks permanent denial versus retryable
// failures and ensures already-persisted work never loses its enqueue retry.
func TestReplyRejectionsDoNotBlockInbox(t *testing.T) {
	transient := errors.New("synthetic database unavailable")
	for _, failure := range []error{c.ErrDenied, fmt.Errorf("wrapped: %w", c.ErrDenied), r.ErrInvalidTransfer, transient, context.DeadlineExceeded, r.ErrStale} {
		store, queue := &replyStore{}, &workerQueue{}
		service := ReplyService{Store: store, Replies: failedReplyResolution{failure: failure}, Members: &liveMembers{}, Queue: queue, Now: time.Now}
		source := r.Update{ID: 10, Type: "message", ThreadID: 20, IsTopicMessage: true}
		_, err := service.Handle(context.Background(), 1, source)
		permanent := errors.Is(failure, c.ErrDenied) || errors.Is(failure, r.ErrInvalidTransfer)
		if (permanent && err != nil) || (!permanent && !errors.Is(err, failure)) || store.accepted != 0 || queue.calls != 0 {
			t.Fatal("incorrect input failure classification", failure, err)
		}
		service.Replies = nativeReplyStore{}
		if _, err = service.Handle(r.WithExecutionFence(context.Background(), &replyFence{}), 1, source); err != nil || queue.calls != 1 {
			t.Fatal("next valid message not enqueued", err)
		}
	}
	queue := &workerQueue{failure: c.ErrDenied}
	service := ReplyService{Store: &replyStore{}, Replies: nativeReplyStore{}, Members: &liveMembers{}, Queue: queue, Now: time.Now}
	_, err := service.Handle(r.WithExecutionFence(context.Background(), &replyFence{}), 1, r.Update{Type: "message", ThreadID: 20, IsTopicMessage: true})
	if !errors.Is(err, c.ErrDenied) || queue.calls != 1 {
		t.Fatal("persisted reply lost enqueue retry", err)
	}
}

type liveMembers struct {
	removed bool
	calls   int
}

func (members *liveMembers) ResolveSupportRoute(context.Context, uint) (g.SupportRoute, error) {
	return g.SupportRoute{BotID: 1, ChatID: -100, RouteVersion: 1, Generation: 1}, nil
}
func (members *liveMembers) AuthorizeSupportMember(context.Context, uint, int64, int64, uint64) error {
	members.calls++
	if members.removed {
		return c.ErrDenied
	}
	return nil
}

type replyFence struct{ calls int }

func (fence *replyFence) Check(context.Context, uint, uint64) error { fence.calls++; return nil }

type deniedDownstreamResolution struct{ c.DownstreamResolutions }

func TestDownstreamResolverRequiresAdministratorEveryTime(t *testing.T) {
	authorizer := &denyResolver{}
	resolver := DownstreamResolver{Store: deniedDownstreamResolution{}, Authorizer: authorizer}
	for range 2 {
		if e := resolver.Resolve(context.Background(), 1, 1, 1, c.Resolution{ActorID: 9}, time.Now()); e != c.ErrDenied {
			t.Fatal(e)
		}
	}
	if authorizer.calls != 2 {
		t.Fatal(authorizer.calls)
	}
	resolver.Authorizer = nil
	if e := resolver.Resolve(context.Background(), 1, 1, 1, c.Resolution{ActorID: 9}, time.Now()); e != c.ErrDenied {
		t.Fatal(e)
	}
}

func TestReplyMembershipRecheckedAndOrdinaryChatIgnored(t *testing.T) {
	store := &replyStore{}
	members := &liveMembers{}
	queue := &workerQueue{}
	fence := &replyFence{}
	service := ReplyService{Store: store, Replies: nativeReplyStore{}, Members: members, Queue: queue, Now: time.Now}
	ctx := r.WithExecutionFence(context.Background(), fence)
	if output, e := service.Handle(ctx, 1, r.Update{Text: "internal ordinary"}); e != nil || len(output) != 0 || store.accepted != 0 || queue.calls != 0 {
		t.Fatal(output, e)
	}
	source := r.Update{Type: "message", ThreadID: 20, IsTopicMessage: true, UserID: 55, Reply: &r.ReplyReference{ChatID: -100, MessageID: 77}}
	if _, e := service.Handle(ctx, 1, source); e != nil || store.accepted != 1 || queue.calls != 1 {
		t.Fatal(e)
	}
	if e := service.RecordNote(ctx, 1, source); e != nil || store.notes != 1 || queue.calls != 1 {
		t.Fatal("note escaped", e)
	}
	if e := service.AuthorizeTarget(ctx, 1, r.Message{}); e != nil || store.checks != 2 {
		t.Fatal(e, store.checks)
	}
	members.removed = true
	if e := service.AuthorizeTarget(ctx, 1, r.Message{}); e != c.ErrDenied {
		t.Fatal("removed member sent", e)
	}
	if _, e := service.Handle(ctx, 1, source); e != nil {
		t.Fatal("permanently denied input must not block the inbox", e)
	}
	if store.accepted != 2 {
		t.Fatal("removed staff persisted", store.accepted)
	}
	if e := service.AuthorizeTarget(context.Background(), 1, r.Message{}); e != r.ErrStale {
		t.Fatal("unsupervised send", e)
	}
	if fence.calls < 4 {
		t.Fatal("execution ownership not checked", fence.calls)
	}
}
