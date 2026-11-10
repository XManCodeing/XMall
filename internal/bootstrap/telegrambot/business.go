package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/crypto"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	userapp "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
	o "github.com/XManCodeing/XMall/internal/modules/order/domain"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	groupdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtimeapi "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/productmedia"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	supportapp "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	supportcontract "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	supportapi "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/botapi"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	deliveryapp "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/application"
	events "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	shopcontract "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// MembershipProductLinks resolves current membership targets without exposing routes to shop.
type MembershipProductLinks struct {
	Codes   membership.ShareResolver
	Configs membership.ConfigReader
	Targets interface {
		Get(context.Context, uint) (*groupdomain.Target, error)
	}
}

// ResolveProductLink returns the persisted product ID, rejecting missing codes without fallback.
func (access MembershipProductLinks) ResolveProductLink(ctx context.Context, code string) (uint, error) {
	if access.Codes == nil {
		return 0, c.ErrUnavailable
	}
	id, err := access.Codes.ResolveProductShare(ctx, code)
	if errors.Is(err, membership.ErrNotFound) {
		return 0, c.ErrUnavailable
	}
	return id, err
}

// AuthorizeProductLink requires every current product target to belong to actor.BotID.
// Missing, disabled, recovering or mismatched relations return an unavailable error.
func (access MembershipProductLinks) AuthorizeProductLink(ctx context.Context, actor c.Actor, productID uint) error {
	if access.Configs == nil || access.Targets == nil || productID == 0 || actor.BotID == 0 {
		return c.ErrUnavailable
	}
	config, err := access.Configs.Config(ctx, productID)
	if errors.Is(err, membership.ErrNotFound) {
		return c.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if config == nil || config.ProductID != productID || !config.Enabled || config.Quantity != 1 || len(config.Targets) == 0 {
		return c.ErrUnavailable
	}
	for _, relation := range config.Targets {
		if relation.ProductID != productID || relation.TargetID == 0 {
			return c.ErrUnavailable
		}
		target, err := access.Targets.Get(ctx, relation.TargetID)
		if errors.Is(err, groups.ErrNotFound) {
			return c.ErrUnavailable
		}
		if err != nil {
			return err
		}
		if target == nil || target.ID != relation.TargetID || target.Purpose != "membership" || !target.Enabled || target.RecoveryTaskID != 0 || target.BotInstanceID != actor.BotID {
			return c.ErrUnavailable
		}
	}
	return nil
}

// NewLeaseClient uses the queue's Redis, not the optional application cache.
// No connection is opened or goroutine started during dependency construction.
func NewLeaseClient(cfg config.QueueConfig) *redis.Client {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port <= 0 {
		port = 6379
	}
	return redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", host, port), Password: cfg.Password, DB: cfg.DB, ContextTimeoutEnabled: true, DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, MaxRetries: -1})
}
func NewManager(store runtime.Store, secret string) *runtimeapp.Manager {
	return runtimeapp.NewManager(store, runtimeapi.Adapter{}, secret)
}

// NewManagerWithMedia shares the upload root supplied by the composition root.
func NewManagerWithMedia(store runtime.Store, secret, uploadRoot string) *runtimeapp.Manager {
	cache, _ := store.(productmedia.Cache)
	return runtimeapp.NewManagerWithMediaReader(store, productmedia.New(runtimeapi.Adapter{}, uploadRoot, cache), secret, func(ref string, maxBytes int64) ([]byte, error) {
		return productmedia.ReadUploadFile(uploadRoot, ref, maxBytes)
	})
}

// UpdateShop retains stamp authorization when wrapping the shop handler.
type UpdateShop interface {
	runtime.Handler
	AuthorizeStamp(context.Context, uint, int64, string) error
}

// LegacyInputRouter consumes legacy inputs before support sees them.
// A handled result including errors must never fall through to consultation.
type LegacyInputRouter interface {
	ConsumeLegacyInput(context.Context, uint, runtime.Update) ([]runtime.Message, bool, error)
}

type UpdateReceiver interface {
	Handle(context.Context, uint, runtime.Update) ([]runtime.Message, error)
}

// SupportIngress gates new consultations only, independently from events and recovery.
type SupportIngress interface {
	UpdateReceiver
	ReadyEnabled(context.Context, uint) (bool, error)
}

// SupportOperations exposes explicit preparation and opaque callback execution.
// Callers supply a genuine trusted update and runtime lease context.
type SupportOperations interface {
	UpdateReceiver
	Prepare(context.Context, uint, supportcontract.OperationIntent) (string, error)
}

type UpdateSubscriptions interface {
	AllowedUpdates(context.Context, uint) ([]string, error)
}

// UpdateModules is immutable after construction. Receivers validate their own references,
// actor, route and expiry; registration never grants delivery permission.
type UpdateModules struct {
	Profiles interface {
		Sync(context.Context, runtime.Update) error
	}
	Support           SupportIngress
	SupportCallbacks  UpdateReceiver
	SupportOperations SupportOperations
	MemberCallbacks   UpdateReceiver
	GroupMessages     UpdateReceiver
	Events            map[string]UpdateReceiver
	Targets           map[runtime.TargetType]runtime.TargetAuthorizer
	Subscriptions     []UpdateSubscriptions
}

type UpdateDispatcher struct {
	shop    UpdateShop
	legacy  LegacyInputRouter
	modules UpdateModules
}

var ErrUpdateTargetDenied = errors.New("telegram update target denied")

func NewUpdateDispatcher(shop UpdateShop, legacy LegacyInputRouter, modules UpdateModules) *UpdateDispatcher {
	// Copy registries to prevent mutation while workers are running.
	events := make(map[string]UpdateReceiver, len(modules.Events))
	for typ, receiver := range modules.Events {
		events[typ] = receiver
	}
	targets := make(map[runtime.TargetType]runtime.TargetAuthorizer, len(modules.Targets))
	for typ, authorizer := range modules.Targets {
		targets[typ] = authorizer
	}
	modules.Events, modules.Targets = events, targets
	modules.Subscriptions = append([]UpdateSubscriptions(nil), modules.Subscriptions...)
	return &UpdateDispatcher{shop: shop, legacy: legacy, modules: modules}
}

// AuthorizePublicReply preserves the shop's exact persisted public-reply capability.
func (d *UpdateDispatcher) AuthorizePublicReply(ctx context.Context, bot uint, message runtime.Message) error {
	guard, ok := d.shop.(runtime.PublicReplyAuthorizer)
	if !ok {
		return ErrUpdateTargetDenied
	}
	return guard.AuthorizePublicReply(ctx, bot, message)
}

func privateUpdate(u runtime.Update) bool {
	chatTypeOK := u.ChatType == "private" || (u.Type == "" && u.ChatType == "")
	return u.Private && u.ChatID > 0 && u.UserID > 0 && u.ChatID == u.UserID && chatTypeOK && u.SenderChat == nil
}

func (d *UpdateDispatcher) Handle(ctx context.Context, bot uint, u runtime.Update) ([]runtime.Message, error) {
	if bot == 0 {
		return nil, nil
	}
	if d.modules.Profiles != nil {
		if err := d.modules.Profiles.Sync(ctx, u); err != nil {
			return nil, fmt.Errorf("sync telegram event profile: %w", err)
		}
	}
	typ := u.Type
	if typ == "" {
		// Only the exact old private envelope is backwards compatible.
		if !privateUpdate(u) {
			return nil, nil
		}
		typ = "message"
		if u.CallbackID != "" || u.CallbackData != "" {
			typ = "callback_query"
		}
	}
	if typ == "callback_query" {
		if u.CallbackID == "" || len(u.CallbackData) == 0 || len(u.CallbackData) > 64 {
			return nil, nil
		}
		switch {
		case strings.HasPrefix(u.CallbackData, "s:"):
			if !privateUpdate(u) || d.shop == nil {
				return nil, nil
			}
			return d.shop.Handle(ctx, bot, u)
		case strings.HasPrefix(u.CallbackData, supportcontract.OperationCallbackPrefix):
			return receiveUpdate(ctx, d.modules.SupportOperations, bot, u)
		case strings.HasPrefix(u.CallbackData, "support:"):
			return receiveUpdate(ctx, d.modules.SupportCallbacks, bot, u)
		case strings.HasPrefix(u.CallbackData, "member:"):
			return receiveUpdate(ctx, d.modules.MemberCallbacks, bot, u)
		default:
			return nil, nil
		}
	}
	if typ != "message" || u.Migration != nil {
		event := typ
		if u.Migration != nil && typ == "message" {
			event = "migration"
		}
		return receiveUpdate(ctx, d.modules.Events[event], bot, u)
	}
	if u.TopicService != nil || u.MessageKind == "service" {
		return receiveUpdate(ctx, d.modules.Events["topic_service"], bot, u)
	}
	if !privateUpdate(u) {
		if u.ChatID < 0 && (u.ChatType == "group" || u.ChatType == "supergroup") {
			if supportapp.IsOperationCommand(u) {
				return receiveUpdate(ctx, d.modules.SupportOperations, bot, u)
			}
			return receiveUpdate(ctx, d.modules.GroupMessages, bot, u)
		}
		return nil, nil
	}
	if u.CallbackID != "" || u.CallbackData != "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		return receiveUpdate(ctx, d.shop, bot, u)
	}
	// Missing classification capability fails closed instead of leaking an old code.
	if d.legacy == nil {
		return receiveUpdate(ctx, d.shop, bot, u)
	}
	messages, handled, err := d.legacy.ConsumeLegacyInput(ctx, bot, u)
	if handled || err != nil {
		return messages, err
	}
	if d.modules.Support != nil {
		enabled, err := d.modules.Support.ReadyEnabled(ctx, bot)
		if err != nil {
			return nil, err
		}
		if enabled {
			return d.modules.Support.Handle(ctx, bot, u)
		}
	}
	if fallback, ok := d.shop.(interface {
		HandleSupportUnavailable(context.Context, uint, runtime.Update) ([]runtime.Message, error)
	}); ok {
		return fallback.HandleSupportUnavailable(ctx, bot, u)
	}
	return receiveUpdate(ctx, d.shop, bot, u)
}

