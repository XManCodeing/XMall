package telegrammembershipaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess/proactive"
	"github.com/XManCodeing/XMall/internal/constants"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"log/slog"
	"sync"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// JoinAbuseNotifications reuses confirmed refusal evidence and the encrypted outbox.
// Cursors bound scans only; restarts reuse event-time keys and never resend unknowns.
type JoinAbuseNotifications struct {
	Store   membership.JoinAbuseRepository
	Queue   runtime.Store
	Routes  membership.AccessRoutes
	Cipher  membership.AccessCipher
	Now     func() time.Time
	mu      sync.Mutex
	cursors map[uint]uint
}

// now returns the injected trusted UTC observation time.
func (service *JoinAbuseNotifications) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

// joinAbuseKey aggregates owner/invite attempts in a fixed hourly event-time window.
func joinAbuseKey(row membership.JoinAbuse) string {
	return fmt.Sprintf("membership-join-abuse:%d:%d:%d:%d", row.BotID, row.AccountID, row.InviteID, row.Window.UTC().Truncate(time.Hour).Unix())
}

// Reconcile queues bounded evidence; existing receipts always suppress insertion.
func (service *JoinAbuseNotifications) Reconcile(ctx context.Context, bot uint) error {
	if service.Store == nil || service.Queue == nil || service.Routes == nil || service.Cipher == nil {
		return membership.ErrCapability
	}
	service.mu.Lock()
	after := service.cursors[bot]
	service.mu.Unlock()
	rows, err := service.Store.JoinAbuses(ctx, bot, after, 25, service.now())
	if err != nil {
		return err
	}
	next := uint(0)
	for _, row := range rows {
		if row.ID <= after || row.BotID != bot {
			return membership.ErrConflict
		}
		if row.InviteID != 0 {
			if err := service.enqueue(ctx, row); err != nil {
				return err
			}
		}
		next = row.ID
	}
	service.mu.Lock()
	if service.cursors == nil {
		service.cursors = make(map[uint]uint)
	}
	service.cursors[bot] = next
	service.mu.Unlock()
	return nil
}

