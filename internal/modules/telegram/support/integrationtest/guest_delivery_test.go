package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	boot "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

type guestMembers struct{}

// ResolveSupportRoute returns the synthetic observed route for bot; no HTTP occurs.
func (guestMembers) ResolveSupportRoute(_ context.Context, bot uint) (g.SupportRoute, error) {
	return g.SupportRoute{BotID: bot, TargetID: 1, ChatID: -100, RouteVersion: 1, Generation: 1}, nil
}

// AuthorizeSupportMember accepts only the synthetic staff actor and exact route.
func (guestMembers) AuthorizeSupportMember(_ context.Context, bot uint, chat, actor int64, route uint64) error {
	if bot != 1 || chat != -100 || actor != 55 || route != 1 {
		return c.ErrDenied
	}
	return nil
}

type guestFence struct{}

// Check validates the synthetic runtime owner inputs and returns denial on mismatch.
func (guestFence) Check(_ context.Context, bot uint, generation uint64) error {
	if bot != 1 || generation != 1 {
		return r.ErrStale
	}
	return nil
}

type guestQueue struct{ *rs.Store }

// EnqueueDelivery passes request to the real durable queue and returns its error.
func (queue guestQueue) EnqueueDelivery(ctx context.Context, request r.DeliveryRequest) error {
	return queue.EnqueueRequest(ctx, &request)
}

// prepareGuestTopic verifies a real dispatcher challenge and establishes the
// synthetic platform topic receipt. It returns the released customer row.
func prepareGuestTopic(t *testing.T, fixture guestGateFixture, ctx context.Context) d.BufferedMessage {
	t.Helper()
	now := time.Now().UTC()
	mustGuest(t, fixture.db.AutoMigrate(&gd.Target{}, &gd.ModuleConfig{}, &d.TopicOperation{}, &d.TopicMapping{}, &d.ReplyMapping{}, &d.ResolutionAudit{}))
	mustGuest(t, fixture.db.Create(&gd.Target{ID: 1, Purpose: "support", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -100, OccupancySlot: "synthetic", Observation: gd.Observation{Generation: 1, RouteVersion: 1, BotTelegramID: 123, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, IsForum: true, CanManageTopics: true, State: "ready", ObservedAt: &now}}).Error)
	mustGuest(t, fixture.db.Create(&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 1, Version: 1}).Error)
	_, err := fixture.dispatcher.Handle(ctx, 1, guestMessage(1))
	mustGuest(t, err)
	var challenge d.Challenge
	mustGuest(t, fixture.db.First(&challenge).Error)
	callback := guestMessage(2)
	callback.Type, callback.CallbackID, callback.CallbackData = "callback_query", "synthetic-click", "support:"+c.VerificationAnswer(challenge, challenge.AnswerIndex)
	_, err = fixture.dispatcher.Handle(ctx, 1, callback)
	mustGuest(t, err)
	var buffered d.BufferedMessage
	mustGuest(t, fixture.db.Where("update_id = ?", 1).First(&buffered).Error)
	if buffered.State != "ready" || buffered.UserID != 42 {
		t.Fatal("guest challenge did not release upstream")
	}
	claim, err := fixture.repo.ClaimTopic(ctx, 1, buffered.SessionID, 1, 1, 1, -100, "synthetic-topic", now, time.Minute)
	mustGuest(t, err)
	mustGuest(t, fixture.repo.FinishTopic(ctx, 1, claim.Operation.ID, 1, "synthetic-topic", c.TopicResult{State: "succeeded", ThreadID: 20}, now))
	return buffered
}

// deliverGuestUpstream creates the exact transfer using real claims/encoder,
// records a synthetic transport receipt, and returns the authorized envelope.
func deliverGuestUpstream(t *testing.T, fixture guestGateFixture, ctx context.Context, buffered d.BufferedMessage) r.Message {
	t.Helper()
	now := time.Now().UTC()
	fixture.repo.WithReadyEncoder(c.EncodeReadyTransfer)
	claimed, err := fixture.repo.ClaimReady(ctx, 1, buffered.SessionID, 1, "synthetic-ready", now, time.Minute)
	mustGuest(t, err)
	if claimed == nil {
		t.Fatal("guest upstream not claimed")
	}
	prepared, err := fixture.repo.LoadReady(ctx, 1, claimed.ID, 1, "synthetic-ready", now)
	mustGuest(t, err)
	envelope, err := c.EncodeReadyTransfer(prepared.Source, prepared.Binding, c.ReadyBusinessRef(buffered.SessionID, claimed.ID))
	mustGuest(t, err)
	request, err := fixture.repo.PrepareDispatch(ctx, 1, claimed.ID, 1, "synthetic-ready", envelope, now)
	mustGuest(t, err)
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, buffered.SessionID, 55, 0, true, nil, "synthetic upstream block", now))
	if err = fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope); !errors.Is(err, c.ErrDenied) {
		t.Fatal("blocked upstream authorized", err)
	}
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, buffered.SessionID, 55, 1, false, nil, "synthetic upstream unblock", now))
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
	mustGuest(t, rs.New(fixture.db).EnqueueRequest(ctx, &request))
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("key = ?", request.Key).Updates(map[string]any{"status": "sent", "message_id": 77, "message_ids": "[77]"}).Error)
	state, err := fixture.repo.ReconcileReady(ctx, 1, claimed.ID, 1, now)
	mustGuest(t, err)
	if state != "delivered" {
		t.Fatal("upstream receipt not consumed", state)
	}
	return envelope
}