// PrepareSupportOperation uses an unmodified trusted update and explicit operation intent.
// Prepared references are not messages and do not grant delivery permission.
func (d *UpdateDispatcher) PrepareSupportOperation(ctx context.Context, bot uint, intent supportcontract.OperationIntent) (string, error) {
	if d.modules.SupportOperations == nil {
		return "", supportcontract.ErrDenied
	}
	return d.modules.SupportOperations.Prepare(ctx, bot, intent)
}

func receiveUpdate(ctx context.Context, receiver UpdateReceiver, bot uint, u runtime.Update) ([]runtime.Message, error) {
	if receiver == nil {
		return nil, nil
	}
	return receiver.Handle(ctx, bot, u)
}

func (d *UpdateDispatcher) AuthorizeDelivery(ctx context.Context, bot uint, tg int64, owner uint) error {
	if d.shop == nil || bot == 0 || tg <= 0 || owner == 0 {
		return ErrUpdateTargetDenied
	}
	return d.shop.AuthorizeDelivery(ctx, bot, tg, owner)
}

// AuthorizeProactiveDelivery forwards only to the installed background grant guard.
func (d *UpdateDispatcher) AuthorizeProactiveDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	guard, ok := d.modules.Events["chat_member"].(runtime.ProactiveDeliveryAuthorizer)
	if !ok {
		return ErrUpdateTargetDenied
	}
	return guard.AuthorizeProactiveDelivery(ctx, bot, message)
}

