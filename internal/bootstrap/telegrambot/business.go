package telegrambot

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	message := runtime.Message{ChatID: event.PrivateChatID, OwnerUserID: event.UserID}
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
		message.Text = fmt.Sprintf(selectText("订单 %s 已付款，发货进度可在「我的订单」查看。", "訂單 %s 已付款，發貨進度可在「我的訂單」查看。", "Order %s is paid. Check My orders for delivery."), order.OrderNo)
	case events.EventOrderFulfilled:
		order, err := r.shop.d.Orders.GetOrderByUser(event.OrderID, event.UserID)
		if err != nil {
			return message, err
		}
		view := orderView(order)
		if len(view.Delivered) == 0 {
			return message, fmt.Errorf("fulfillment is not ready")
		}
		message.Text = fmt.Sprintf(selectText("订单 %s\n%s", "訂單 %s\n%s", "Order %s\n%s"), order.OrderNo, strings.Join(view.Delivered, "\n\n"))
	case events.EventRechargeSucceeded:
		message.Text = selectText("账户充值已到账，请查看账户余额。", "帳戶儲值已到帳，請查看帳戶餘額。", "Your recharge is credited. Check your account balance.")
	default:
		return message, fmt.Errorf("unsupported business event")
	}
	return message, nil
}

type messageEnqueuer struct{ store runtime.Store }

func (q messageEnqueuer) EnqueueMessage(ctx context.Context, botID uint, key string, telegramID int64, userID uint, cipher string) error {
	return q.store.Enqueue(ctx, &rd.Delivery{BotID: botID, Key: key, ChatID: telegramID, TelegramUserID: telegramID, UserID: userID, PayloadCipher: cipher, Priority: 200, Status: "pending", AvailableAt: time.Now()})
}
func NewBusinessDispatcher(store events.DispatcherStore, d ShopDependencies, guard runtime.Handler, outbox runtime.Store) *deliveryapp.Dispatcher {
	return &deliveryapp.Dispatcher{Store: store, Renderer: businessRenderer{shop: &shopAdapter{d: d}, guard: guard, store: outbox}, Outbox: messageEnqueuer{store: outbox}, Secret: d.Secret}
}
