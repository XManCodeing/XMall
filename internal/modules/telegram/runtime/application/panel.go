package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"log/slog"
	"sync"
	"time"
)

type callbackAnswer struct {
	request chan string
	done    chan struct{}
	expires time.Time
}

// A scope belongs to exactly one runBot generation. Its worker owns both the
// fallback timer and the HTTP call. Tombstones deduplicate only within this
// generation: a replay in a new generation gets a fresh responder/token.
// Old responder closures remain inert after their scope is cancelled.
type callbackScope struct {
	ctx       context.Context
	cancel    context.CancelFunc
	api       c.API
	token     string
	mu        sync.Mutex
	closed    bool
	workers   sync.WaitGroup
	callbacks map[string]*callbackAnswer
	check     func(context.Context) error
	newTimer  func() (<-chan time.Time, func())
}

type callbackScopeKey struct{}

func newCallbackScope(parent context.Context, api c.API, token string) *callbackScope {
	ctx, cancel := context.WithCancel(parent)
	return &callbackScope{ctx: ctx, cancel: cancel, api: api, token: token, callbacks: make(map[string]*callbackAnswer), newTimer: func() (<-chan time.Time, func()) {
		timer := time.NewTimer(1500 * time.Millisecond)
		return timer.C, func() { timer.Stop() }
	}}
}

func (s *callbackScope) close() {
	s.mu.Lock()
	s.closed = true // excludes all future Add calls before Wait begins
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
}

func (s *callbackScope) responder(id string) func(string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return func(string) {}
	}
	now := time.Now()
	for key, old := range s.callbacks {
		if old.expires.Before(now) {
			select {
			case <-old.done:
				delete(s.callbacks, key)
			default: // an uncooperative transport must not allow a duplicate
			}
		}
	}
	state := s.callbacks[id]
	if state == nil {
		state = &callbackAnswer{request: make(chan string, 1), done: make(chan struct{}), expires: now.Add(10 * time.Minute)}
		s.callbacks[id] = state
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer close(state.done)
			// A callback answer is a one-shot external action. Keep its tombstone
			// on panic; later callbacks still get independent supervised workers.
			defer recoverWorker(s.ctx, "callback", func() {})
			timer, stopTimer := s.newTimer()
			defer stopTimer()
			var text string
			select {
			case <-s.ctx.Done():
				return
			case text = <-state.request:
			case <-timer:
			}
			// Cancellation wins even when a request/timer is also ready.
			if s.ctx.Err() != nil {
				return
			}
			answerCtx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
			defer cancel()
			if s.check != nil && s.check(answerCtx) != nil {
				return
			}
			if err := s.api.Answer(answerCtx, s.token, id, text); err != nil {
				// Never log callback IDs, tokens or response text. Verification may
				// have committed even when Telegram fails to display its feedback.
				slog.WarnContext(answerCtx, "telegram callback answer unavailable", "error_class", failureErrorClass(err))
			}
		}()
	}
	return func(text string) {
		select {
		case state.request <- text:
		default:
		}
		// Preserve synchronous explicit answers and once-only semantics. The
		// same worker handles every contender and closes done after HTTP exits.
		<-state.done
	}
}

// Registered as soon as Accept commits, independent of the serial inbox handler.
func (r *Runtime) callbackResponder(ctx context.Context, id string) func(string) {
	if scope, ok := ctx.Value(callbackScopeKey{}).(*callbackScope); ok {
		return scope.responder(id)
	}
	// Never create unowned timers when no supervised generation is present.
	return func(string) {}
}

func semantic(err error) string {
	if err == nil {
		return ""
	}
	var classified interface{ Kind() string }
	if errors.As(err, &classified) {
		return classified.Kind()
	}
	return ""
}

func (r *Runtime) shopCleanupTarget(item *d.Delivery) bool {
	if c.TargetType(item.TargetType).Normalize() != c.TargetShopPrivate {
		return false
	}
	raw, err := r.manager.Open(item.PayloadCipher)
	if err != nil {
		return false
	}
	var message c.Message
	if json.Unmarshal([]byte(raw), &message) != nil {
		return false
	}
	target, err := deliveryTarget(item, message)
	return err == nil && target == c.TargetShopPrivate
}