// AuthorizeJoinAbuseDelivery preserves independent owner-only notification authorization.
func (d *UpdateDispatcher) AuthorizeJoinAbuseDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	guard, ok := d.modules.Events["chat_member"].(runtime.JoinAbuseDeliveryAuthorizer)
	if !ok {
		return ErrUpdateTargetDenied
	}
	return guard.AuthorizeJoinAbuseDelivery(ctx, bot, message)
}

// AuthorizeReminderDelivery uses the installed membership event bridge, not a website permission.
func (d *UpdateDispatcher) AuthorizeReminderDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	guard, ok := d.modules.Events["chat_member"].(runtime.ReminderDeliveryAuthorizer)
	if !ok {
		return ErrUpdateTargetDenied
	}
	return guard.AuthorizeReminderDelivery(ctx, bot, message)
}

// AuthorizeInviteDelivery preserves the extra membership guard through dispatch.
func (d *UpdateDispatcher) AuthorizeInviteDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	guard, ok := d.shop.(runtime.InviteDeliveryAuthorizer)
	if !ok {
		return ErrUpdateTargetDenied
	}
	return guard.AuthorizeInviteDelivery(ctx, bot, message)
}

func (d *UpdateDispatcher) AuthorizeStamp(ctx context.Context, bot uint, tg int64, stamp string) error {
	if d.shop == nil || bot == 0 || tg <= 0 {
		return ErrUpdateTargetDenied
	}
	return d.shop.AuthorizeStamp(ctx, bot, tg, stamp)
}
func (d *UpdateDispatcher) AuthorizeTarget(ctx context.Context, bot uint, m runtime.Message) error {
	typ := m.TargetType.Normalize()
	if bot == 0 || !typ.Valid() {
		return ErrUpdateTargetDenied
	}
	if typ == runtime.TargetShopPrivate {
		if m.ChatID <= 0 || m.ThreadID != 0 {
			return ErrUpdateTargetDenied
		}
		if err := d.AuthorizeDelivery(ctx, bot, m.ChatID, m.OwnerUserID); err != nil {
			return err
		}
		return d.AuthorizeStamp(ctx, bot, m.ChatID, m.IdentityStamp)
	}
	authorizer := d.modules.Targets[typ]
	if authorizer == nil {
		return ErrUpdateTargetDenied
	}
	return authorizer.AuthorizeTarget(ctx, bot, m)
}

func (d *UpdateDispatcher) AllowedUpdates(ctx context.Context, bot uint) ([]string, error) {
	types := []string{"message", "callback_query"}
	seen := map[string]bool{"message": true, "callback_query": true}
	for _, provider := range d.modules.Subscriptions {
		if provider == nil {
			continue
		}
		extra, err := provider.AllowedUpdates(ctx, bot)
		if err != nil {
			return nil, err
		}
		if len(extra) == 0 {
			continue
		}
		if err := runtime.ValidateSubscription(extra); err != nil {
			return nil, err
		}
		for _, typ := range extra {
			if !seen[typ] {
				types = append(types, typ)
				seen[typ] = true
			}
		}
	}
	return types, nil
}

var _ runtime.Handler = (*UpdateDispatcher)(nil)
var _ runtime.TargetAuthorizer = (*UpdateDispatcher)(nil)

// SupportStore collects existing support ports for composition, not a new store.
type SupportStore interface {
	supportcontract.Dispatches
	supportcontract.ReadyConsumer
	supportcontract.Topics
	supportcontract.Downstreams
	supportcontract.NativeReplies
	supportcontract.Operations
	supportcontract.OperationControls
	runtime.TargetAuthorizer
}

