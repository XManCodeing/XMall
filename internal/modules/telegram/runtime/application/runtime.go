package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/google/uuid"
)

// Runtime is supervised for the entire application lifetime, including while disabled.
// Every child is joined before Stop succeeds, which is required before Linux exec.
type Runtime struct {
	manager        *Manager
	leases         c.Leaser
	handler        c.Handler
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	stopping       bool
	ReconcileLocal func(context.Context, uint) error
	Reconcile      func(context.Context, uint) error
	Recover        func(context.Context) error
}

func NewRuntime(m *Manager, l c.Leaser, h c.Handler) *Runtime {
	return &Runtime{manager: m, leases: l, handler: h, done: make(chan struct{})}
}
func (*Runtime) Name() string { return "telegram-bot" }
func (r *Runtime) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.cancel = cancel
	if r.stopping {
		cancel()
	}
	r.mu.Unlock()
	defer close(r.done)
	defer cancel()
	var recoveryWG sync.WaitGroup
	if r.Recover != nil {
		recoveryWG.Add(1)
		go func() {
			defer recoveryWG.Done()
			for wait(ctx, time.Minute) {
				if err := recoverCall(func() error { return r.Recover(ctx) }); err != nil && ctx.Err() == nil {
					slog.WarnContext(ctx, "telegram_recovery_failed")
				}
			}
		}()
	}
	defer recoveryWG.Wait()
	defer cancel()
	type child struct {
		version uint64
		cancel  context.CancelFunc
		done    chan struct{}
	}
	children := map[uint]child{}
	stop := func(id uint) { ch := children[id]; ch.cancel(); <-ch.done; delete(children, id) }
	defer func() {
		for id := range children {
			stop(id)
		}
	}()
	for {
		instances, err := r.listInstances(ctx)
		if err == nil {
			desired := map[uint]d.Instance{}
			for _, i := range instances {
				if i.Enabled && i.Mode == "internal" && i.RecoveryTaskID == 0 {
					desired[i.ID] = i
				}
			}
			for id, ch := range children {
				i, ok := desired[id]
				exited := false
				select {
				case <-ch.done:
					exited = true
				default:
				}
				if !ok || i.Version != ch.version || exited {
					stop(id)
				}
			}
			for id, i := range desired {
				if _, ok := children[id]; !ok {
					childCtx, childCancel := context.WithCancel(ctx)
					done := make(chan struct{})
					children[id] = child{version: i.Version, cancel: childCancel, done: done}
					go func(instance d.Instance) {
						defer close(done)
						defer recoverWorker(childCtx, "bot", childCancel)
						r.runBot(childCtx, instance)
					}(i)
				}
			}
		}
		if !wait(ctx, 2*time.Second) {
			return nil
		}
	}
}
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.stopping = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (r *Runtime) runBot(parent context.Context, initial d.Instance) {
	if r.leases == nil || r.handler == nil {
		return
	}
	lease, err := r.leases.Acquire(parent, "xmall:telegram:bot:"+strconv.FormatInt(initial.TelegramID, 10))
	if err != nil {
		return
	}
	// Release only after all children have stopped, never with the cancelled context.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lease.Release(ctx)
	}()
	instance, err := r.manager.Store.BeginGeneration(parent, initial.ID, initial.Version)
	if err != nil {
		return
	}
	defer r.recordGenerationExit(instance)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ctx = c.WithExecutionFence(ctx, &leasedExecutionFence{store: r.manager.Store, lease: lease, instance: *instance, cancel: cancel})
	token, err := r.manager.Token(instance)
	if err != nil {
		_ = r.status(ctx, instance, "credential_error", "credential unavailable")
		return
	}
	callbacks := newCallbackScope(ctx, r.manager.API, token)
	callbacks.check = func(checkCtx context.Context) error { return r.checkRecovery(checkCtx, instance) }
	ctx = context.WithValue(ctx, callbackScopeKey{}, callbacks)
	var wg sync.WaitGroup
	defer func() {
		// Cancel and join Answer requests before workers, final heartbeat and
		// lease release. Registration is sealed before the scope waits.
		callbacks.close()
		wg.Wait()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = r.status(stopCtx, instance, "stopped", "")
	}()
	defer cancel()
	leaseDeadline := time.AfterFunc(28*time.Second, cancel)
	defer leaseDeadline.Stop()
	startWorker(ctx, &wg, "lease", cancel, func() {
		for wait(ctx, 8*time.Second) {
			renewCtx, renewCancel := context.WithTimeout(ctx, 3*time.Second)
			err := lease.Renew(renewCtx)
			renewCancel()
			if err != nil {
				cancel()
				return
			}
			leaseDeadline.Reset(25 * time.Second)
			current, err := r.manager.Store.Get(ctx, instance.ID)
			if err != nil || current.RecoveryTaskID != 0 || !current.Enabled || current.Version != instance.Version || current.Generation != instance.Generation {
				cancel()
				return
			}
		}
	})
	if err := r.checkRecovery(ctx, instance); err != nil {
		return
	}
	webhook, err := r.manager.API.Webhook(ctx, token)
	if err != nil || webhook != "" {
		_ = r.status(ctx, instance, "blocked", "webhook check failed or takeover required")
		wait(ctx, 15*time.Second)
		return
	}
	if r.Reconcile != nil {
		startWorker(ctx, &wg, "reconcile", cancel, func() {
			r.reconcileLane(ctx, instance, cancel, r.Reconcile, "telegram_reconcile_failed")
		})
	}
	if r.ReconcileLocal != nil {
		startWorker(ctx, &wg, "local_reconcile", cancel, func() { r.reconcileLocal(ctx, instance, cancel) })
	}
	startWorker(ctx, &wg, "inbox", cancel, func() { r.processInbox(ctx, instance, token) })
	startWorker(ctx, &wg, "delivery", cancel, func() { r.deliver(ctx, instance, token, lease, cancel) })
	r.pollGeneration(ctx, instance, token, lease)
}

