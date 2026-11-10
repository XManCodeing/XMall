package application

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// BlockAndDrainRecovery persists a cross-instance stop request, then waits for
// durable supervisor joins. It never calls global Stop or treats timeout/lease
// expiry as proof. On blocked/error the fence remains installed for safe retry.
// ctx bounds waiting; fence identifies the exact recovery task and old snapshot.
func (r *Runtime) BlockAndDrainRecovery(ctx context.Context, fence c.RecoveryFence) error {
	store, ok := r.manager.Store.(c.RecoveryFenceStore)
	if !ok {
		return c.ErrRecoveryBlocked
	}
	if err := store.BlockRecovery(ctx, fence); err != nil {
		return err
	}
	slog.Info("telegram recovery fence installed", "bot_id", fence.BotID, "task_id", fence.TaskID, "generation", fence.Generation)
	for {
		err := store.RecoveryDrained(ctx, fence)
		if err == nil {
			slog.Info("telegram runtime generation drain proven", "bot_id", fence.BotID, "task_id", fence.TaskID)
			return nil
		}
		if ctx.Err() != nil {
			return c.ErrRecoveryBlocked
		}
		if !errors.Is(err, c.ErrRecoveryBlocked) {
			return err
		}
		if !wait(ctx, 50*time.Millisecond) {
			return c.ErrRecoveryBlocked
		}
	}
}

// checkRecovery revalidates a worker snapshot before work; compatibility stores
// without durable recovery capability keep their ordinary lifecycle semantics.
func (r *Runtime) checkRecovery(ctx context.Context, instance *d.Instance) error {
	if store, ok := r.manager.Store.(c.RecoveryFenceStore); ok {
		return store.CheckRecovery(ctx, instance.ID, instance.Version, instance.Generation)
	}
	return ctx.Err()
}