// SupportRuntime wires recovery independently of the closed consultation gate.
// Runtime supplies the lease-scoped context; this adapter never creates a fence.
type SupportRuntime struct {
	Ready      *supportapp.ReadyWorker
	Replies    *supportapp.ReplyService
	Operations *supportapp.OperationService
	Topics     *supportapp.TopicWorker
	store      runtime.Store
	mu         sync.Mutex
	cursors    map[uint]supportCursor
}

type supportCursor struct {
	generation          uint64
	ready, reply, cards uint
}

// NewSupportRuntime binds trusted ports without starting workers or enabling ingress.
// outbox is the existing durable runtime queue; no direct send is permitted here.
func NewSupportRuntime(store SupportStore, runtimeStore runtime.Store, outbox supportapp.DeliveryStore, cipher supportcontract.Cipher, members supportcontract.SupportMembers) *SupportRuntime {
	queue := supportQueue{outbox}
	topics := &supportapp.TopicWorker{Topics: store, Routes: members,
		Platform: &supportapi.Topics{Store: runtimeStore, Cipher: cipher, API: runtimeapi.Adapter{}},
		NewClaim: uuid.NewString, Now: time.Now}
	return &SupportRuntime{
		Ready:   &supportapp.ReadyWorker{Store: store, Consumer: topicReadyConsumer{ReadyConsumer: store, topics: topics}, Queue: queue, Builder: supportapp.TransferReadyBuilder{}, NewClaim: uuid.NewString, Topics: topics},
		Replies: &supportapp.ReplyService{Store: store, Replies: store, Members: members, Queue: queue, Now: time.Now},
		Operations: &supportapp.OperationService{Store: store, Members: members, Cipher: cipher, Now: time.Now, Controls: store, Queue: queue,
			BotUsername: func(ctx context.Context, bot uint) (string, error) {
				instance, err := runtimeStore.Get(ctx, bot)
				if err != nil {
					return "", err
				}
				if instance == nil {
					return "", runtime.ErrNotFound
				}
				return instance.Username, nil
			}},
		Topics: topics, store: runtimeStore, cursors: make(map[uint]supportCursor),
	}
}

type supportQueue struct{ supportapp.DeliveryStore }

// EnqueueDelivery preserves the prepared request and runtime deduplication key.
func (queue supportQueue) EnqueueDelivery(ctx context.Context, request runtime.DeliveryRequest) error {
	return queue.EnqueueRequest(ctx, &request)
}

type topicReadyConsumer struct {
	supportcontract.ReadyConsumer
	topics *supportapp.TopicWorker
}

// ClaimReady establishes a trusted topic before claiming customer content.
// A failed or uncertain topic never falls back to an arbitrary chat/thread.
func (consumer topicReadyConsumer) ClaimReady(ctx context.Context, bot, session uint, generation uint64, claim string, now time.Time, ttl time.Duration) (*supportdomain.BufferedMessage, error) {
	if err := consumer.topics.Ensure(ctx, bot, session); err != nil {
		return nil, err
	}
	return consumer.ReadyConsumer.ClaimReady(ctx, bot, session, generation, claim, now, ttl)
}

// Reconcile advances bounded per-generation cursors using only the runtime fence.
// Errors are returned to the supervisor; queue insertion is never a send receipt.
func (support *SupportRuntime) Reconcile(ctx context.Context, bot uint) error {
	instance, err := support.store.Get(ctx, bot)
	if err != nil {
		return err
	}
	if instance == nil {
		return runtime.ErrStale
	}
	if err = runtime.CheckExecutionFence(ctx, bot, instance.Generation); err != nil {
		return err
	}
	support.mu.Lock()
	cursor := support.cursors[bot]
	if cursor.generation != instance.Generation {
		cursor = supportCursor{generation: instance.Generation}
	}
	support.mu.Unlock()
	topicErr := support.Topics.Reconcile(ctx, bot, instance.Generation, time.Now().UTC())
	cards, cardsErr := support.Operations.TickVerificationCards(ctx, bot, instance.Generation, cursor.cards, support.Topics)
	ready, readyErr := support.Ready.Tick(ctx, bot, instance.Generation, cursor.ready, 50, time.Now().UTC())
	reply, replyErr := support.Replies.Tick(ctx, bot, instance.Generation, cursor.reply, 50)
	if err = runtime.CheckExecutionFence(ctx, bot, instance.Generation); err != nil {
		return errors.Join(topicErr, cardsErr, readyErr, replyErr, err)
	}
	support.mu.Lock()
	support.cursors[bot] = supportCursor{generation: instance.Generation, ready: ready, reply: reply, cards: cards}
	support.mu.Unlock()
	return errors.Join(topicErr, cardsErr, readyErr, replyErr)
}