// enqueue seals fixed text for the original reachable owner; panel fields stay empty.
func (service *JoinAbuseNotifications) enqueue(ctx context.Context, row membership.JoinAbuse) error {
	reader, ok := service.Queue.(runtime.BusinessDeliveryReader)
	if !ok {
		return membership.ErrCapability
	}
	key := joinAbuseKey(row)
	receipt, err := reader.BusinessDelivery(ctx, row.BotID, key)
	if err != nil {
		return err
	}
	if receipt.State != "not_requested" {
		return nil
	}
	contact, err := service.Queue.Contact(ctx, row.BotID, row.TelegramID)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.UserID != row.AccountID || contact.ChatID != row.TelegramID || contact.TelegramUserID != row.TelegramID {
		return nil
	}
	message := runtime.Message{JoinAbuseDelivery: &runtime.JoinAbuseDeliveryRef{ID: row.ID, Generation: row.Generation}, TargetType: runtime.TargetShopPrivate, BusinessRef: key, Purpose: "membership_join_abuse", Operation: "send", OwnerUserID: row.AccountID, ChatID: row.TelegramID, Text: joinAbuseText(contact.Locale)}
	if err := service.AuthorizeJoinAbuseDelivery(ctx, row.BotID, message); err != nil {
		var invalid runtime.DeliveryInvalidated
		if errors.As(err, &invalid) {
			return nil
		}
		return err
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	cipher, err := service.Cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	return service.Queue.Enqueue(ctx, &rd.Delivery{BotID: row.BotID, Key: key, ChatID: row.TelegramID, TelegramUserID: row.TelegramID, UserID: row.AccountID, PayloadCipher: cipher, TargetType: string(runtime.TargetShopPrivate), Purpose: message.Purpose, Operation: "send", Priority: 20, Status: "pending", AvailableAt: service.now()})
}

// AuthorizeJoinAbuseDelivery checks durable refusal, original binding/contact and
// exact current route/generation at runtime's enqueue and pre-HTTP boundaries.
func (service *JoinAbuseNotifications) AuthorizeJoinAbuseDelivery(ctx context.Context, bot uint, message runtime.Message) (result error) {
	defer func() {
		if result == nil {
			return
		}
		var invalid runtime.DeliveryInvalidated
		if errors.As(result, &invalid) {
			return
		}
		if errors.Is(result, membership.ErrConflict) || errors.Is(result, membership.ErrUntrustedIdentity) || errors.Is(result, membership.ErrNotFound) || errors.Is(result, membership.ErrInvalid) {
			result = runtime.DeliveryInvalidated{State: "superseded"}
			return
		}
		result = runtime.DeliveryDeferred{AvailableAt: time.Now().UTC().Add(time.Minute), Transient: true}
	}()
	if message.JoinAbuseDelivery == nil || message.JoinAbuseDelivery.ID == 0 || message.Purpose != "membership_join_abuse" || message.Operation != "send" || message.TargetType != runtime.TargetShopPrivate || message.ChatID <= 0 || message.OwnerUserID == 0 || message.PanelEpoch != "" || message.PanelRevision != 0 || message.TargetMessageID != 0 || message.ReplacePanel || message.InviteDelivery != nil || message.ReminderDelivery != nil || message.ProactiveDelivery != nil || message.PublicReply != nil || message.Transfer != nil || message.PanelCleanup != nil || len(message.Buttons) != 0 || len(message.Photo) != 0 || message.PhotoURL != "" || message.FileID != "" || message.ParseMode != "" {
		return membership.ErrInvalid
	}
	row, err := service.Store.JoinAbuse(ctx, message.JoinAbuseDelivery.ID, service.now())
	if err != nil {
		return err
	}
	if row == nil || row.ID != message.JoinAbuseDelivery.ID || row.InviteID == 0 || row.BindingVersion == 0 || row.Window.IsZero() || row.BotID != bot || row.AccountID != message.OwnerUserID || row.TelegramID != message.ChatID || row.Generation != message.JoinAbuseDelivery.Generation || joinAbuseKey(*row) != message.BusinessRef {
		return membership.ErrUntrustedIdentity
	}
	route, err := service.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if route.BotID != bot || route.TargetID != row.TargetID || route.ChatID != row.ChatID || route.RouteVersion != row.RouteVersion || route.Generation != row.Generation {
		return membership.ErrConflict
	}
	contact, err := service.Queue.Contact(ctx, bot, row.TelegramID)
	if errors.Is(err, runtime.ErrNotFound) {
		return runtime.DeliveryInvalidated{State: "unreachable"}
	}
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.UserID != row.AccountID || contact.ChatID != row.TelegramID || contact.TelegramUserID != row.TelegramID {
		return runtime.DeliveryInvalidated{State: "unreachable"}
	}
	if message.Text != joinAbuseText(contact.Locale) {
		return membership.ErrConflict
	}
	latest, err := service.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return membership.ErrConflict
	}
	return runtime.CheckExecutionFence(ctx, bot, route.Generation)
}

// joinAbuseText contains no applicant identity or invitation secret.
func joinAbuseText(locale string) string {
	switch locale {
	case "en", "en-US":
		return "Someone tried to use your exclusive invitation link and was denied. Please keep your link private."
	case "zh-TW", "zh-HK":
		return "有人嘗試使用你的專屬邀請連結，已拒絕。請勿外傳，保護你的連結。"
	default:
		return "有人尝试使用你的专属邀请链接，已拒绝。请勿外传，保护你的链接。"
	}
}

// IdentityMigrations performs read-only admission verification, never automated identity verification.
// Cursor is supplied by the caller for fair bounded pagination; all facts remain durable in membership.
type IdentityMigrations struct {
	cursorMu sync.Mutex
	cursors  map[uint]uint
	Store    membership.IdentityMigrationObserver
	Routes   membership.AccessRoutes
	Platform interface {
		GetMembershipMember(context.Context, membership.AccessJoinAction) membership.AccessMemberResult
	}
	Now func() time.Time
}