// recordGenerationExit runs AFTER callback/worker joins and BEFORE lease release.
// Persistence failure leaves recovery blocked, never substitutes a timeout proof.
func (r *Runtime) recordGenerationExit(instance *d.Instance) {
	store, ok := r.manager.Store.(c.RecoveryFenceStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.RecordGenerationExit(ctx, instance.ID, instance.Version, instance.Generation); err != nil {
		slog.Error("telegram generation exit proof not persisted", "bot_id", instance.ID, "generation", instance.Generation)
	}
}

type leasedExecutionFence struct {
	store    c.Store
	lease    c.Lease
	instance d.Instance
	cancel   context.CancelFunc
}

func (f *leasedExecutionFence) Check(ctx context.Context, bot uint, generation uint64) error {
	if bot != f.instance.ID || generation != f.instance.Generation {
		return c.ErrStale
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := f.lease.Renew(checkCtx); err != nil {
		f.cancel()
		return c.ErrStale
	}
	i, err := f.store.Get(checkCtx, bot)
	if err != nil {
		return err
	}
	if i == nil || i.RecoveryTaskID != 0 || !i.Enabled || i.Mode != "internal" || i.State != "running" || i.Version != f.instance.Version || i.Generation != generation {
		return c.ErrStale
	}
	return ctx.Err()
}

// Subscription changes affect only future polling, never the persisted inbox or offset.
func (r *Runtime) poll(ctx context.Context, botID uint, token string, offset int64) ([]c.Update, error) {
	types := []string{"message", "callback_query"}
	if provider, ok := r.handler.(interface {
		AllowedUpdates(context.Context, uint) ([]string, error)
	}); ok {
		var err error
		types, err = provider.AllowedUpdates(ctx, botID)
		if err != nil {
			return nil, err
		}
	}
	types = append([]string(nil), types...)
	if err := c.ValidateSubscription(types); err != nil {
		return nil, err
	}
	if api, ok := r.manager.API.(c.SubscriptionAPI); ok {
		return api.PollWithSubscription(ctx, token, offset, types)
	}
	// Legacy Poll has a fixed basic subscription. A subset is not equivalent either.
	if len(types) != 2 || !((types[0] == "message" && types[1] == "callback_query") || (types[1] == "message" && types[0] == "callback_query")) {
		return nil, c.ErrSubscriptionUnsupported
	}
	return r.manager.API.Poll(ctx, token, offset)
}

func privateUpdate(u c.Update) bool {
	return u.Private && (u.ChatType == "" || u.ChatType == "private") && u.UserID > 0 && u.ChatID == u.UserID && u.SenderChat == nil
}

func shopContactUpdate(u c.Update) bool {
	if !privateUpdate(u) || u.Migration != nil {
		return false
	}
	switch u.Type {
	case "", "message", "callback_query":
		return true
	default:
		return false
	}
}

// Validate only the structural envelope, not current module subscriptions. Unknown
// events remain safe no-ops, and anonymous/channel senders need not have a user ID.
func validUpdate(u c.Update) bool {
	if u.ChatID == 0 || u.ThreadID < 0 {
		return false
	}
	if u.Private || u.ChatType == "private" {
		if !privateUpdate(u) {
			return false
		}
	} else {
		if u.ChatID >= 0 {
			return false
		}
		switch u.ChatType {
		case "group", "supergroup", "channel":
		default:
			return false
		}
	}
	switch u.Type {
	case "":
		return privateUpdate(u) // old persisted private envelopes
	case "message", "edited_message", "channel_post", "edited_channel_post":
		return u.MessageID > 0
	case "callback_query":
		return u.CallbackID != "" && u.UserID > 0 && u.MessageID > 0
	case "my_chat_member", "chat_member":
		return u.Member != nil && u.Member.Subject.ID > 0
	case "chat_join_request":
		return u.JoinRequest != nil && u.JoinRequest.User.ID > 0
	default:
		return false
	}
}

type unsupportedDelivery struct{}

func (unsupportedDelivery) Error() string     { return "delivery operation unsupported" }
func (unsupportedDelivery) StatusCode() int   { return 400 }
func (unsupportedDelivery) RetrySeconds() int { return 0 }
func (unsupportedDelivery) Uncertain() bool   { return false }

// deferredDelivery distinguishes an unsent quiet-hour deferral from a rejected authorization.
func deferredDelivery(err error, item *d.Delivery, now time.Time) (string, time.Time, string) {
	var invalid c.DeliveryInvalidated
	if errors.As(err, &invalid) && (invalid.State == "superseded" || invalid.State == "unreachable") {
		return invalid.State, now, "recipient authorization failed"
	}
	var deferred c.DeliveryDeferred
	if errors.As(err, &deferred) && deferred.AvailableAt.After(now) {
		if !deferred.Transient {
			return "retry", deferred.AvailableAt, c.DeliveryPolicyDeferred
		}
		// Attempts survives restarts. Policy claims are refunded atomically by
		// storage, so long quiet hours never exhaust this dependency-failure cap.
		if item.Attempts < 8 {
			return "retry", deferred.AvailableAt, "recipient authorization failed"
		}
	}
	return "failed", now, "recipient authorization failed"
}

// deferPublicAuthorization delays only identifiable transient dependency errors.
// Definite policy denial and malformed capabilities keep their terminal behavior.
func deferPublicAuthorization(err error) error {
	if err == nil {
		return nil
	}
	var deferred c.DeliveryDeferred
	if errors.As(err, &deferred) {
		return err
	}
	seconds := 5
	var network net.Error
	var api c.APIError
	temporary := errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
	if errors.As(err, &api) && (api.StatusCode() == 429 || api.StatusCode() >= 500 || api.Uncertain()) {
		temporary = true
		seconds = max(seconds, api.RetrySeconds())
	}
	if temporary {
		return c.DeliveryDeferred{AvailableAt: time.Now().Add(time.Duration(min(seconds, 300)) * time.Second), Transient: true}
	}
	return err
}

var errTarget = errors.New("telegram delivery target unauthorized")

func deliveryTarget(item *d.Delivery, message c.Message) (c.TargetType, error) {
	target := message.TargetType.Normalize()
	if err := validateTransferEnvelope(item, message); err != nil {
		return target, err
	}
	if !target.Valid() || c.TargetType(item.TargetType).Normalize() != target {
		return target, errTarget
	}
	switch message.TargetKind {
	case "", "text", "photo", "document":
	case "video", "voice", "audio", "animation", "sticker", "album":
		// Rich consultation media must never widen the legacy shop or member
		// operation envelope. Its content is still checked by the support guard.
		if target != c.TargetSupportPrivate && target != c.TargetSupportGroup {
			return target, errTarget
		}
	default:
		return target, errTarget
	}
	if (message.Purpose == "panel" || message.Purpose == "cleanup" || item.Purpose == "panel" || item.Purpose == "cleanup") && target != c.TargetShopPrivate {
		return target, errTarget
	}
	// Old private envelopes may omit chat_id; the durable row remains authoritative.
	if message.ChatID != 0 && message.ChatID != item.ChatID {
		return target, errTarget
	}
	if target == c.TargetShopPrivate {
		if item.ChatID <= 0 || item.ChatID != item.TelegramUserID || message.ThreadID != 0 {
			return target, errTarget
		}
	} else {
		if message.ChatID == 0 || message.BusinessRef == "" || message.ThreadID < 0 || item.Purpose != message.Purpose || item.Operation != message.Operation {
			return target, errTarget
		}
		switch target {
		case c.TargetSupportPrivate:
			if item.ChatID <= 0 || message.ThreadID != 0 {
				return target, errTarget
			}
		case c.TargetSupportGroup:
			if item.ChatID >= 0 || message.ThreadID <= 0 || message.RouteVersion == 0 {
				return target, errTarget
			}
		case c.TargetMemberOperation:
			if item.ChatID >= 0 || message.RouteVersion == 0 {
				return target, errTarget
			}
		}
	}
	return target, nil
}

// validateTransferEnvelope binds nested transport fields to the authenticated
// encrypted envelope; only the two explicit support delivery purposes opt in.
func validateTransferEnvelope(item *d.Delivery, message c.Message) error {
	request := message.Transfer
	if request == nil {
		return nil
	}
	if message.Operation != "send" || item.Operation != "send" || message.Purpose != item.Purpose ||
		(message.Purpose != "support_ready" && message.Purpose != "support_reply") ||
		(message.Purpose == "support_ready" && message.TargetType != c.TargetSupportGroup) ||
		(message.Purpose == "support_reply" && message.TargetType != c.TargetSupportPrivate) {
		return errTarget
	}
	if request.TargetType != message.TargetType || request.BusinessRef != message.BusinessRef || request.BusinessRef == "" ||
		request.ChatID != message.ChatID || request.ChatID != item.ChatID || request.ThreadID != message.ThreadID ||
		request.RouteVersion != message.RouteVersion || request.RouteVersion == 0 {
		return errTarget
	}
	if message.Text != "" || len(message.Buttons) != 0 || len(message.Photo) != 0 || message.PhotoURL != "" ||
		message.FileID != "" || message.MediaName != "" || message.ParseMode != "" || message.ForceDocument ||
		message.ReplacePanel || message.TargetMessageID != 0 || message.PanelRevision != 0 || message.PanelEpoch != "" ||
		message.OwnerUserID != 0 || message.IdentityStamp != "" {
		return errTarget
	}
	if len(request.Contents) == 0 || len(request.Contents) > 10 || request.ReplyMessageID < 0 {
		return errTarget
	}
	if request.SourceChatID != 0 || len(request.SourceMessageIDs) != 0 {
		if message.Purpose != "support_ready" || request.SourceChatID != item.TelegramUserID || request.SourceChatID <= 0 || request.ReplyMessageID != 0 || len(request.SourceMessageIDs) != len(request.Contents) {
			return errTarget
		}
		previous := int64(0)
		for _, id := range request.SourceMessageIDs {
			if id <= previous {
				return errTarget
			}
			previous = id
		}
	}
	expected := request.Contents[0].Kind
	if len(request.Contents) > 1 {
		expected = "album"
	}
	if message.TargetKind != expected {
		return errTarget
	}
	return nil
}

// executeTransfer requires receipt storage before sending and forces destination
// protection regardless of persisted client input. It never calls legacy Apply.
func (runtime *Runtime) executeTransfer(ctx context.Context, token string, message c.Message) (c.DeliveryResult, c.DeliveryReceipt, error) {
	var receipt c.DeliveryReceipt
	if _, ok := runtime.manager.Store.(c.DeliveryReceiptStore); !ok {
		return c.DeliveryResult{}, receipt, unsupportedDelivery{}
	}
	transport, ok := runtime.manager.API.(c.TransferAPI)
	if !ok {
		return c.DeliveryResult{}, receipt, unsupportedDelivery{}
	}
	request := *message.Transfer
	request.ProtectContent = true
	result, err := transport.Transfer(ctx, token, request)
	var first c.DeliveryResult
	if len(result.Messages) > 0 {
		first = result.Messages[0]
	}
	seen := make(map[int64]bool)
	valid := len(result.Messages) == len(request.Contents)
	for _, observation := range result.Messages {
		receipt.MessageIDs = append(receipt.MessageIDs, observation.MessageID)
		if observation.MessageID <= 0 || seen[observation.MessageID] {
			valid = false
		}
		seen[observation.MessageID] = true
	}
	// Observations plus any error (including 429) cannot enter automatic retries.
	if (err != nil && len(result.Messages) > 0) || (err == nil && !valid) {
		err = errors.New("uncertain transfer receipt")
	}
	return first, receipt, err
}

func (r *Runtime) authorizeMessage(ctx context.Context, botID uint, item *d.Delivery, message c.Message) error {
	if handled, err := r.authorizeFailureNotice(ctx, botID, item, message); handled {
		return err
	}
	target, err := deliveryTarget(item, message)
	if err != nil {
		return err
	}
	if message.PanelCleanup != nil {
		return r.authorizePanelCleanup(ctx, botID, item, message)
	}
	if message.JoinAbuseDelivery != nil || message.Purpose == "membership_join_abuse" || item.Purpose == "membership_join_abuse" {
		guard, ok := r.handler.(c.JoinAbuseDeliveryAuthorizer)
		if !ok || message.JoinAbuseDelivery == nil || target != c.TargetShopPrivate || item.Purpose != message.Purpose || item.Key != message.BusinessRef || item.UserID != message.OwnerUserID || item.ChatID != message.ChatID || item.TelegramUserID != message.ChatID || item.Operation != "send" || item.TargetMessageID != 0 || item.PanelEpoch != "" || item.PanelRevision != 0 {
			return errTarget
		}
		return guard.AuthorizeJoinAbuseDelivery(ctx, botID, message)
	}
	if message.ProactiveDelivery != nil || message.Purpose == "membership_proactive" || item.Purpose == "membership_proactive" {
		return r.authorizeProactive(ctx, botID, item, message)
	}
	if message.ReminderDelivery != nil || message.Purpose == "membership_reminder" || item.Purpose == "membership_reminder" {
		guard, ok := r.handler.(c.ReminderDeliveryAuthorizer)
		if !ok || message.ReminderDelivery == nil || target != c.TargetShopPrivate || item.Purpose != message.Purpose || item.Key != message.BusinessRef || item.UserID != message.OwnerUserID || message.OwnerUserID == 0 {
			return errTarget
		}
		if err := guard.AuthorizeReminderDelivery(ctx, botID, message); err != nil {
			return err
		}
	}
	if message.InviteDelivery != nil {
		guard, ok := r.handler.(c.InviteDeliveryAuthorizer)
		if !ok || target != c.TargetShopPrivate || item.Purpose != "panel" {
			return errTarget
		}
		if err := guard.AuthorizeInviteDelivery(ctx, botID, message); err != nil {
			return err
		}
	}
	if target == c.TargetShopPrivate && message.PublicReply != nil {
		guard, ok := r.handler.(c.PublicReplyAuthorizer)
		if !ok || item.UserID != 0 || message.OwnerUserID != 0 || item.TelegramUserID != message.PublicReply.TelegramID || item.ChatID != message.ChatID {
			return errTarget
		}
		return deferPublicAuthorization(guard.AuthorizePublicReply(ctx, botID, message))
	}
	if message.PublicReply != nil {
		return errTarget
	}
	if target == c.TargetShopPrivate {
		if item.UserID == 0 {
			return errTarget
		}
		// The reminder guard has already checked current account, binding row version,
		// exact recipient and reachability. Do not run the panel guard again: it has
		// no typed dependency retry contract and can turn a temporary DB error terminal.
		if message.ReminderDelivery != nil {
			return nil
		}
		if err := r.handler.AuthorizeDelivery(ctx, botID, item.TelegramUserID, item.UserID); err != nil {
			return err
		}
		return r.authorizeStamp(ctx, botID, item.TelegramUserID, message.IdentityStamp)
	}
	guard, ok := r.handler.(c.TargetAuthorizer)
	if !ok {
		return errTarget
	}
	return guard.AuthorizeTarget(ctx, botID, message)
}

// deliveryChatCooldown budgets support groups at 20 message units per minute.
// All topics share the chat budget; albums consume one unit per source, not per API call.
// Private transactions keep their independent one-second chat budget.
func deliveryChatCooldown(message c.Message) time.Duration {
	if message.TargetType != c.TargetSupportGroup {
		return time.Second
	}
	units := 1
	if message.Transfer != nil {
		units = max(1, len(message.Transfer.Contents))
	}
	return time.Duration(units) * 3 * time.Second
}

// Priority comes from the runtime's target policy, never arbitrary handler input.
func messagePriority(m c.Message) int {
	switch m.TargetType.Normalize() {
	case c.TargetMemberOperation:
		return 180
	case c.TargetSupportPrivate, c.TargetSupportGroup:
		return c.PrioritySupport
	default:
		switch m.Purpose {
		case "notice", "notification", "delivery":
			return c.PriorityTransaction
		case "cleanup":
			return 110
		default:
			return 100
		}
	}
}

func (r *Runtime) claimReadyDelivery(ctx context.Context, botID uint, generation uint64, claim string, nextChat map[int64]time.Time) (*d.Delivery, error) {
	now := time.Now()
	excluded := make([]int64, 0, len(nextChat))
	for chat, until := range nextChat {
		if until.After(now) {
			excluded = append(excluded, chat)
		} else {
			delete(nextChat, chat)
		}
	}
	if store, ok := r.manager.Store.(interface {
		ClaimDeliveryExcluding(context.Context, uint, uint64, time.Time, []int64) (*d.Delivery, error)
	}); ok {
		return store.ClaimDeliveryExcluding(ctx, botID, generation, now, excluded)
	}
	// Compatibility stores must release a cooling chat instead of sleeping with a
	// claimed item and blocking unrelated transactions behind it.
	item, err := r.manager.Store.ClaimDelivery(ctx, botID, claim, now.Add(time.Minute))
	if err == nil && item != nil && nextChat[item.ChatID].After(now) {
		err = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "retry", 0, "", nextChat[item.ChatID])
		return nil, err
	}
	return item, err
}