// Handle receives only topic service events and reads generation from trusted storage.
// It never returns outbound messages or routes rejected events to ordinary replies.
func (support *SupportRuntime) Handle(ctx context.Context, bot uint, update runtime.Update) ([]runtime.Message, error) {
	if update.TopicService == nil {
		return nil, nil
	}
	instance, err := support.store.Get(ctx, bot)
	if err != nil {
		return nil, err
	}
	if instance == nil || support.Topics == nil {
		return nil, runtime.ErrStale
	}
	_, err = support.Topics.ReceiveTopic(ctx, bot, instance.Generation, update)
	return nil, err
}

// SupportGroupAuthorizer keeps operational controls separate from customer content.
type SupportGroupAuthorizer struct {
	Ready    runtime.TargetAuthorizer
	Controls runtime.TargetAuthorizer
}

// AuthorizeTarget dispatches exact purposes; delegated services validate the complete envelope.
func (authorizer SupportGroupAuthorizer) AuthorizeTarget(ctx context.Context, bot uint, message runtime.Message) error {
	if message.TargetType != runtime.TargetSupportGroup {
		return ErrUpdateTargetDenied
	}
	if message.Purpose == supportcontract.OperationControlPurpose && authorizer.Controls != nil {
		return authorizer.Controls.AuthorizeTarget(ctx, bot, message)
	}
	if (message.Purpose == "support_ready" || message.Purpose == "support_reply_rejected") && authorizer.Ready != nil {
		return authorizer.Ready.AuthorizeTarget(ctx, bot, message)
	}
	return ErrUpdateTargetDenied
}

// SupportPrivateAuthorizer separates verification notices from staff replies.
// Purpose selects the delegated service; it does not bypass full authorization.
type SupportPrivateAuthorizer struct {
	Notices runtime.TargetAuthorizer
	Replies runtime.TargetAuthorizer
}

func (authorizer SupportPrivateAuthorizer) AuthorizeTarget(ctx context.Context, bot uint, message runtime.Message) error {
	if message.TargetType != runtime.TargetSupportPrivate {
		return ErrUpdateTargetDenied
	}
	switch message.Purpose {
	case "support_gate", "support_gate_cleanup":
		if authorizer.Notices != nil {
			return authorizer.Notices.AuthorizeTarget(ctx, bot, message)
		}
	case "support_reply":
		if authorizer.Replies != nil {
			return authorizer.Replies.AuthorizeTarget(ctx, bot, message)
		}
	}
	return ErrUpdateTargetDenied
}

// EventProfileSync reuses identity resolution to update existing profiles, never provisioning accounts.
type EventProfileSync struct {
	Auth *userapp.Service
}

// Sync accepts a trusted runtime update and returns storage errors for inbox retry.
// Only current native actors, membership subjects and applicants are eligible; historical references are ignored.
func (s EventProfileSync) Sync(ctx context.Context, update runtime.Update) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Auth == nil {
		return errors.New("telegram profile sync: auth service unavailable")
	}
	var users []runtime.UserReference
	switch update.Type {
	case "message", "edited_message", "callback_query", "chat_member", "my_chat_member", "chat_join_request":
		if update.Actor != nil && (update.SenderChat == nil || update.Type == "callback_query") {
			users = append(users, *update.Actor)
		}
	default:
		return nil
	}
	if (update.Type == "chat_member" || update.Type == "my_chat_member") && update.Member != nil {
		users = append(users, update.Member.Subject)
	}
	if update.Type == "chat_join_request" && update.JoinRequest != nil {
		users = append(users, update.JoinRequest.User)
	}
	seen := make(map[int64]bool, len(users))
	for _, user := range users {
		if user.ID <= 0 || user.IsBot || seen[user.ID] {
			continue
		}
		seen[user.ID] = true
		_, _, err := s.Auth.ResolveTelegramChannelIdentity(userapp.TelegramChannelIdentityInput{
			ChannelUserID: strconv.FormatInt(user.ID, 10), Username: user.Username, UsernamePresent: true,
		})
		// Disabled/deleted local accounts must not block membership or moderation events.
		if err != nil && !errors.Is(err, userapp.ErrUserDisabled) && !errors.Is(err, userapp.ErrNotFound) {
			return fmt.Errorf("resolve telegram event identity: %w", err)
		}
	}
	return nil
}

// Resolve looks up active identity; provision explicitly uses the Bot-only policy.
func (a *shopAdapter) Resolve(ctx context.Context, x c.Actor, provision bool) (uint, error) {
	in := userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10), Username: x.Username, UsernamePresent: x.UsernamePresent}
	user, _, err := a.d.Auth.ResolveTelegramChannelIdentity(in)
	if err != nil {
		return 0, shopIdentityError(err)
	}
	if user == nil && provision {
		user, _, _, err = a.d.Auth.ProvisionTelegramBotIdentity(ctx, x.BotID, in)
	}
	if err != nil {
		return 0, shopIdentityError(err)
	}
	if user == nil {
		return 0, nil
	}
	return user.ID, nil
}

