package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"sync"
	"time"
)

type callbackAnswer struct {
	once    sync.Once
	answer  func(string)
	expires time.Time
}

// Registered as soon as Accept commits, independent of the serial inbox handler.
// An answered tombstone prevents processing/retries from acknowledging twice.
func (r *Runtime) callbackResponder(ctx context.Context, token, id string) func(string) {
	r.callbackMu.Lock()
	defer r.callbackMu.Unlock()
	if r.callbacks == nil {
		r.callbacks = map[string]*callbackAnswer{}
	}
	tokenHash := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(tokenHash[:]) + ":" + id
	if old := r.callbacks[key]; old != nil {
		return old.answer
	}
	now := time.Now()
	for k, v := range r.callbacks {
		if v.expires.Before(now) {
			delete(r.callbacks, k)
		}
	}
	state := &callbackAnswer{expires: now.Add(10 * time.Minute)}
	state.answer = func(text string) {
		state.once.Do(func() {
			answerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = r.manager.API.Answer(answerCtx, token, id, text)
		})
	}
	r.callbacks[key] = state
	time.AfterFunc(1500*time.Millisecond, func() { state.answer("") })
	return state.answer
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

func (r *Runtime) cleanupStamp(item *d.Delivery) string {
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
	raw, err := json.Marshal(c.Message{IdentityStamp: r.cleanupStamp(item), Purpose: "cleanup", Operation: "markup", ChatID: item.ChatID, TargetMessageID: item.PreviousMessageID})
	if err != nil {
		return
	}
	cipher, err := r.manager.Seal(string(raw))
	if err != nil {
		return
	}
	_ = r.manager.Store.Enqueue(ctx, &d.Delivery{BotID: item.BotID, Key: fmt.Sprintf("panel-clear:%d", item.ID), ChatID: item.ChatID, TelegramUserID: item.TelegramUserID, UserID: item.UserID, PayloadCipher: cipher, Purpose: "cleanup", Operation: "markup", TargetMessageID: item.PreviousMessageID, Priority: 1, Status: "pending", AvailableAt: time.Now()})
}

func (r *Runtime) enqueuePanelCleanup(ctx context.Context, item *d.Delivery) {
	raw, err := json.Marshal(c.Message{IdentityStamp: r.cleanupStamp(item), Purpose: "cleanup", Operation: "delete", ChatID: item.ChatID, TargetMessageID: item.PreviousMessageID})
	if err != nil {
		return
	}
	cipher, err := r.manager.Seal(string(raw))
	if err != nil {
		return
	}
	_ = r.manager.Store.Enqueue(ctx, &d.Delivery{BotID: item.BotID, Key: fmt.Sprintf("panel-cleanup:%d", item.ID), ChatID: item.ChatID, TelegramUserID: item.TelegramUserID, UserID: item.UserID, PayloadCipher: cipher, Purpose: "cleanup", Operation: "delete", TargetMessageID: item.PreviousMessageID, PreviousMessageID: item.PreviousMessageID, Priority: 1, Status: "pending", AvailableAt: time.Now()})
}