// TestGuestDispatcherUpstreamDownstreamBlockRecovery exercises both delivery
// target authorizers without creating a website user or bypassing business stores.
func TestGuestDispatcherUpstreamDownstreamBlockRecovery(t *testing.T) {
	fixture := newGuestGate(t)
	ctx := r.WithExecutionFence(context.Background(), guestFence{})
	queue := rs.New(fixture.db)
	replies := &a.ReplyService{Store: fixture.repo, Replies: fixture.repo, Members: guestMembers{}, Queue: guestQueue{queue}, Now: time.Now}
	storefront := shop.New(shop.Dependencies{Store: queue, Identity: noCreateIdentity{t: t}, Secret: "synthetic-guest-test"})
	fixture.dispatcher = boot.NewUpdateDispatcher(storefront, storefront, boot.UpdateModules{Support: fixture.service, SupportCallbacks: fixture.service, GroupMessages: replies, Targets: map[r.TargetType]r.TargetAuthorizer{
		r.TargetSupportPrivate: boot.SupportPrivateAuthorizer{Notices: fixture.service, Replies: replies},
		r.TargetSupportGroup:   fixture.repo,
	}})
	buffered := prepareGuestTopic(t, fixture, ctx)
	deliverGuestUpstream(t, fixture, ctx, buffered)
	source := r.Update{ID: 3, Type: "message", MessageKind: "text", ChatType: "supergroup", UserID: 55, ChatID: -100, ThreadID: 20, IsTopicMessage: true, MessageID: 80, Text: "staff reply", Reply: &r.ReplyReference{ChatID: -100, ThreadID: 20, MessageID: 77}}
	_, err := fixture.dispatcher.Handle(ctx, 1, source)
	mustGuest(t, err)
	var delivery rd.Delivery
	mustGuest(t, fixture.db.Where("purpose = ?", "support_reply").First(&delivery).Error)
	envelope := gateEnvelope(t, fixture, delivery)
	if envelope.ChatID != 42 || envelope.OwnerUserID != 0 {
		t.Fatal("reply requires website identity")
	}
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, buffered.SessionID, 55, 2, true, nil, "synthetic block", time.Now()))
	if err = fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope); !errors.Is(err, c.ErrDenied) {
		t.Fatal("blocked downstream authorized", err)
	}
	mustGuest(t, fixture.repo.SetSupportBlock(ctx, 1, buffered.SessionID, 55, 3, false, nil, "synthetic unblock", time.Now()))
	mustGuest(t, fixture.dispatcher.AuthorizeTarget(ctx, 1, envelope))
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("id = ?", delivery.ID).Updates(map[string]any{"status": "sent", "message_id": 90, "message_ids": "[90]"}).Error)
	var downstream d.Downstream
	mustGuest(t, fixture.db.First(&downstream).Error)
	state, err := fixture.repo.ReconcileDownstream(ctx, 1, downstream.ID, 1, time.Now())
	mustGuest(t, err)
	if state != "delivered" {
		t.Fatal("downstream receipt lost", state)
	}
}

// TestGuestProductionReadinessRemainsClosed checks nil capability even with an
// enabled module; recovery markers and stale generation never open ingress.
func TestGuestProductionReadinessRemainsClosed(t *testing.T) {
	fixture := newGuestGate(t)
	fixture.service.DeliveryReady = nil
	ctx := context.Background()
	for _, changes := range []map[string]any{{"state": "running"}, {"recovery_task_id": 9}, {"generation": 2}, {"enabled": false}} {
		mustGuest(t, fixture.db.Model(&rd.Instance{}).Where("id = ?", 1).Updates(changes).Error)
		ready, err := fixture.service.ReadyEnabled(ctx, 1)
		mustGuest(t, err)
		if ready {
			t.Fatal("unproven production readiness")
		}
		_, err = fixture.service.Handle(ctx, 1, guestMessage(1))
		mustGuest(t, err)
	}
	var count int64
	mustGuest(t, fixture.db.Model(&d.Session{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("closed production gate accepted guest")
	}
}