// Generation returns the exact current nonzero binding generation.
func (a *shopAdapter) Generation(ctx context.Context, x c.Actor) (string, error) {
	user, identity, err := a.d.Auth.ResolveTelegramChannelIdentity(userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10)})
	if err != nil {
		return "", shopIdentityError(err)
	}
	if user == nil || identity == nil || user.ID != x.UserID {
		return "", c.ErrDenied
	}
	return fmt.Sprintf("%d:%d", identity.ID, user.ID), nil
}

// authorize validates an active owner and optional binding generation.
func (a *shopAdapter) authorize(ctx context.Context, x c.Actor) error {
	if x.BotID == 0 || x.TelegramID <= 0 || x.UserID == 0 {
		return c.ErrDenied
	}
	uid, err := a.Resolve(ctx, x, false)
	if err != nil {
		return err
	}
	if uid == 0 || uid != x.UserID {
		return c.ErrDenied
	}
	if x.Generation != "" {
		generation, err := a.Generation(ctx, x)
		if err != nil {
			return err
		}
		if generation != x.Generation {
			return c.ErrDenied
		}
	}
	return nil
}

// authorizeCatalog permits only genuinely unbound public catalog readers.
func (a *shopAdapter) authorizeCatalog(ctx context.Context, actor c.Actor) error {
	if actor.BotID == 0 || actor.TelegramID <= 0 {
		return c.ErrDenied
	}
	if actor.UserID != 0 {
		return a.authorize(ctx, actor)
	}
	uid, err := a.Resolve(ctx, actor, false)
	if err != nil {
		return err
	}
	if uid != 0 {
		return c.ErrDenied
	}
	return nil
}

// SendBindCode preserves legacy binding authorization without provisioning.
func (a *shopAdapter) SendBindCode(ctx context.Context, x c.Actor, email string) error {
	if err := a.authorize(ctx, x); err != nil {
		return err
	}
	return shopIdentityError(a.d.Auth.SendVerifyCode(ctx, email, constants.VerifyPurposeTelegramBind, x.Locale, ""))
}

// Bind delegates verified legacy binding to the shared identity service.
func (a *shopAdapter) Bind(ctx context.Context, x c.Actor, email, code string) (uint, error) {
	if err := a.authorize(ctx, x); err != nil {
		return 0, err
	}
	user, _, _, err := a.d.Auth.BindTelegramBotByEmailCode(x.BotID, userapp.BindTelegramChannelByEmailCodeInput{Identity: userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10)}, Email: email, Code: code})
	if err != nil {
		return 0, shopIdentityError(err)
	}
	if user == nil {
		return 0, c.ErrDenied
	}
	return user.ID, nil
}

// shopIdentityError maps terminal policy outcomes without hiding dependency errors.
func shopIdentityError(err error) error {
	if errors.Is(err, userapp.ErrUserDisabled) {
		return c.ErrAccountDisabled
	}
	if errors.Is(err, userapp.ErrRegistrationDisabled) {
		return c.ErrRegistrationDisabled
	}
	if errors.Is(err, userapp.ErrInviteCodeRequired) || errors.Is(err, userapp.ErrInviteCodeInvalid) {
		return c.ErrInviteRequired
	}
	for _, target := range []error{userapp.ErrNotFound, userapp.ErrInvalidEmail, userapp.ErrVerifyCodeInvalid, userapp.ErrVerifyCodeExpired, userapp.ErrVerifyCodeTooFrequent, userapp.ErrVerifyCodeAttemptsExceeded, userapp.ErrUserOAuthIdentityExists, userapp.ErrUserOAuthAlreadyBound} {
		if errors.Is(err, target) {
			return c.ErrDenied
		}
	}
	return err
}

// HistoryUsers grants only persisted historical reads to active owners.
func (a *shopAdapter) HistoryUsers(ctx context.Context, x c.Actor) ([]uint, error) {
	if err := a.authorize(ctx, x); err != nil {
		return nil, err
	}
	return a.d.Auth.AllowedTelegramHistoryUserIDs(ctx, x.BotID, strconv.FormatInt(x.TelegramID, 10), x.UserID)
}

// readOwner rejects zero owners and validates historical read grants.
func (a *shopAdapter) readOwner(ctx context.Context, x c.Actor, owner uint) error {
	if owner == 0 {
		return c.ErrDenied
	}
	if err := a.authorize(ctx, x); err != nil {
		return err
	}
	if x.UserID == owner {
		return nil
	}
	allowed, err := a.d.Auth.CanReadTelegramHistory(ctx, x.BotID, strconv.FormatInt(x.TelegramID, 10), x.UserID, owner)
	if err != nil {
		return err
	}
	if !allowed {
		return c.ErrDenied
	}
	return nil
}

type businessRenderer struct {
	shop  *shopAdapter
	guard runtime.Handler
	store runtime.Store
}