// pollGeneration persists polled updates before advancing offsets or answering callbacks.
func (r *Runtime) pollGeneration(ctx context.Context, instance *d.Instance, token string, lease c.Lease) {
	offset := instance.Offset
	backoff := time.Second
	for ctx.Err() == nil {
		renewCtx, renewCancel := context.WithTimeout(ctx, 3*time.Second)
		renewErr := lease.Renew(renewCtx)
		renewCancel()
		if renewErr != nil {
			return
		}
		if err := r.status(ctx, instance, "running", ""); err != nil {
			return
		}
		updates, err := r.poll(ctx, instance.ID, token, offset)
		if err != nil {
			_ = r.status(ctx, instance, "degraded", "telegram polling failed")
			if !wait(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		batch, next, err := r.encodeUpdates(instance.ID, offset, updates)
		if err != nil {
			slog.WarnContext(ctx, "telegram update encoding unavailable", "bot_id", instance.ID, "error_class", failureErrorClass(err))
			_ = r.status(ctx, instance, "degraded", "message payload unavailable")
			if !wait(ctx, backoff) {
				return
			}
			backoff = min(30*time.Second, backoff*2)
			continue
		}
		if len(batch) > 0 {
			if err := r.manager.Store.Accept(ctx, instance.ID, instance.Generation, batch, next); err != nil {
				if errors.Is(err, c.ErrStale) {
					return
				}
				if !wait(ctx, time.Second) {
					return
				}
				continue
			}
			for _, u := range updates {
				if u.CallbackID != "" {
					r.callbackResponder(ctx, u.CallbackID)
				}
			}
			offset = next
		} else if !wait(ctx, 100*time.Millisecond) {
			return
		}
		backoff = time.Second
	}
}
func (r *Runtime) status(ctx context.Context, i *d.Instance, state, detail string) error {
	return r.manager.Store.Heartbeat(ctx, i.ID, i.Generation, state, detail, i.Version)
}
func (r *Runtime) handleEntry(ctx context.Context, i *d.Instance, token string, entry *d.Inbox) (err error) {
	handlerStarted := false
	defer func() {
		if recover() != nil {
			err = errProcessingPanic
			// Handlers can perform external business actions. Without a receipt
			// their panic is terminal, even if it occurred before the action.
			if handlerStarted {
				err = errProcessingUnknown
			}
		}
	}()
	raw, err := r.manager.Open(entry.PayloadCipher)
	if err != nil {
		return err
	}
	var update c.Update
	if err = json.Unmarshal([]byte(raw), &update); err != nil {
		return err
	}
	if update.CallbackID != "" {
		answer := r.callbackResponder(ctx, update.CallbackID)
		ctx = c.WithCallbackResponder(ctx, answer)
		defer answer("")
	}
	if !validUpdate(update) {
		return nil
	}
	var contact *d.Contact
	if shopContactUpdate(update) {
		contact, err = r.manager.Store.Contact(ctx, i.ID, update.UserID)
		if err != nil && !errors.Is(err, c.ErrNotFound) {
			return err
		}
		if contact == nil {
			contact = &d.Contact{BotID: i.ID, TelegramUserID: update.UserID}
		}
		contact.ChatID = update.ChatID
		contact.Reachable = true
		if contact.Locale == "" {
			contact.Locale = update.Locale
		}
		if err = r.manager.Store.SaveContact(ctx, contact); err != nil {
			return err
		}
	}
	handlerStarted = true
	if store, ok := r.manager.Store.(c.InboxExecutionStore); ok && entry.Claim != "" {
		if err := store.MarkInboxExecution(ctx, entry.ID, entry.Claim); err != nil {
			return err
		}
	}
	messages, err := r.handler.Handle(ctx, i.ID, update)
	if err != nil {
		return err
	}
	if shopContactUpdate(update) {
		contact, err = r.manager.Store.Contact(ctx, i.ID, update.UserID)
		if err != nil {
			return err
		}
	}
	return r.enqueueUpdateMessages(ctx, i, update, messages)
}

// enqueueUpdateMessages binds replies to the authenticated update and authorizes before enqueue.
func (r *Runtime) enqueueUpdateMessages(ctx context.Context, i *d.Instance, update c.Update, messages []c.Message) error {
	for n, message := range messages {
		// Fixed infrastructure notices are issued only by the runtime inbox worker.
		if message.Purpose == failureNoticePurpose {
			return errTarget
		}
		message.TargetType = message.TargetType.Normalize()
		if !message.TargetType.Valid() {
			return errTarget
		}
		if message.TargetType == c.TargetShopPrivate {
			if !privateUpdate(update) {
				return errTarget
			}
			// A shopping handler cannot redirect private replies to another chat.
			message.ChatID = update.ChatID
		} else if message.Purpose == "panel" || message.Purpose == "cleanup" {
			return errTarget
		}
		if message.Purpose == "panel" && message.PublicReply == nil {
			// Only a callback source describes an existing Bot panel. A new
			// incoming video/sticker must not relabel the shopping response.
			if update.CallbackID != "" {
				message.TargetKind = update.MessageKind
			}
			// Only the actual private callback source can claim an unknown send.
			if update.CallbackID == "" || message.TargetMessageID != update.MessageID {
				message.TargetMessageID = 0
			}
		}
		var previousMessageID int64
		if message.Purpose == "cleanup" {
			if message.TargetMessageID != update.MessageID || update.MessageID <= 0 {
				continue
			}
			if update.CallbackID != "" {
				// Callback data is not a confirmed platform receipt. Only the
				// receipt-backed replacement cleanup path may delete Bot panels.
				continue
			}
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		cipher, err := r.manager.Seal(string(raw))
		if err != nil {
			return err
		}
		ownerID := message.OwnerUserID
		item := &d.Delivery{BotID: i.ID, Key: fmt.Sprintf("update:%d:%d", update.ID, n), TargetType: string(message.TargetType), ChatID: message.ChatID, TelegramUserID: update.UserID, UserID: ownerID, PayloadCipher: cipher, Priority: messagePriority(message), Purpose: message.Purpose, PanelEpoch: message.PanelEpoch, PanelRevision: message.PanelRevision, Operation: message.Operation, TargetMessageID: message.TargetMessageID, PreviousMessageID: previousMessageID, Status: "pending", AvailableAt: time.Now()}
		if message.InviteDelivery != nil {
			item.Key = fmt.Sprintf("membership-invite:%d:update:%d:%d", message.InviteDelivery.InviteID, update.ID, n)
		}
		if _, err = deliveryTarget(item, message); err != nil {
			return err
		}
		if err = r.authorizeMessage(ctx, i.ID, item, message); err != nil {
			return err
		}
		if err = r.manager.Store.Enqueue(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runtime) authorizeStamp(ctx context.Context, bot uint, tg int64, expected string) error {
	if guard, ok := r.handler.(interface {
		AuthorizeStamp(context.Context, uint, int64, string) error
	}); ok {
		return guard.AuthorizeStamp(ctx, bot, tg, expected)
	}
	if expected != "" {
		return errTarget
	}
	return nil
}
func (r *Runtime) deliver(ctx context.Context, i *d.Instance, token string, lease c.Lease, cancelBot context.CancelFunc) {
	nextChat := map[int64]time.Time{}
	var nextBot time.Time
	for ctx.Err() == nil {
		if delay := time.Until(nextBot); delay > 0 && !wait(ctx, delay) {
			return
		}
		claim := uuid.NewString()
		item, err := r.claimReadyDelivery(ctx, i.ID, i.Generation, claim, nextChat)
		if errors.Is(err, c.ErrStale) {
			cancelBot()
			return
		}
		if err != nil || item == nil {
			if !wait(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		// Excluding stores generate the claim under the generation fence.
		if item.Claim != "" {
			claim = item.Claim
		}
		stop, delayed := r.processDelivery(ctx, i, token, lease, cancelBot, item, claim, nextChat)
		if stop {
			return
		}
		if !delayed.IsZero() {
			nextBot = delayed
		}
		if !wait(ctx, 50*time.Millisecond) {
			return
		}
	}
}

// processDelivery checks a claimed envelope, executes it once and persists its outcome.
// It returns stop for lost ownership and a bot-wide rate-limit deadline when present.
func (r *Runtime) processDelivery(ctx context.Context, i *d.Instance, token string, lease c.Lease, cancelBot context.CancelFunc, item *d.Delivery, claim string, nextChat map[int64]time.Time) (bool, time.Time) {
	externalStarted := false
	defer r.recoverDelivery(ctx, item, claim, &externalStarted)
	var nextBot time.Time
	var err error
	status, detail := "sent", ""
	var messageID int64
	var transferReceipt c.DeliveryReceipt
	var usedTransfer bool
	available := time.Now()
	{
		raw, decodeErr := r.manager.Open(item.PayloadCipher)
		var message c.Message
		if decodeErr == nil {
			decodeErr = json.Unmarshal([]byte(raw), &message)
		}
		if decodeErr == nil {
			decodeErr = r.authorizeMessage(ctx, i.ID, item, message)
		}
		if decodeErr != nil {
			status, available, detail = deferredDelivery(decodeErr, item, available)
		} else {
			ready, stop := r.prepareDelivery(ctx, i, item, claim, &message, lease, cancelBot)
			if !ready {
				return stop, time.Time{}
			}
			if store, ok := r.manager.Store.(c.DeliveryExecutionStore); ok {
				if err := store.MarkDeliveryExecution(ctx, item.ID, claim); err != nil {
					return false, time.Time{}
				}
			}
			externalStarted = true
			delivered, receipt, sendErr := r.sendDelivery(ctx, token, message)
			usedTransfer = message.Transfer != nil
			transferReceipt, err = receipt, sendErr
			messageID = delivered.MessageID
			if delivered.MessageKind != "" {
				item.MessageKind = delivered.MessageKind
			}
			if semantic(err) == "not_modified" {
				err = nil
				messageID = message.TargetMessageID
			}
			if err == nil && messageID == 0 && message.Operation == "edit" {
				messageID = message.TargetMessageID
			}
			if err == nil && messageID <= 0 && (message.PublicReply != nil || message.JoinAbuseDelivery != nil || message.ProactiveDelivery != nil || message.ReminderDelivery != nil || message.InviteDelivery != nil || message.TargetType != c.TargetShopPrivate) && (message.Operation == "" || message.Operation == "send") {
				// A successful HTTP envelope without a platform message ID
				// cannot prove a consultation was delivered. Never auto-retry.
				err = errors.New("missing platform delivery receipt")
			}
			if (semantic(err) == "target_missing" || semantic(err) == "media_transition") && message.Purpose == "panel" && message.Operation == "edit" && item.Attempts < 3 {
				if replacer, ok := r.manager.Store.(interface {
					ReplacePanel(context.Context, *d.Delivery) error
				}); ok && replacer.ReplacePanel(ctx, item) == nil {
					_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "retry", 0, "", time.Now())
					return false, time.Time{}
				}
			}
			nextChat[item.ChatID] = time.Now().Add(deliveryChatCooldown(message))
			// Bound limiter state to active chats instead of retaining every
			// broadcast recipient for the lifetime of the process.
			for chat, until := range nextChat {
				if !until.After(time.Now()) {
					delete(nextChat, chat)
				}
			}
			status, detail, available, nextBot = r.classifyDelivery(ctx, i, item, message, err)
		}
	}
	r.finishDeliveryOutcome(ctx, i, item, claim, status, detail, messageID, transferReceipt, usedTransfer, available)
	return false, nextBot
}

// finishDeliveryOutcome retries receipt persistence only, never a completed external send.
func (r *Runtime) finishDeliveryOutcome(ctx context.Context, i *d.Instance, item *d.Delivery, claim, status, detail string, messageID int64, transferReceipt c.DeliveryReceipt, usedTransfer bool, available time.Time) {
	var err error
	if item.Purpose == "panel" && c.TargetType(item.TargetType).Normalize() == c.TargetShopPrivate {
		if panel, ok := r.manager.Store.(c.PanelStore); ok {
			// Retry persistence, never the external send. Keep the known result
			// alive through transient DB errors and shutdown cancellation.
			finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			for {
				err = panel.FinishPanel(finishCtx, item, status, c.DeliveryResult{MessageID: messageID, MessageKind: item.MessageKind}, detail, available)
				if err == nil || errors.Is(err, c.ErrStale) || !wait(finishCtx, 100*time.Millisecond) {
					break
				}
			}
			finishCancel()
			if err == nil && status == "sent" && item.Operation == "send" && item.PreviousMessageID > 0 && item.PreviousMessageID != messageID {
				r.enqueuePanelCleanup(ctx, item)
			}
		}
	} else if target := c.TargetType(item.TargetType).Normalize(); target == c.TargetSupportPrivate || target == c.TargetSupportGroup || item.Purpose == "membership_reminder" || item.Purpose == "membership_join_abuse" || item.Purpose == "membership_proactive" {
		// Keep the known receipt through shutdown/transient storage errors.
		// Retry only persistence, never the external consultation send.
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		for {
			if receiptStore, ok := r.manager.Store.(c.DeliveryReceiptStore); usedTransfer && ok {
				err = receiptStore.FinishDeliveryReceipt(finishCtx, item.ID, claim, status, transferReceipt, detail, available)
			} else {
				err = r.manager.Store.FinishDelivery(finishCtx, item.ID, claim, status, messageID, detail, available)
			}
			if err == nil || errors.Is(err, c.ErrStale) || !wait(finishCtx, 100*time.Millisecond) {
				break
			}
		}
		finishCancel()
	} else {
		_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, status, messageID, detail, available)
	}
	if item.Purpose == "cleanup" && c.TargetType(item.TargetType).Normalize() == c.TargetShopPrivate && item.Operation == "delete" && item.PreviousMessageID > 0 && (status == "failed" || status == "unknown") {
		r.enqueueClearButtons(ctx, item)
	}
	if status == "sent" {
		if tracker, ok := r.manager.Store.(interface {
			RecordSent(context.Context, uint, uint64) error
		}); ok {
			_ = tracker.RecordSent(ctx, i.ID, i.Generation)
		}
	}
}

// prepareDelivery renews execution ownership and revalidates before any external effect.
// It returns ready and whether a lost generation requires stopping all bot workers.
func (r *Runtime) prepareDelivery(ctx context.Context, i *d.Instance, item *d.Delivery, claim string, message *c.Message, lease c.Lease, cancelBot context.CancelFunc) (bool, bool) {
	message.ChatID = item.ChatID
	message.TargetType = message.TargetType.Normalize()
	renewCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := lease.Renew(renewCtx)
	cancel()
	if err != nil {
		cancelBot()
		return false, true
	}
	if err := r.authorizeMessage(ctx, i.ID, item, *message); err != nil {
		status, at, detail := deferredDelivery(err, item, time.Now())
		r.finishDeliveryOutcome(ctx, i, item, claim, status, detail, 0, c.DeliveryReceipt{}, false, at)
		return false, false
	}
	authorized := *message
	if message.Purpose == "panel" && !r.prepareDeliveryPanel(ctx, item, claim, message) {
		return false, false
	}
	if err := r.checkRecovery(ctx, i); err != nil {
		cancelBot()
		return false, true
	}
	if err := r.authorizePublicBeforeSend(ctx, i.ID, item, authorized); err != nil {
		status, at, detail := deferredDelivery(err, item, time.Now())
		r.finishDeliveryOutcome(ctx, i, item, claim, status, detail, 0, c.DeliveryReceipt{}, false, at)
		return false, false
	}
	return true, false
}

// prepareDeliveryPanel persists exact transport intent; uncertain creations remain terminal.
func (r *Runtime) prepareDeliveryPanel(ctx context.Context, item *d.Delivery, claim string, message *c.Message) bool {
	panel, ok := r.manager.Store.(c.PanelStore)
	if !ok {
		_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "failed", 0, "message payload unavailable", time.Now())
		return false
	}
	if err := panel.PreparePanel(ctx, item, message); err != nil {
		status := "retry"
		if errors.Is(err, c.ErrConflict) {
			status = "superseded"
		}
		if errors.Is(err, c.ErrStale) {
			status = "unknown"
		}
		_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, status, 0, "", time.Now().Add(time.Second))
		return false
	}
	return true
}

// sendDelivery performs exactly one transport attempt and returns its observed receipt.
func (r *Runtime) sendDelivery(ctx context.Context, token string, message c.Message) (c.DeliveryResult, c.DeliveryReceipt, error) {
	timeout := 20 * time.Second
	if message.Purpose == "cleanup" {
		timeout = 2 * time.Second
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if message.Transfer != nil {
		return r.executeTransfer(sendCtx, token, message)
	}
	var result c.DeliveryResult
	var err error
	if api, ok := r.manager.API.(c.ApplyingAPI); ok {
		result, err = api.Apply(sendCtx, token, message)
	} else if message.TargetType.Normalize() == c.TargetShopPrivate && (message.Operation == "" || message.Operation == "send") {
		result.MessageID, err = r.manager.API.Send(sendCtx, token, message)
	} else {
		err = unsupportedDelivery{}
	}
	return result, c.DeliveryReceipt{}, err
}

// classifyDelivery preserves definite retry semantics and never retries unknown sends.
func (r *Runtime) classifyDelivery(ctx context.Context, i *d.Instance, item *d.Delivery, message c.Message, err error) (string, string, time.Time, time.Time) {
	status, detail, available, nextBot := "sent", "", time.Now(), time.Time{}
	if err == nil {
		return status, detail, available, nextBot
	}
	status, detail = "unknown", "delivery outcome unknown"
	mutable := message.Operation == "edit" || message.Operation == "delete" || message.Operation == "clear_buttons" || message.Operation == "markup"
	if mutable {
		status = "retry"
		available = time.Now().Add(time.Second)
	}
	var apiErr c.APIError
	if errors.As(err, &apiErr) && !apiErr.Uncertain() {
		status, detail = "failed", "telegram rejected message"
		if apiErr.StatusCode() == 400 && semantic(err) == "topic_missing" && message.TargetType == c.TargetSupportGroup && message.Purpose == "support_ready" && message.ThreadID > 0 {
			detail = "topic_missing"
		}
		if apiErr.StatusCode() >= 500 {
			status = "unknown"
		}
		if apiErr.StatusCode() == 429 || ((apiErr.StatusCode() >= 500 || semantic(err) == "retryable") && mutable) {
			status = "retry"
			available = time.Now().Add(time.Duration(max(5, apiErr.RetrySeconds())) * time.Second)
			// retry_after does not identify whether Telegram throttled the whole
			// bot or just this group. Preserve the conservative bot-wide backoff;
			// the independent group budget prevents routine traffic causing it.
			if apiErr.StatusCode() == 429 && message.Purpose != "cleanup" {
				nextBot = available
			}
		}
		if apiErr.StatusCode() == 403 && (message.ReminderDelivery != nil || message.JoinAbuseDelivery != nil) {
			status = "unreachable"
		}
		if apiErr.StatusCode() == 403 && message.TargetType.Normalize() == c.TargetShopPrivate {
			contact, lookupErr := r.manager.Store.Contact(ctx, i.ID, item.TelegramUserID)
			if lookupErr == nil && contact != nil {
				contact.Reachable = false
				_ = r.manager.Store.SaveContact(ctx, contact)
			}
		}
	}
	if (message.InviteDelivery != nil || message.ProactiveDelivery != nil) && status == "retry" {
		var definite c.APIError
		if !errors.As(err, &definite) || definite.Uncertain() || definite.StatusCode() >= 500 {
			status = "unknown"
		}
	}
	limit := 8
	if mutable {
		limit = 3
	}
	if message.Purpose == "cleanup" {
		limit = 2
	}
	if item.Attempts >= limit && status == "retry" {
		status = "failed"
	}
	return status, detail, available, nextBot
}