func (r *Runtime) cleanupStamp(item *d.Delivery) string {
	if c.TargetType(item.TargetType).Normalize() != c.TargetShopPrivate {
		return ""
	}
	raw, err := r.manager.Open(item.PayloadCipher)
	if err != nil {
		return ""
	}
	var message c.Message
	if json.Unmarshal([]byte(raw), &message) != nil {
		return ""
	}
	return message.IdentityStamp
}

func (r *Runtime) enqueueClearButtons(ctx context.Context, item *d.Delivery) {
	r.enqueueConfirmedCleanup(ctx, item, "markup")
}

func (r *Runtime) enqueuePanelCleanup(ctx context.Context, item *d.Delivery) {
	r.enqueueConfirmedCleanup(ctx, item, "delete")
}

// authorizePanelCleanup authorizes only receipt-backed removal without message content.
func (r *Runtime) authorizePanelCleanup(ctx context.Context, bot uint, item *d.Delivery, message c.Message) error {
	store, ok := r.manager.Store.(c.PanelCleanupStore)
	if !ok || item.BotID != bot || message.ChatID != item.ChatID || message.TargetType.Normalize() != c.TargetShopPrivate || message.Purpose != "cleanup" || item.Purpose != "cleanup" || message.TargetMessageID <= 0 || message.TargetMessageID != item.TargetMessageID || message.Operation != item.Operation || (message.Operation != "delete" && message.Operation != "markup") {
		return errTarget
	}
	if message.PublicReply != nil || message.JoinAbuseDelivery != nil || message.InviteDelivery != nil || message.ReminderDelivery != nil || message.Transfer != nil || message.Text != "" || len(message.Buttons) != 0 || len(message.Photo) != 0 || message.PhotoURL != "" || message.FileID != "" || message.BusinessRef != "" || message.OwnerUserID != 0 {
		return errTarget
	}
	if message.PanelEpoch != "" || message.PanelRevision != 0 || message.IdentityStamp != "" || message.ParseMode != "" || message.MediaName != "" || message.ForceDocument || message.ReplacePanel || message.TargetKind != "" || message.RouteVersion != 0 {
		return errTarget
	}
	return store.AuthorizePanelCleanup(ctx, bot, item.TelegramUserID, message.TargetMessageID, *message.PanelCleanup)
}

// enqueueConfirmedCleanup enqueues deletion only after both physical receipts are proven.
func (r *Runtime) enqueueConfirmedCleanup(ctx context.Context, item *d.Delivery, operation string) {
	if !r.shopCleanupTarget(item) || item.PreviousMessageID <= 0 {
		return
	}
	proof := &c.ConfirmedPanelCleanup{SourceDeliveryID: item.ID}
	if item.Purpose == "cleanup" {
		raw, err := r.manager.Open(item.PayloadCipher)
		if err != nil {
			return
		}
		var old c.Message
		if json.Unmarshal([]byte(raw), &old) != nil || old.PanelCleanup == nil {
			return
		}
		proof = old.PanelCleanup
	}
	message := c.Message{PanelCleanup: proof, TargetType: c.TargetShopPrivate, Purpose: "cleanup", Operation: operation, ChatID: item.ChatID, TargetMessageID: item.PreviousMessageID}
	delivery := &d.Delivery{BotID: item.BotID, Key: fmt.Sprintf("panel-%s:%d", operation, item.ID), TargetType: string(c.TargetShopPrivate), ChatID: item.ChatID, TelegramUserID: item.TelegramUserID, Purpose: "cleanup", Operation: operation, TargetMessageID: item.PreviousMessageID, PreviousMessageID: item.PreviousMessageID, Priority: 1, Status: "pending", AvailableAt: time.Now()}
	if r.authorizePanelCleanup(ctx, item.BotID, delivery, message) != nil {
		return
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return
	}
	delivery.PayloadCipher, err = r.manager.Seal(string(raw))
	if err != nil {
		return
	}
	_ = r.manager.Store.Enqueue(ctx, delivery)
}

// authorizePublicBeforeSend rechecks immutable content after panel preparation.
func (r *Runtime) authorizePublicBeforeSend(ctx context.Context, bot uint, item *d.Delivery, message c.Message) error {
	if message.PublicReply == nil {
		return nil
	}
	return r.authorizeMessage(ctx, bot, item, message)
}