// membershipProduct projects shared configuration onto already-public SKUs.
// ctx propagates cancellation; view retains ordinary pricing/stock without configuration.
func (a *shopAdapter) membershipProduct(ctx context.Context, view *c.Product) error {
	if a.d.Membership == nil {
		// Legacy assembly may display only products the shared policy proves ordinary.
		if err := product.RequireOrdinaryWrite(a.d.ProductRepo, []uint{view.ID}); err != nil {
			return c.ErrUnavailable
		}
		return nil
	}
	config, err := a.d.Membership.Display(ctx, view.ID)
	if errors.Is(err, membership.ErrNotFound) {
		return nil
	}
	if err != nil {
		return c.ErrUnavailable
	}
	if config == nil || config.ProductID != view.ID || config.Quantity != 1 || len(config.GroupNames) == 0 {
		return c.ErrUnavailable
	}
	view.Membership, view.Available = true, view.Available && config.SalesOpen
	view.GroupNames = append([]string(nil), config.GroupNames...)
	view.Min, view.Max = 1, 1
	terms := make(map[uint]*c.MembershipTerm, len(config.Terms))
	for _, term := range config.Terms {
		if term.ProductID != view.ID || term.SKUID == 0 || terms[term.SKUID] != nil {
			return c.ErrUnavailable
		}
		// Match shared duration representability without importing module internals.
		maxMonths := int64(math.MaxInt64) / int64(time.Second) / (30 * 24 * 60 * 60)
		if (term.Kind != "months" && term.Kind != "permanent") ||
			(term.Kind == "months" && (term.Months <= 0 || term.Months > maxMonths)) ||
			(term.Kind == "permanent" && term.Months != 0) {
			return c.ErrUnavailable
		}
		terms[term.SKUID] = &c.MembershipTerm{Kind: term.Kind, Months: term.Months}
	}
	if len(terms) == 0 {
		return c.ErrUnavailable
	}
	for index := range view.SKUs {
		sku := &view.SKUs[index]
		sku.MembershipTerm = terms[sku.ID]
		sku.Available = sku.Available && config.SalesOpen && sku.MembershipTerm != nil
	}
	return nil
}

// checkShopPublicSale guards preview and both create writers; productID is the actual selected product.
// Missing shared policy or a rejected product returns a non-sensitive unavailable error.
func (a *shopAdapter) checkShopPublicSale(productID uint) error {
	if productID == 0 {
		return c.ErrDenied
	}
	if err := product.RequirePublicSale(a.d.ProductRepo, []uint{productID}); err != nil {
		return c.ErrUnavailable
	}
	return nil
}

func (r businessRenderer) Render(ctx context.Context, event events.Event) (runtime.Message, error) {
	if err := r.guard.AuthorizeDelivery(ctx, event.BotID, event.TelegramID, event.UserID); err != nil {
		return runtime.Message{}, err
	}
	message := runtime.Message{ChatID: event.PrivateChatID, OwnerUserID: event.UserID, Purpose: "notification"}
	actor := shopcontract.Actor{BotID: event.BotID, TelegramID: event.TelegramID}
	current, err := r.shop.Resolve(ctx, actor, false)
	if err != nil {
		return message, err
	}
	actor.UserID = current
	message.IdentityStamp, err = r.shop.Generation(ctx, actor)
	if err != nil {
		return message, err
	}
	locale := "zh-CN"
	if contact, err := r.store.Contact(ctx, event.BotID, event.TelegramID); err == nil && contact != nil && contact.Locale != "" {
		locale = contact.Locale
	}
	selectText := func(cn, tw, en string) string {
		switch locale {
		case "en", "en-US":
			return en
		case "zh-TW", "zh-Hant":
			return tw
		default:
			return cn
		}
	}
	switch event.Kind {
	case events.EventOrderPaid:
		order, err := r.shop.d.Orders.GetOrderByUser(event.OrderID, event.UserID)
		if err != nil {
			return message, err
		}
		membershipCount, ordinaryCount, err := r.paidMembershipCounts(ctx, order)
		if err != nil {
			return message, err
		}
		message.Text = fmt.Sprintf(selectText("付款成功，感谢支持！\n订单 %s\n正在为你发货，进度可在「我的订单」查看。", "付款成功，感謝支持！\n訂單 %s\n正在為你出貨，進度可在「我的訂單」查看。", "Payment received, thank you!\nOrder %s\nWe are preparing your delivery. Check My orders for progress."), order.OrderNo)
		if membershipCount > 0 {
			message.Text = fmt.Sprintf(selectText("付款成功。\n订单 %s\n会员权益正在确认，入群入口准备好后会单独通知；已在群内无需重新加入。", "付款成功。\n訂單 %s\n會員權益正在確認，入群入口準備好後會另行通知；已在群內無需重新加入。", "Payment received.\nOrder %s\nMembership is being confirmed. Your invitation will arrive separately; existing members do not need to rejoin."), order.OrderNo)
			if ordinaryCount > 0 {
				message.Text += selectText("\n其他商品将按订单进度发货。", "\n其他商品將按訂單進度出貨。", "\nOther items will be fulfilled according to order progress.")
			}
		}
	case events.EventOrderFulfilled:
		order, err := r.shop.d.Orders.GetOrderByUser(event.OrderID, event.UserID)
		if err != nil {
			return message, err
		}
		view := orderView(order)
		if len(view.Delivered) == 0 {
			return message, fmt.Errorf("fulfillment is not ready")
		}
		message.Purpose = "delivery"
		message.Text = fmt.Sprintf(selectText("你的商品已送达，请妥善保存。\n订单 %s\n%s", "你的商品已送達，請妥善保存。\n訂單 %s\n%s", "Your items have arrived. Keep them safe.\nOrder %s\n%s"), order.OrderNo, strings.Join(view.Delivered, "\n\n"))
		message.ForceDocument = len(utf16.Encode([]rune(message.Text))) > 3500
	case events.EventRechargeSucceeded:
		message.Text = selectText("账户充值已到账，请查看账户余额。", "帳戶儲值已到帳，請查看帳戶餘額。", "Your recharge is credited. Check your account balance.")
	default:
		return message, fmt.Errorf("unsupported business event")
	}
	return message, nil
}

