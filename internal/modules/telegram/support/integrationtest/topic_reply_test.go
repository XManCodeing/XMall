package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	boot "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// topicReplyFixture installs real durable ingress/authorization and two isolated
// customer topics. The second topic intentionally has no delivered reply mapping.
func topicReplyFixture(t *testing.T) (guestGateFixture, *a.ReplyService, context.Context) {
	t.Helper()
	fixture := newGuestGate(t)
	ctx := r.WithExecutionFence(context.Background(), guestFence{})
	replies := &a.ReplyService{Store: fixture.repo, Replies: fixture.repo, Members: guestMembers{}, Queue: guestQueue{rs.New(fixture.db)}, Now: time.Now}
	storefront := shop.New(shop.Dependencies{Store: rs.New(fixture.db), Identity: noCreateIdentity{t: t}, Secret: "synthetic-topic-test"})
	fixture.dispatcher = boot.NewUpdateDispatcher(storefront, storefront, boot.UpdateModules{Support: fixture.service, SupportCallbacks: fixture.service, GroupMessages: replies, Targets: map[r.TargetType]r.TargetAuthorizer{
		r.TargetSupportPrivate: replies, r.TargetSupportGroup: fixture.repo,
	}})
	buffered := prepareGuestTopic(t, fixture, ctx)
	deliverGuestUpstream(t, fixture, ctx, buffered)
	second := d.Session{BotID: 1, UserID: 43, ChatID: 43, State: "open", Version: 1}
	mustGuest(t, fixture.db.Create(&second).Error)
	mustGuest(t, fixture.db.Create(&d.TopicMapping{BotID: 1, ChatID: -100, ThreadID: 21, SessionID: second.ID, TargetID: 1, RouteVersion: 1, State: "open", OperationID: 999, Version: 1}).Error)
	return fixture, replies, ctx
}

// routeCheckMembers injects a live membership outcome after the real store check.
// after can change persisted evidence to exercise the post-HTTP recheck.
type routeCheckMembers struct {
	guestMembers
	err   error
	after func()
}

// AuthorizeSupportMember returns the configured probe result without external I/O.
func (members routeCheckMembers) AuthorizeSupportMember(context.Context, uint, int64, int64, uint64) error {
	if members.after != nil {
		members.after()
	}
	return members.err
}