// Reconcile rotates a per-bot cursor; a restart safely repeats read-only observations.
// The mutex covers only cursor access, never HTTP or database transactions.
func (bridge *IdentityMigrations) Reconcile(ctx context.Context, bot uint) error {
	bridge.cursorMu.Lock()
	after := bridge.cursors[bot]
	bridge.cursorMu.Unlock()
	next, err := bridge.Scan(ctx, bot, after, 25)
	bridge.cursorMu.Lock()
	if bridge.cursors == nil {
		bridge.cursors = make(map[uint]uint)
	}
	bridge.cursors[bot] = next
	bridge.cursorMu.Unlock()
	return err
}

// Scan queries a bounded cursor page of reviewed targets under the runtime fence.
// Returns zero after an empty page; unknown/network outcomes never trigger old cleanup.
func (bridge *IdentityMigrations) Scan(ctx context.Context, bot, after uint, limit int) (uint, error) {
	if bridge.Store == nil || bridge.Routes == nil || bridge.Platform == nil {
		return after, membership.ErrCapability
	}
	rows, err := bridge.Store.PendingIdentityMigrations(ctx, bot, after, limit)
	if err != nil {
		return after, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var failures []error
	for _, row := range rows {
		if err := bridge.observe(ctx, row, row.NewTelegramID); err != nil {
			failures = append(failures, err)
		}
		if err := bridge.observe(ctx, row, row.OldTelegramID); err != nil {
			failures = append(failures, err)
		}
		after = row.ID
	}
	return after, errors.Join(failures...)
}

// observe obtains actual membership with fresh route/fence checks before and after HTTP.
// Only verified evidence enters a transaction; no network call runs on a database connection.
func (bridge *IdentityMigrations) observe(ctx context.Context, row membership.IdentityMigrationWork, identity int64) error {
	route, err := bridge.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if route.BotID != row.BotID || route.ChatID != row.ChatID || route.RouteVersion != row.RouteVersion {
		return membership.ErrConflict
	}
	if err := runtime.CheckExecutionFence(ctx, row.BotID, route.Generation); err != nil {
		return err
	}
	observedAt := time.Now().UTC()
	if bridge.Now != nil {
		observedAt = bridge.Now().UTC()
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	result := bridge.Platform.GetMembershipMember(callCtx, membership.AccessJoinAction{Route: route, TelegramID: identity})
	cancel()
	if result.Outcome != membership.AccessSucceeded {
		return membership.ErrCapability
	}
	latest, err := bridge.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return membership.ErrConflict
	}
	if err := runtime.CheckExecutionFence(ctx, row.BotID, route.Generation); err != nil {
		return err
	}
	_, err = bridge.Store.ObserveAccess(ctx, membership.AccessObservation{TargetID: row.TargetID, BotID: row.BotID, ChatID: row.ChatID, RouteVersion: row.RouteVersion, TelegramID: identity, Status: result.Status, IsMember: result.IsMember, ObservedAt: observedAt, Source: "query"})
	return err
}

// Reminders connects bounded membership scans to the existing encrypted runtime queue.
// Now is injectable; this bridge never creates contacts or makes Telegram calls.
type Reminders struct {
	Store  membership.ReminderRepository
	Queue  runtime.Store
	Routes membership.AccessRoutes
	Cipher membership.AccessCipher
	Now    func() time.Time
}

// now returns the trusted UTC observation time without changing paid timestamps.
func (service *Reminders) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

// Reconcile scans and consumes bounded fair batches inside the leased runtime loop.
func (service *Reminders) Reconcile(ctx context.Context, bot uint) error {
	if _, err := service.Store.ScanReminders(ctx, bot, 100, service.now()); err != nil {
		return err
	}
	rows, err := service.Store.DueReminders(ctx, bot, 25, service.now())
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if err := service.process(ctx, row); err != nil {
			// Rotate failed dependency work too, so one broken route cannot starve later IDs.
			postponeErr := service.Store.FinishReminder(ctx, row.ID, row.State, 0, service.now())
			slog.WarnContext(ctx, "membership_reminder_reconcile_failed", "reminder_id", row.ID)
			failures = append(failures, errors.Join(err, postponeErr))
		}
	}
	return errors.Join(failures...)
}

