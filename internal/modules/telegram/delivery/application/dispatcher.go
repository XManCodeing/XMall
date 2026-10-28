package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	events "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/google/uuid"
)

// Renderer resolves an event's immutable business reference and verifies the
// current recipient authorization; it never allocates inventory or calls a gateway.
type Renderer interface {
	Render(context.Context, events.Event) (runtime.Message, error)
}
type Enqueuer interface {
	EnqueueMessage(context.Context, uint, string, int64, uint, string) error
}
type Dispatcher struct {
	Store    events.DispatcherStore
	Renderer Renderer
	Outbox   Enqueuer
	Secret   string
}

func (d *Dispatcher) Reconcile(ctx context.Context, botID uint) error {
	now := time.Now()
	rows, err := d.Store.ScanBot(botID, now, 100)
	if err != nil {
		return err
	}
	for _, event := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if event.BotID != botID {
			continue
		}
		owner := uuid.NewString()
		ok, err := d.Store.Claim(event.ID, owner, time.Now(), time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		operationCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		message, err := d.Renderer.Render(operationCtx, event)
		if err == nil {
			message.ChatID = event.PrivateChatID
			var raw []byte
			raw, err = json.Marshal(message)
			if err == nil {
				var encrypted string
				encrypted, err = crypto.Encrypt(crypto.DeriveKey(d.Secret), string(raw))
				if err == nil {
					err = d.Outbox.EnqueueMessage(operationCtx, event.BotID, fmt.Sprintf("business:%d", event.ID), event.TelegramID, event.UserID, encrypted)
				}
			}
		}
		cancel()
		if err != nil {
			_ = d.Store.Retry(event.ID, owner, time.Now(), time.Now().Add(time.Minute))
			continue
		}
		// Enqueue uses a unique key: a crash here only repeats the insert, not send.
		if err = d.Store.MarkDelivered(event.ID, owner, time.Now()); err != nil {
			return err
		}
	}
	return nil
}