// TestReplyAuthorizationClassifiesRealRoute covers production SQL projection,
// both store checks and live membership, including exact trusted-clock backoff.
func TestReplyAuthorizationClassifiesRealRoute(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		change    map[string]any
		memberErr error
		post      bool
		delay     time.Duration
	}{
		{name: "fresh"},
		{name: "expired", change: map[string]any{"obs_observed_at": time.Now().Add(-6 * time.Minute)}, delay: 30 * time.Second},
		{name: "network", change: map[string]any{"obs_state": "unknown", "obs_error_class": "network", "obs_can_manage_topics": false}, delay: 30 * time.Second},
		{name: "rate_limited", change: map[string]any{"obs_state": "rate_limited", "obs_error_class": "rate_limited", "obs_retry_after_seconds": 73}, delay: 73 * time.Second},
		{name: "left_expired", change: map[string]any{"obs_state": "left", "obs_observed_at": time.Now().Add(-6 * time.Minute)}},
		{name: "kicked_network", change: map[string]any{"obs_member_status": "kicked", "obs_error_class": "network"}},
		{name: "permission_lost", change: map[string]any{"obs_can_manage_topics": false}},
		{name: "route_changed", change: map[string]any{"route_version": 2}},
		{name: "observation_route_changed", change: map[string]any{"obs_route_version": 2, "obs_error_class": "network"}},
		{name: "disabled", change: map[string]any{"enabled": false}},
		{name: "recovering", change: map[string]any{"recovery_task_id": 99}},
		{name: "live_network", memberErr: g.ErrCapabilityPending, delay: 30 * time.Second},
		{name: "live_429", memberErr: &g.CapabilityPending{RetryAfterSeconds: 73}, delay: 73 * time.Second},
		{name: "live_denied", memberErr: g.ErrCapability},
		{name: "post_expired", post: true, change: map[string]any{"obs_observed_at": time.Now().Add(-6 * time.Minute)}, delay: 30 * time.Second},
		{name: "post_denied", post: true, change: map[string]any{"obs_state": "left"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture, replies, ctx := topicReplyFixture(t)
			source := topicStaffMessage(300, 20)
			_, err := fixture.dispatcher.Handle(ctx, 1, source)
			mustGuest(t, err)
			var row d.Downstream
			mustGuest(t, fixture.db.Where("update_id = ?", source.ID).First(&row).Error)
			var delivery rd.Delivery
			mustGuest(t, fixture.db.Where("key = ?", c.DownstreamBusinessRef(row.ID)).First(&delivery).Error)
			envelope := gateEnvelope(t, fixture, delivery)
			now := time.Now().UTC()
			replies.Now = func() time.Time { return now }
			members := routeCheckMembers{err: scenario.memberErr}
			if scenario.change != nil {
				apply := func() {
					mustGuest(t, fixture.db.Table("telegram_group_targets").Where("id = ?", 1).Updates(scenario.change).Error)
				}
				if scenario.post {
					members.after = apply
				} else {
					apply()
				}
			}
			replies.Members = members
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			err = fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope)
			var deferred *r.DeliveryDeferred
			switch {
			case scenario.delay > 0:
				if !errors.As(err, &deferred) || !deferred.AvailableAt.Equal(now.Add(scenario.delay)) || errors.Is(err, c.ErrDenied) {
					t.Fatalf("expected retry at %s, got %v", now.Add(scenario.delay), err)
				}
			case scenario.name == "fresh":
				mustGuest(t, err)
			default:
				if !errors.Is(err, c.ErrDenied) || errors.As(err, &deferred) {
					t.Fatalf("expected permanent denial, got %v", err)
				}
			}
		})
	}
}

// topicStaffMessage returns a synthetic unquoted text update for the given topic.
func topicStaffMessage(id, thread int64) r.Update {
	return r.Update{ID: id, Type: "message", MessageKind: "text", ChatType: "supergroup", UserID: 55, ChatID: -100, ThreadID: thread, IsTopicMessage: true, MessageID: id + 100, Text: "staff answer"}
}

// TestDirectTopicRepliesKeepCustomerIsolation exercises the production dispatcher,
// encrypted queue, final authorization and receipt reconciliation for both owners.
func TestDirectTopicRepliesKeepCustomerIsolation(t *testing.T) {
	fixture, _, ctx := topicReplyFixture(t)
	for index, thread := range []int64{20, 21, 20, 21} {
		source := topicStaffMessage(int64(100+index), thread)
		if index == 1 {
			source.MessageKind, source.Text = "photo", ""
			source.Media = []r.Media{{Type: "photo", FileID: "synthetic-photo"}}
		}
		if index == 2 {
			source.Reply = &r.ReplyReference{ChatID: -100, ThreadID: 20, MessageID: 77}
		}
		if index == 3 {
			source.Reply = &r.ReplyReference{ChatID: -100, MessageID: thread}
		}
		_, err := fixture.dispatcher.Handle(ctx, 1, source)
		mustGuest(t, err)
		var row d.Downstream
		mustGuest(t, fixture.db.Where("update_id = ?", source.ID).First(&row).Error)
		var delivery rd.Delivery
		mustGuest(t, fixture.db.Where("key = ?", c.DownstreamBusinessRef(row.ID)).First(&delivery).Error)
		envelope := gateEnvelope(t, fixture, delivery)
		if envelope.ChatID != 42+(thread-20) || (index == 2) != (row.ReplyMappingID != 0) {
			t.Fatal("customer or explicit mapping changed", row.ID, envelope.ChatID)
		}
		mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
		_, err = fixture.dispatcher.Handle(ctx, 1, source)
		mustGuest(t, err)
		mustGuest(t, fixture.db.Model(&delivery).Updates(map[string]any{"status": "sent", "message_id": 900, "message_ids": "[900]"}).Error)
		state, err := fixture.repo.ReconcileDownstream(ctx, 1, row.ID, 1, time.Now())
		mustGuest(t, err)
		if state != "delivered" {
			t.Fatal("receipt not reconciled", state)
		}
	}
	var count int64
	mustGuest(t, fixture.db.Model(&d.Downstream{}).Count(&count).Error)
	if count != 4 {
		t.Fatal("duplicate reply persisted", count)
	}
}