// process retains actual receipt states and retries only idempotent queue insertion/read operations.
func (service *Reminders) process(ctx context.Context, row d.Reminder) error {
	reader, ok := service.Queue.(runtime.BusinessDeliveryReader)
	if !ok {
		return membership.ErrCapability
	}
	receipt, err := reader.BusinessDelivery(ctx, row.BotID, row.BusinessKey)
	if err != nil {
		return err
	}
	switch receipt.State {
	case "sent", "failed", "unknown", "superseded", "unreachable":
		return service.Store.FinishReminder(ctx, row.ID, receipt.State, receipt.MessageID, service.now())
	case "not_requested":
	default:
		return service.Store.FinishReminder(ctx, row.ID, "queued", 0, service.now())
	}
	if err := service.validate(ctx, row); err != nil {
		if errors.Is(err, membership.ErrConflict) || errors.Is(err, membership.ErrUntrustedIdentity) {
			return service.Store.FinishReminder(ctx, row.ID, "superseded", 0, service.now())
		}
		if finishErr := service.Store.FinishReminder(ctx, row.ID, "pending", 0, service.now()); finishErr != nil {
			return errors.Join(err, finishErr)
		}
		return err
	}
	contact, err := service.Queue.Contact(ctx, row.BotID, row.TelegramID)
	if err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	if contact == nil || !contact.Reachable || contact.ChatID != row.TelegramID || contact.UserID != row.AccountID {
		return service.Store.FinishReminder(ctx, row.ID, "unreachable", 0, service.now())
	}
	message := runtime.Message{ReminderDelivery: &runtime.ReminderDeliveryRef{ID: row.ID}, TargetType: runtime.TargetShopPrivate, BusinessRef: row.BusinessKey, Purpose: "membership_reminder", Operation: "send", OwnerUserID: row.AccountID, ChatID: row.TelegramID, Text: reminderText(contact.Locale, row)}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	cipher, err := service.Cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	if err := service.Queue.Enqueue(ctx, &rd.Delivery{BotID: row.BotID, Key: row.BusinessKey, ChatID: row.TelegramID, TelegramUserID: row.TelegramID, UserID: row.AccountID, PayloadCipher: cipher, TargetType: string(runtime.TargetShopPrivate), Purpose: message.Purpose, Operation: "send", Priority: 20, Status: "pending", AvailableAt: d.ReminderAvailable(service.now())}); err != nil {
		return err
	}
	return service.Store.FinishReminder(ctx, row.ID, "queued", 0, service.now())
}

// validate recomputes every source and checks cycle, binding, physical route and runtime lease.
func (service *Reminders) validate(ctx context.Context, row d.Reminder) error {
	now := service.now()
	if !row.ExpiresAt.After(now) {
		return membership.ErrConflict
	}
	route, err := service.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if route.BotID != row.BotID || route.ChatID != row.ChatID || route.RouteVersion != row.RouteVersion {
		return membership.ErrConflict
	}
	if err := runtime.CheckExecutionFence(ctx, row.BotID, route.Generation); err != nil {
		return err
	}
	eligible, err := service.Store.Eligibility(ctx, membership.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}, row.TargetID, now)
	if err != nil {
		return err
	}
	if !eligible.Active || eligible.Kind == d.TermPermanent || eligible.ExpiresAt == nil || !eligible.ExpiresAt.Equal(row.ExpiresAt) || eligible.Version != row.EntitlementVersion || eligible.BindingVersion != row.BindingVersion {
		return membership.ErrConflict
	}
	latest, err := service.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if latest != route {
		return membership.ErrConflict
	}
	return runtime.CheckExecutionFence(ctx, row.BotID, route.Generation)
}

