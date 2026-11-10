package application

import (
	"context"
	"errors"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"log/slog"
	"time"
)

// ReplyService is opt-in group ingress and outbound authorization. It never
// returns an outbound Message from Handle: business persistence precedes enqueue.
type ReplyService struct {
	Store   c.Downstreams
	Replies c.NativeReplies
	Members c.SupportMembers
	Queue   ReadyQueue
	Now     func() time.Time
}

// DownstreamResolver is the administrator authorization boundary, separate from
// live support membership. It cannot inject IDs; Store requires runtime receipts.
type DownstreamResolver struct {
	Store      c.DownstreamResolutions
	Authorizer ResolutionAuthorizer
}

// Resolve checks actor permission on every attempt before the persistent CAS.
// generation is the current trusted runtime generation; in contains audit only.
func (resolver *DownstreamResolver) Resolve(ctx context.Context, bot, id uint, generation uint64, in c.Resolution, now time.Time) error {
	if resolver.Store == nil || resolver.Authorizer == nil {
		return c.ErrDenied
	}
	if err := resolver.Authorizer.AuthorizeSupportResolution(ctx, in.ActorID, bot); err != nil {
		return err
	}
	return resolver.Store.ResolveDownstream(ctx, bot, id, generation, in, now)
}

// Handle routes customer-topic messages, with or without an explicit reply.
// General group chat, service events and commands never enter the delivery path.
func (service *ReplyService) Handle(ctx context.Context, bot uint, update r.Update) ([]r.Message, error) {
	if update.ThreadID <= 0 || !update.IsTopicMessage || update.Type != "message" || update.MessageKind == "service" || update.TopicService != nil || c.DownstreamCommand(update) {
		return nil, nil
	}
	rowID, err := service.accept(ctx, bot, update, false)
	if rowID == 0 && (errors.Is(err, c.ErrDenied) || errors.Is(err, r.ErrInvalidTransfer)) {
		// Permanent input rejection must not occupy the serial inbox for eight
		// retries. Persisted work and transient infrastructure failures still retry.
		slog.WarnContext(ctx, "support_reply_rejected", "bot_id", bot, "update_id", update.ID)
		return nil, nil
	}
	return nil, err
}

// RecordNote explicitly records an internal note and never enqueues a delivery.
// Call this only from a distinct trusted UI/command, never infer it from text.
func (service *ReplyService) RecordNote(ctx context.Context, bot uint, update r.Update) error {
	_, err := service.accept(ctx, bot, update, true)
	return err
}

// accept authorizes the observed actor and current route before persisting update.
// note selects internal-only storage; the result is the durable downstream ID.
func (service *ReplyService) accept(ctx context.Context, bot uint, update r.Update, note bool) (uint, error) {
	if service.Store == nil || service.Replies == nil || service.Members == nil || service.Now == nil {
		return 0, c.ErrDenied
	}
	now := service.Now().UTC()
	mapping, err := service.Replies.ResolveNativeReply(ctx, bot, update, now)
	if err != nil || mapping == nil {
		return 0, err
	}
	route, err := service.Members.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return 0, err
	}
	if route.ChatID != mapping.ChatID || route.RouteVersion != mapping.RouteVersion {
		return 0, c.ErrDenied
	}
	if err = r.CheckExecutionFence(ctx, bot, route.Generation); err != nil {
		return 0, err
	}
	if err = service.Members.AuthorizeSupportMember(ctx, bot, mapping.ChatID, update.UserID, mapping.RouteVersion); err != nil {
		return 0, err
	}
	row, err := service.Store.AcceptDownstream(ctx, bot, update, note, now)
	if err != nil {
		return 0, err
	}
	if note {
		return row.ID, nil
	}
	if row.State == "rejected" {
		slog.InfoContext(ctx, "support_reply_content_rejected", "bot_id", bot, "downstream_id", row.ID)
	}
	if service.Queue == nil {
		return row.ID, c.ErrDenied
	}
	request, err := service.Store.DownstreamRequest(ctx, bot, row.ID, now)
	if err != nil || request == nil {
		return row.ID, err
	}
	return row.ID, service.Queue.EnqueueDelivery(ctx, *request)
}

