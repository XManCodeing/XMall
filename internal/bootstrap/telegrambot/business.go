package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/crypto"
	"strings"
	"time"
	"unicode/utf16"

	deliveryapp "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/application"
	events "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	shopcontract "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type businessRenderer struct {
	shop  *shopAdapter
	guard runtime.Handler
	store runtime.Store
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
		message.Text = fmt.Sprintf(selectText("付款成功，感谢支持！\n订单 %s\n正在为你发货，进度可在「我的订单」查看。", "付款成功，感謝支持！\n訂單 %s\n正在為你出貨，進度可在「我的訂單」查看。", "Payment received, thank you!\nOrder %s\nWe are preparing your delivery. Check My orders for progress."), order.OrderNo)
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

type messageEnqueuer struct{ store runtime.Store }

func (q messageEnqueuer) EnqueueMessage(ctx context.Context, botID uint, key string, telegramID int64, userID uint, cipher string) error {
	return q.store.Enqueue(ctx, &rd.Delivery{BotID: botID, Key: key, ChatID: telegramID, TelegramUserID: telegramID, UserID: userID, PayloadCipher: cipher, Priority: 200, Status: "pending", AvailableAt: time.Now()})
}
func NewBusinessDispatcher(store events.DispatcherStore, d ShopDependencies, guard runtime.Handler, outbox runtime.Store) *deliveryapp.Dispatcher {
	return &deliveryapp.Dispatcher{Store: store, Renderer: businessRenderer{shop: &shopAdapter{d: d}, guard: guard, store: outbox}, Outbox: messageEnqueuer{store: outbox}, Secret: d.Secret}
}