// AuthorizeReminderDelivery is invoked by runtime twice before HTTP, including after waiting.
func (service *Reminders) AuthorizeReminderDelivery(ctx context.Context, bot uint, message runtime.Message) (result error) {
	// Only this reminder guard converts pre-send dependency failures to durable retries.
	// No API call has happened: defer one minute using the queue's wall clock, not
	// an injected business observation clock. Unknown platform outcomes are untouched.
	defer func() {
		if result == nil {
			return
		}
		var deferred runtime.DeliveryDeferred
		var invalidated runtime.DeliveryInvalidated
		if errors.As(result, &deferred) || errors.As(result, &invalidated) || errors.Is(result, membership.ErrInvalid) {
			return
		}
		if errors.Is(result, membership.ErrConflict) || errors.Is(result, membership.ErrUntrustedIdentity) {
			result = runtime.DeliveryInvalidated{State: "superseded"}
			return
		}
		if errors.Is(result, membership.ErrNotFound) || errors.Is(result, groups.ErrNotFound) {
			result = runtime.DeliveryInvalidated{State: "superseded"}
			return
		}
		result = runtime.DeliveryDeferred{AvailableAt: time.Now().UTC().Add(time.Minute), Transient: true}
	}()
	if message.ReminderDelivery == nil || message.Purpose != "membership_reminder" || message.Operation != "send" {
		return membership.ErrInvalid
	}
	row, err := service.Store.Reminder(ctx, message.ReminderDelivery.ID)
	if err != nil {
		return err
	}
	if row.BotID != bot || row.AccountID != message.OwnerUserID || row.TelegramID != message.ChatID || row.BusinessKey != message.BusinessRef || (row.State != "pending" && row.State != "queued") {
		return membership.ErrUntrustedIdentity
	}
	if err := service.validate(ctx, *row); err != nil {
		if errors.Is(err, membership.ErrConflict) || errors.Is(err, membership.ErrUntrustedIdentity) {
			return runtime.DeliveryInvalidated{State: "superseded"}
		}
		return err
	}
	now := service.now()
	if available := d.ReminderAvailable(now); available.After(now) {
		return runtime.DeliveryDeferred{AvailableAt: available}
	}
	contact, err := service.Queue.Contact(ctx, bot, row.TelegramID)
	if errors.Is(err, runtime.ErrNotFound) {
		return runtime.DeliveryInvalidated{State: "unreachable"}
	}
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.ChatID != row.TelegramID || contact.UserID != row.AccountID {
		return runtime.DeliveryInvalidated{State: "unreachable"}
	}
	return nil
}

// reminderText renders safe per-target text with stable existing membership/shopping commands.
func reminderText(locale string, row d.Reminder) string {
	expiry := constants.DisplayTime(row.ExpiresAt, "2006-01-02 15:04")
	switch locale {
	case "en-US", "en":
		return fmt.Sprintf("Membership #%d expires at %s (Beijing time). %d-day reminder. View /memberships · Shop /start", row.TargetID, expiry, row.Days)
	case "zh-TW", "zh-HK":
		return fmt.Sprintf("會員 #%d 將於 %s（北京時間）到期。到期前 %d 天提醒。查看 /memberships · 選購 /start", row.TargetID, expiry, row.Days)
	default:
		return fmt.Sprintf("会员 #%d 将于 %s（北京时间）到期。到期前 %d 天提醒。查看 /memberships · 选购 /start", row.TargetID, expiry, row.Days)
	}
}

// Routes translates the group module's fresh authorization snapshot into the membership contract.
// Lookup must remain side-effect-free; the platform adapter separately verifies the runtime execution fence.
type Routes struct {
	Lookup interface {
		MembershipRoute(context.Context, uint) (groups.SupportRoute, error)
	}
}

