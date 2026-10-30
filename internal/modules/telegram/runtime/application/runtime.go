package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	manager    *Manager
	leases     c.Leaser
	handler    c.Handler
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	stopping   bool
	Reconcile  func(context.Context, uint) error
	Recover    func(context.Context) error
	callbackMu sync.Mutex
	callbacks  map[string]*callbackAnswer
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
				_ = r.Recover(ctx)
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
		instances, err := r.manager.Store.List(ctx)
		if err == nil {
			desired := map[uint]d.Instance{}
			for _, i := range instances {
				if i.Enabled && i.Mode == "internal" {
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
					go func(instance d.Instance) { defer close(done); r.runBot(childCtx, instance) }(i)
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
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	token, err := r.manager.Token(instance)
	if err != nil {
		_ = r.status(ctx, instance, "credential_error", "credential unavailable")
		return
	}
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = r.status(stopCtx, instance, "stopped", "")
	}()
	defer cancel()
	leaseDeadline := time.AfterFunc(28*time.Second, cancel)
	defer leaseDeadline.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
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
			if err != nil || !current.Enabled || current.Version != instance.Version || current.Generation != instance.Generation {
				cancel()
				return
			}
		}
	}()
	webhook, err := r.manager.API.Webhook(ctx, token)
	if err != nil || webhook != "" {
		_ = r.status(ctx, instance, "blocked", "webhook check failed or takeover required")
		wait(ctx, 15*time.Second)
		return
	}
	if r.Reconcile != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_ = r.Reconcile(ctx, instance.ID)
				if !wait(ctx, 2*time.Second) {
					return
				}
			}
		}()
	}
	wg.Add(2)
	go func() { defer wg.Done(); r.processInbox(ctx, instance, token) }()
	go func() { defer wg.Done(); r.deliver(ctx, instance, token, lease, cancel) }()
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
		updates, err := r.manager.API.Poll(ctx, token, offset)
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
		batch := make([]d.Inbox, 0, len(updates))
		next := offset
		for _, u := range updates {
			raw, err := json.Marshal(u)
			if err != nil {
				return
			}
			cipher, err := r.manager.Seal(string(raw))
			if err != nil {
				return
			}
			batch = append(batch, d.Inbox{BotID: instance.ID, UpdateID: u.ID, PayloadCipher: cipher, Status: "pending", AvailableAt: time.Now()})
			if u.ID >= next {
				next = u.ID + 1
			}
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
					r.callbackResponder(ctx, token, u.CallbackID)
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
func (r *Runtime) processInbox(ctx context.Context, i *d.Instance, token string) {
	for ctx.Err() == nil {
		claim := uuid.NewString()
		entry, err := r.manager.Store.ClaimInbox(ctx, i.ID, claim, time.Now().Add(2*time.Minute))
		if err != nil || entry == nil {
			if !wait(ctx, 300*time.Millisecond) {
				return
			}
			continue
		}
		processCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = r.handleEntry(processCtx, i, token, entry)
		cancel()
		state, summary := "done", ""
		if err != nil {
			state = "retry"
			summary = "update processing failed"
			if entry.Attempts >= 8 {
				state = "failed"
			}
		}
		_ = r.manager.Store.FinishInbox(ctx, entry.ID, claim, state, summary, time.Now().Add(5*time.Second))
	}
}
func (r *Runtime) handleEntry(ctx context.Context, i *d.Instance, token string, entry *d.Inbox) error {
	raw, err := r.manager.Open(entry.PayloadCipher)
	if err != nil {
		return err
	}
	var update c.Update
	if err = json.Unmarshal([]byte(raw), &update); err != nil {
		return err
	}
	if update.CallbackID != "" {
		answer := r.callbackResponder(ctx, token, update.CallbackID)
		ctx = c.WithCallbackResponder(ctx, answer)
		defer answer("")
	}
	if !update.Private || update.UserID == 0 || update.ChatID != update.UserID {
		return nil
	}
	contact, err := r.manager.Store.Contact(ctx, i.ID, update.UserID)
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
	messages, err := r.handler.Handle(ctx, i.ID, update)
	if err != nil {
		return err
	}
	contact, err = r.manager.Store.Contact(ctx, i.ID, update.UserID)
	if err != nil {
		return err
	}
	for n, message := range messages {
		// A handler cannot use a user update to redirect private replies to another chat.
		message.ChatID = update.ChatID
		if message.Purpose == "panel" {
			message.TargetKind = update.MessageKind
			// Only the actual private callback source can claim an unknown send.
			if update.CallbackID == "" || message.TargetMessageID != update.MessageID {
				message.TargetMessageID = 0
			}
		}
		if message.Purpose == "cleanup" && (update.CallbackID != "" || message.TargetMessageID != update.MessageID || update.MessageID <= 0) {
			continue
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
		if ownerID == 0 && contact != nil {
			ownerID = contact.UserID
		}
		priority := 100
		if message.Purpose == "notice" || message.Purpose == "notification" || message.Purpose == "delivery" {
			priority = 200
		}
		if message.Purpose == "cleanup" {
			priority = 110
		}
		if err = r.manager.Store.Enqueue(ctx, &d.Delivery{BotID: i.ID, Key: fmt.Sprintf("update:%d:%d", update.ID, n), ChatID: update.ChatID, TelegramUserID: update.UserID, UserID: ownerID, PayloadCipher: cipher, Priority: priority, Purpose: message.Purpose, PanelEpoch: message.PanelEpoch, PanelRevision: message.PanelRevision, Operation: message.Operation, TargetMessageID: message.TargetMessageID, Status: "pending", AvailableAt: time.Now()}); err != nil {
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
		item, err := r.manager.Store.ClaimDelivery(ctx, i.ID, claim, time.Now().Add(time.Minute))
		if err != nil || item == nil {
			if !wait(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		status, detail := "sent", ""
		var messageID int64
		available := time.Now()
		if err = r.handler.AuthorizeDelivery(ctx, i.ID, item.TelegramUserID, item.UserID); err != nil {
			status = "failed"
			detail = "recipient authorization failed"
		} else {
			raw, decodeErr := r.manager.Open(item.PayloadCipher)
			var message c.Message
			if decodeErr == nil {
				decodeErr = json.Unmarshal([]byte(raw), &message)
			}
			if decodeErr == nil {
				decodeErr = r.authorizeStamp(ctx, i.ID, item.TelegramUserID, message.IdentityStamp)
			}
			if decodeErr != nil {
				status = "failed"
				detail = "message payload unavailable"
			} else {
				message.ChatID = item.ChatID
				if delay := time.Until(nextChat[item.ChatID]); delay > 0 {
					if !wait(ctx, delay) {
						return
					}
				}
				// Verify ownership again immediately before an external side effect.
				renewCtx, renewCancel := context.WithTimeout(ctx, 3*time.Second)
				renewErr := lease.Renew(renewCtx)
				renewCancel()
				if renewErr != nil {
					cancelBot()
					return
				}
				// Revalidate after rate-limit waiting: binding may have changed while
				// this delivery was queued or sleeping. Never send cached secrets on
				// the strength of the earlier check alone.
				if authErr := r.handler.AuthorizeDelivery(ctx, i.ID, item.TelegramUserID, item.UserID); authErr != nil {
					_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "failed", 0, "recipient authorization failed", time.Now())
					continue
				}
				if authErr := r.authorizeStamp(ctx, i.ID, item.TelegramUserID, message.IdentityStamp); authErr != nil {
					_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "failed", 0, "recipient binding changed", time.Now())
					continue
				}
				if message.Purpose == "panel" {
					panel, ok := r.manager.Store.(c.PanelStore)
					if !ok {
						_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "failed", 0, "message payload unavailable", time.Now())
						continue
					}
					if prepErr := panel.PreparePanel(ctx, item, &message); prepErr != nil {
						state := "retry"
						if errors.Is(prepErr, c.ErrConflict) {
							state = "superseded"
						}
						if errors.Is(prepErr, c.ErrStale) {
							state = "unknown"
						}
						_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, state, 0, "", time.Now().Add(time.Second))
						continue
					}
				}
				timeout := 20 * time.Second
				if message.Purpose == "cleanup" {
					timeout = 2 * time.Second
				}
				sendCtx, cancel := context.WithTimeout(ctx, timeout)
				var delivered c.DeliveryResult
				if api, ok := r.manager.API.(c.ApplyingAPI); ok {
					delivered, err = api.Apply(sendCtx, token, message)
				} else if message.Operation == "" || message.Operation == "send" {
					delivered.MessageID, err = r.manager.API.Send(sendCtx, token, message)
				} else {
					err = errors.New("operation unsupported")
				}
				messageID = delivered.MessageID
				if delivered.MessageKind != "" {
					item.MessageKind = delivered.MessageKind
				}
				cancel()
				if semantic(err) == "not_modified" {
					err = nil
					messageID = message.TargetMessageID
				}
				if err == nil && messageID == 0 && message.Operation == "edit" {
					messageID = message.TargetMessageID
				}
				if (semantic(err) == "target_missing" || semantic(err) == "media_transition") && message.Purpose == "panel" && message.Operation == "edit" && item.Attempts < 3 {
					if replacer, ok := r.manager.Store.(interface {
						ReplacePanel(context.Context, *d.Delivery) error
					}); ok && replacer.ReplacePanel(ctx, item) == nil {
						_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, "retry", 0, "", time.Now())
						continue
					}
				}
				nextChat[item.ChatID] = time.Now().Add(time.Second)
				// Bound limiter state to active chats instead of retaining every
				// broadcast recipient for the lifetime of the process.
				for chat, until := range nextChat {
					if !until.After(time.Now()) {
						delete(nextChat, chat)
					}
				}
				if err != nil {
					status = "unknown"
					detail = "delivery outcome unknown"
					if message.Operation == "edit" || message.Operation == "delete" || message.Operation == "clear_buttons" || message.Operation == "markup" {
						status = "retry"
						available = time.Now().Add(time.Second)
					}
					var apiErr c.APIError
					if errors.As(err, &apiErr) && !apiErr.Uncertain() {
						status = "failed"
						detail = "telegram rejected message"
						if apiErr.StatusCode() >= 500 {
							status = "unknown"
						}
						if apiErr.StatusCode() == 429 || ((apiErr.StatusCode() >= 500 || semantic(err) == "retryable") && (message.Operation == "edit" || message.Operation == "delete" || message.Operation == "markup" || message.Operation == "clear_buttons")) {
							status = "retry"
							delay := apiErr.RetrySeconds()
							if delay < 5 {
								delay = 5
							}
							available = time.Now().Add(time.Duration(delay) * time.Second)
							if apiErr.StatusCode() == 429 && message.Purpose != "cleanup" {
								// Telegram flood limits can be Bot-wide; do not keep
								// hammering other chats while this item waits.
								nextBot = available
							}
						}
						if apiErr.StatusCode() == 403 {
							contact, e := r.manager.Store.Contact(ctx, i.ID, item.TelegramUserID)
							if e == nil && contact != nil {
								contact.Reachable = false
								_ = r.manager.Store.SaveContact(ctx, contact)
							}
						}
					}
					limit := 8
					if message.Operation == "edit" || message.Operation == "delete" || message.Operation == "clear_buttons" || message.Operation == "markup" {
						limit = 3
					}
					if message.Purpose == "cleanup" {
						limit = 2
					}
					if item.Attempts >= limit && status == "retry" {
						status = "failed"
					}
				}
			}
		}
		if item.Purpose == "panel" {
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
		} else {
			_ = r.manager.Store.FinishDelivery(ctx, item.ID, claim, status, messageID, detail, available)
		}
		if item.Purpose == "cleanup" && item.Operation == "delete" && item.PreviousMessageID > 0 && (status == "failed" || status == "unknown") {
			r.enqueueClearButtons(ctx, item)
		}
		if status == "sent" {
			if tracker, ok := r.manager.Store.(interface {
				RecordSent(context.Context, uint, uint64) error
			}); ok {
				_ = tracker.RecordSent(ctx, i.ID, i.Generation)
			}
		}
		if !wait(ctx, 50*time.Millisecond) {
			return
		}
	}
}