// TestTopicRepliesRejectStaleAndInternalInputs ensures invalid references never
// fall back, and notes/commands cannot become customer-facing deliveries.
func TestTopicRepliesRejectStaleAndInternalInputs(t *testing.T) {
	fixture, replies, ctx := topicReplyFixture(t)
	for index, mutate := range []func(*r.Update){
		func(source *r.Update) { source.Reply = &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 20} },
		func(source *r.Update) { source.Reply = &r.ReplyReference{ChatID: -100, MessageID: 77} },
		func(source *r.Update) { source.Reply = &r.ReplyReference{ChatID: -100, MessageID: 999} },
		func(source *r.Update) { source.ChatID = -200 },
		func(source *r.Update) { source.UserID = 56 },
	} {
		source := topicStaffMessage(int64(200+index), 21)
		mutate(&source)
		if _, err := fixture.dispatcher.Handle(ctx, 1, source); err != nil && !errors.Is(err, c.ErrDenied) {
			t.Fatal("unexpected rejection error", index, err)
		}
		var accepted int64
		mustGuest(t, fixture.db.Model(&d.Downstream{}).Where("update_id = ?", source.ID).Count(&accepted).Error)
		if accepted != 0 {
			t.Fatal("untrusted source persisted", index)
		}
	}
	note := topicStaffMessage(210, 21)
	mustGuest(t, replies.RecordNote(ctx, 1, note))
	for index, text := range []string{"/note internal", "/support", " /unknown@bot"} {
		source := topicStaffMessage(int64(211+index), 21)
		source.Text = text
		_, err := fixture.dispatcher.Handle(ctx, 1, source)
		mustGuest(t, err)
	}
	var count int64
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "support_reply").Count(&count).Error)
	if count != 0 {
		t.Fatal("note or command escaped", count)
	}
	for _, recovery := range []uint{99, 0} {
		mustGuest(t, fixture.db.Model(&rd.Instance{}).Where("id = ?", 1).Update("recovery_task_id", recovery).Error)
		_, err := fixture.dispatcher.Handle(ctx, 1, topicStaffMessage(219, 21))
		if recovery != 0 {
			var accepted int64
			mustGuest(t, fixture.db.Model(&d.Downstream{}).Where("update_id = ?", 219).Count(&accepted).Error)
			if accepted != 0 {
				t.Fatal("recovery isolation bypassed")
			}
		}
		if recovery == 0 {
			mustGuest(t, err)
		}
	}
	for _, change := range []map[string]any{{"state": "closed"}, {"state": "open", "route_version": 2}} {
		mustGuest(t, fixture.db.Model(&d.TopicMapping{}).Where("thread_id = ?", 21).Updates(change).Error)
		if _, err := fixture.dispatcher.Handle(ctx, 1, topicStaffMessage(220, 21)); err != nil && !errors.Is(err, c.ErrDenied) {
			t.Fatal("unexpected stale route error", err)
		}
		var accepted int64
		mustGuest(t, fixture.db.Model(&d.Downstream{}).Where("update_id = ?", 220).Count(&accepted).Error)
		if accepted != 0 {
			t.Fatal("closed/stale route accepted")
		}
	}
}