// AuthorizeTarget rechecks source, mapping and session before and after the live
// membership HTTP call, outside DB transactions, then renews execution ownership.
func (service *ReplyService) AuthorizeTarget(ctx context.Context, bot uint, envelope r.Message) (result error) {
	defer func() { result = deferSupportAuthorization(result, service.Now) }()
	if service.Store == nil || service.Members == nil || service.Now == nil {
		return c.ErrDenied
	}
	row, err := service.Store.CheckDownstream(ctx, bot, envelope, service.Now().UTC())
	if err != nil {
		return err
	}
	route, err := service.Members.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return err
	}
	if route.ChatID != row.SourceChatID || route.RouteVersion != row.RouteVersion {
		return c.ErrDenied
	}
	if err = r.CheckExecutionFence(ctx, bot, route.Generation); err != nil {
		return err
	}
	if err = service.Members.AuthorizeSupportMember(ctx, bot, row.SourceChatID, row.ActorID, row.RouteVersion); err != nil {
		return err
	}
	if _, err = service.Store.CheckDownstream(ctx, bot, envelope, service.Now().UTC()); err != nil {
		return err
	}
	return r.CheckExecutionFence(ctx, bot, route.Generation)
}

// deferSupportAuthorization keeps definitely unsent infrastructure failures retryable.
// err is an authorization result; now supplies the trusted backoff clock. Explicit
// denials and route changes remain permanent and never acquire new permissions.
func deferSupportAuthorization(err error, now func() time.Time) error {
	if err == nil || now == nil || errors.Is(err, r.ErrStale) {
		return err
	}
	var deferred r.DeliveryDeferred
	var invalid r.DeliveryInvalidated
	if errors.As(err, &deferred) || errors.As(err, &invalid) {
		return err
	}
	if errors.Is(err, c.ErrDenied) || errors.Is(err, r.ErrInvalidTransfer) || errors.Is(err, g.ErrCapability) || errors.Is(err, g.ErrConflict) || errors.Is(err, g.ErrInvalid) || errors.Is(err, g.ErrNotFound) {
		return c.ErrDenied
	}
	var api r.APIError
	if errors.As(err, &api) && api.StatusCode() >= 400 && api.StatusCode() < 500 && api.StatusCode() != 429 {
		return c.ErrDenied
	}
	delay := 30 * time.Second
	var retry interface{ RetrySeconds() int }
	if errors.As(err, &retry) && retry.RetrySeconds() > 0 {
		delay = time.Duration(min(retry.RetrySeconds(), 300)) * time.Second
	}
	return &r.DeliveryDeferred{AvailableAt: now().UTC().Add(delay), Transient: true}
}

// Tick repairs pre-enqueue crashes and sent acknowledgements in one bounded page.
// Runtime owns retries; unknown and internal notes never enter this enqueue path.
func (service *ReplyService) Tick(ctx context.Context, bot uint, generation uint64, after uint, limit int) (uint, error) {
	if service.Store == nil || service.Queue == nil || service.Now == nil {
		return after, c.ErrDenied
	}
	if err := r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return after, err
	}
	rows, err := service.Store.ScanDownstream(ctx, bot, after, limit)
	if err != nil {
		return after, err
	}
	var failures []error
	for _, row := range rows {
		after = row.ID
		state, e := service.Store.ReconcileDownstream(ctx, bot, row.ID, generation, service.Now().UTC())
		if e == nil && (state == "pending" || state == "collecting" || state == "rejected") {
			request, requestErr := service.Store.DownstreamRequest(ctx, bot, row.ID, service.Now().UTC())
			e = requestErr
			if e == nil && request != nil {
				e = service.Queue.EnqueueDelivery(ctx, *request)
			}
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	if len(rows) < limit {
		after = 0
	}
	return after, errors.Join(failures...)
}