// MembershipRoute preserves every physical routing and generation field without introducing a second route registry.
func (routes Routes) MembershipRoute(ctx context.Context, target uint) (membership.AccessRoute, error) {
	if routes.Lookup == nil {
		return membership.AccessRoute{}, membership.ErrCapability
	}
	route, err := routes.Lookup.MembershipRoute(ctx, target)
	return membership.AccessRoute(route), err
}

// Events is the narrow runtime input/reconciliation port; web requests cannot manufacture these events.
type Events interface {
	ReceiveMembershipJoin(context.Context, membership.AccessJoinRequest) error
	ObserveMembership(context.Context, membership.AccessObservation) (bool, error)
	RunAccess(context.Context, uint, int) (int, error)
	RunAccessReconcile(context.Context, uint, int) (int, error)
}

// LifecycleWork is the bounded expiry consumer; it never starts detached workers.
type LifecycleWork interface {
	Scan(context.Context, uint, int) (int, error)
	Run(context.Context, uint, int) (int, error)
}

// Runtime binds trustworthy inbox updates to registered membership routes, independently of private contacts.
type Runtime struct {
	Panels interface {
		ReconcileMembershipPanels(context.Context, uint) error
	}
	JoinAbuse  *JoinAbuseNotifications
	Proactive  *proactive.Service
	Migrations *IdentityMigrations
	Reminders  *Reminders
	Service    Events
	Lifecycle  LifecycleWork
	Bots       runtime.Store
	Groups     groups.Store
	Routes     membership.AccessRoutes
	Recovery   RecoveryAccess
}

// AllowedUpdates explicitly subscribes to member and application events even when new sales are stopped.
func (bridge *Runtime) AllowedUpdates(context.Context, uint) ([]string, error) {
	return []string{"chat_join_request", "chat_member"}, nil
}

// Reconcile executes a bounded batch inside the runtime-provided lease context; a website request cannot supply it.
func (bridge *Runtime) Reconcile(ctx context.Context, bot uint) error {
	if bridge.Service == nil {
		return membership.ErrCapability
	}
	_, accessErr := bridge.Service.RunAccess(ctx, bot, 25)
	_, reconcileErr := bridge.Service.RunAccessReconcile(ctx, bot, 25)
	if bridge.Lifecycle == nil {
		return errors.Join(accessErr, reconcileErr, membership.ErrCapability)
	}
	var migrationErr error
	if bridge.Migrations != nil {
		migrationErr = bridge.Migrations.Reconcile(ctx, bot)
	}
	_, scanErr := bridge.Lifecycle.Scan(ctx, bot, 100)
	_, lifecycleErr := bridge.Lifecycle.Run(ctx, bot, 25)
	var reminderErr error
	if bridge.Reminders != nil {
		reminderErr = bridge.Reminders.Reconcile(ctx, bot)
	}
	var abuseErr error
	if bridge.JoinAbuse != nil {
		abuseErr = bridge.JoinAbuse.Reconcile(ctx, bot)
	}
	var panelErr error
	if bridge.Panels != nil {
		panelErr = bridge.Panels.ReconcileMembershipPanels(ctx, bot)
	}
	return errors.Join(accessErr, reconcileErr, scanErr, lifecycleErr, reminderErr, migrationErr, abuseErr, panelErr)
}

// ReconcileProactive executes local bounded work independently from network batches.
func (bridge *Runtime) ReconcileProactive(ctx context.Context, bot uint) error {
	if bridge.Proactive == nil {
		return membership.ErrCapability
	}
	return bridge.Proactive.Reconcile(ctx, bot)
}

// AuthorizeProactiveDelivery preserves the dedicated background grant authorization boundary.
func (bridge *Runtime) AuthorizeProactiveDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	if bridge.Proactive == nil {
		return membership.ErrCapability
	}
	return bridge.Proactive.AuthorizeProactiveDelivery(ctx, bot, message)
}

// AuthorizeJoinAbuseDelivery delegates to the independently scoped notification guard.
func (bridge *Runtime) AuthorizeJoinAbuseDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	if bridge.JoinAbuse == nil {
		return membership.ErrCapability
	}
	return bridge.JoinAbuse.AuthorizeJoinAbuseDelivery(ctx, bot, message)
}