// AfterEnqueue updates only a panel still displaying this order. Notification
// delivery has its own durable key and is never replaced by this refresh.
func (r businessRenderer) AfterEnqueue(ctx context.Context, event events.Event) error {
	if event.Kind != events.EventOrderPaid && event.Kind != events.EventOrderFulfilled {
		return nil
	}
	refresher, ok := r.guard.(interface {
		RefreshOrderPanel(context.Context, uint, int64, uint) ([]runtime.Message, error)
	})
	if !ok {
		return nil
	}
	messages, err := refresher.RefreshOrderPanel(ctx, event.BotID, event.TelegramID, event.OrderID)
	// A missing/expired session or revoked identity must not keep a durable
	// notification event retrying forever. Navigation conflicts can retry safely.
	if errors.Is(err, runtime.ErrNotFound) || errors.Is(err, shopcontract.ErrDenied) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.Purpose != "panel" {
			continue
		}
		message.ChatID = event.PrivateChatID
		// An event has no Telegram callback source, so it cannot adopt an
		// unknown physical message from the session's remembered callback ID.
		message.TargetMessageID = 0
		message.TargetKind = ""
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		cipher, err := crypto.Encrypt(crypto.DeriveKey(r.shop.d.Secret), string(raw))
		if err != nil {
			return err
		}
		// A replay may render a newer revision; do not dedupe it against an older,
		// already superseded payload. The store coalesces obsolete panel renders.
		key := fmt.Sprintf("business-panel:%d:%s:%d", event.ID, message.PanelEpoch, message.PanelRevision)
		if err = r.store.Enqueue(ctx, &rd.Delivery{BotID: event.BotID, Key: key, ChatID: event.PrivateChatID, TelegramUserID: event.TelegramID, UserID: message.OwnerUserID, PayloadCipher: cipher, Purpose: "panel", Operation: message.Operation, PanelEpoch: message.PanelEpoch, PanelRevision: message.PanelRevision, TargetMessageID: message.TargetMessageID, Priority: 100, Status: "pending", AvailableAt: time.Now()}); err != nil {
			return err
		}
	}
	return nil
}

// paidMembershipCounts classifies immutable paid items, never mutable product configuration.
// Duplicate parent/child item IDs count once; storage errors stop misleading notifications.
func (renderer businessRenderer) paidMembershipCounts(ctx context.Context, order *o.Order) (int, int, error) {
	reader, ok := renderer.shop.d.Membership.(membership.SnapshotReader)
	if !ok {
		return 0, 0, nil
	}
	membershipCount, ordinaryCount := 0, 0
	seen := map[uint]bool{}
	orders := []*o.Order{order}
	for len(orders) > 0 {
		current := orders[0]
		orders = orders[1:]
		for index := range current.Children {
			orders = append(orders, &current.Children[index])
		}
		for _, item := range current.Items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			snapshot, err := reader.Snapshot(ctx, item.ID)
			if errors.Is(err, membership.ErrNotFound) {
				ordinaryCount++
				continue
			}
			if err != nil {
				return 0, 0, err
			}
			if snapshot == nil {
				ordinaryCount++
				continue
			}
			if snapshot.AccountID != order.UserID || snapshot.OrderItemID != item.ID {
				return 0, 0, membership.ErrUntrustedIdentity
			}
			membershipCount++
		}
	}
	return membershipCount, ordinaryCount, nil
}

type messageEnqueuer struct{ store runtime.Store }

func (q messageEnqueuer) EnqueueMessage(ctx context.Context, botID uint, key string, telegramID int64, userID uint, cipher string) error {
	return q.store.Enqueue(ctx, &rd.Delivery{BotID: botID, Key: key, ChatID: telegramID, TelegramUserID: telegramID, UserID: userID, PayloadCipher: cipher, Priority: 200, Status: "pending", AvailableAt: time.Now()})
}
func NewBusinessDispatcher(store events.DispatcherStore, d ShopDependencies, guard runtime.Handler, outbox runtime.Store) *deliveryapp.Dispatcher {
	return &deliveryapp.Dispatcher{Store: store, Renderer: businessRenderer{shop: &shopAdapter{d: d}, guard: guard, store: outbox}, Outbox: messageEnqueuer{store: outbox}, Secret: d.Secret}
}