// AuthorizeReminderDelivery delegates only to the runtime-owned reminder bridge.
func (bridge *Runtime) AuthorizeReminderDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	if bridge.Reminders == nil {
		return membership.ErrCapability
	}
	return bridge.Reminders.AuthorizeReminderDelivery(ctx, bot, message)
}

// Handle consumes only native updates from a known physical membership route, never parsed text or sender usernames.
// Invalid/unregistered targets do not create users, contacts, invitations, or managed member records.
func (bridge *Runtime) Handle(ctx context.Context, bot uint, update runtime.Update) ([]runtime.Message, error) {
	if bridge.Service == nil || bridge.Groups == nil || bridge.Routes == nil {
		return nil, membership.ErrCapability
	}
	if bot == 0 || update.ID <= 0 || update.ChatID >= 0 || update.ChatType != "supergroup" || (update.Type != "chat_join_request" && update.Type != "chat_member") {
		return nil, nil
	}
	targets, total, err := bridge.Groups.List(ctx, groups.Filter{BotID: bot, Purpose: "membership", ChatID: update.ChatID, Page: 1, PageSize: 2})
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	if total != 1 || len(targets) != 1 {
		return nil, membership.ErrConflict
	}
	target := targets[0]
	if target.RecoveryTaskID != 0 {
		if bridge.Recovery == nil {
			return nil, membership.ErrCapability
		}
		return nil, bridge.Recovery.Receive(ctx, bot, target.ID, update)
	}
	if update.Type == "chat_join_request" {
		return nil, bridge.receiveJoin(ctx, bot, update)
	}
	event := update.Member
	if event == nil || event.Subject.ID <= 0 || event.Subject.IsBot || event.New.User.ID != event.Subject.ID || event.Date <= 0 {
		return nil, nil
	}
	route, err := bridge.Routes.MembershipRoute(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if route.BotID != bot || route.ChatID != update.ChatID {
		return nil, membership.ErrConflict
	}
	managedActor := false
	if bridge.Bots != nil {
		instance, identityErr := bridge.Bots.Get(ctx, bot)
		if identityErr != nil {
			return nil, identityErr
		}
		managedActor = instance != nil && instance.ID == bot && instance.TelegramID > 0 && instance.Generation == route.Generation && instance.Enabled && instance.Mode == "internal" && instance.State == "running" && event.Operator.IsBot && event.Operator.ID == instance.TelegramID
	}
	_, err = bridge.Service.ObserveMembership(ctx, membership.AccessObservation{
		ActorIsManagedBot: managedActor,
		TargetID:          target.ID, BotID: bot, ChatID: update.ChatID, RouteVersion: route.RouteVersion,
		TelegramID: event.Subject.ID, Status: event.New.Status, IsMember: event.New.IsMember,
		ObservedAt: time.Unix(event.Date, 0).UTC(), Sequence: update.ID, Source: "chat_member",
	})
	if errors.Is(err, membership.ErrUntrustedIdentity) {
		// Observing a system-external member never silently enrolls them for later removal.
		return nil, nil
	}
	return nil, err
}

// receiveJoin forwards the actual applicant and invite, never the temporary user_chat_id private capability.
func (bridge *Runtime) receiveJoin(ctx context.Context, bot uint, update runtime.Update) error {
	event := update.JoinRequest
	if event == nil || event.User.ID <= 0 || event.User.IsBot || event.Date <= 0 {
		return nil
	}
	link := ""
	if event.InviteLink != nil && event.InviteLink.CreatesJoinRequest && !event.InviteLink.IsRevoked {
		link = event.InviteLink.InviteLink
	}
	return bridge.Service.ReceiveMembershipJoin(ctx, membership.AccessJoinRequest{
		BotID: bot, UpdateID: update.ID, ChatID: update.ChatID, TelegramID: event.User.ID,
		InviteLink: link, ReceivedAt: time.Unix(event.Date, 0).UTC(),
	})
}
