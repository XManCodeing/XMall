package botapi

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/platform/botlease"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"github.com/redis/go-redis/v9"
	"time"
)

type Adapter struct{ Options []api.Option }

func (a Adapter) client(token string) (*api.Client, error) { return api.NewClient(token, a.Options...) }
func (a Adapter) Identity(ctx context.Context, token string) (c.BotIdentity, error) {
	client, err := a.client(token)
	if err != nil {
		return c.BotIdentity{}, err
	}
	u, err := client.GetMe(ctx)
	if err != nil {
		return c.BotIdentity{}, err
	}
	if !u.IsBot {
		return c.BotIdentity{}, errors.New("telegram identity is not a bot")
	}
	return c.BotIdentity{ID: u.ID, Username: u.Username}, nil
}
func (a Adapter) Webhook(ctx context.Context, token string) (string, error) {
	client, err := a.client(token)
	if err != nil {
		return "", err
	}
	w, err := client.GetWebhookInfo(ctx)
	return w.URL, err
}
func (a Adapter) DeleteWebhook(ctx context.Context, token string, dropPending bool) error {
	if dropPending {
		return errors.New("dropping pending updates is not allowed")
	}
	client, err := a.client(token)
	if err != nil {
		return err
	}
	return client.DeleteWebhook(ctx, false)
}
func (a Adapter) Poll(ctx context.Context, token string, offset int64) ([]c.Update, error) {
	client, err := a.client(token)
	if err != nil {
		return nil, err
	}
	allowed := []string{"message", "callback_query"}
	updates, err := client.GetUpdates(ctx, api.GetUpdatesRequest{Offset: offset, Limit: 100, Timeout: 25, AllowedUpdates: &allowed})
	if err != nil {
		return nil, err
	}
	result := make([]c.Update, 0, len(updates))
	for _, raw := range updates {
		u := c.Update{ID: raw.UpdateID}
		message := raw.Message
		var from *api.User
		if message != nil {
			from = message.From
			u.Text = message.Text
		}
		if raw.CallbackQuery != nil {
			q := raw.CallbackQuery
			message = q.Message
			from = &q.From
			u.CallbackID = q.ID
			u.CallbackData = q.Data
		}
		if message != nil {
			u.ChatID = message.Chat.ID
			u.Private = message.Chat.Type == "private"
		}
		if from != nil {
			u.UserID = from.ID
			u.Locale = from.LanguageCode
			u.Username = from.Username
		}
		result = append(result, u)
	}
	return result, nil
}
func (a Adapter) Answer(ctx context.Context, token, id, text string) error {
	client, err := a.client(token)
	if err != nil {
		return err
	}
	return client.AnswerCallbackQuery(ctx, api.AnswerCallbackQueryRequest{CallbackQueryID: id, Text: text})
}

type sendError struct {
	code, delay int
	unknown     bool
}

func (e *sendError) Error() string     { return "telegram delivery failed" }
func (e *sendError) StatusCode() int   { return e.code }
func (e *sendError) RetrySeconds() int { return e.delay }
func (e *sendError) Uncertain() bool   { return e.unknown }
func (a Adapter) Send(ctx context.Context, token string, m c.Message) (int64, error) {
	client, err := a.client(token)
	if err != nil {
		return 0, &sendError{code: 400}
	}
	var keyboard *api.InlineKeyboardMarkup
	if len(m.Buttons) > 0 {
		keyboard = &api.InlineKeyboardMarkup{}
		for _, row := range m.Buttons {
			buttons := []api.InlineKeyboardButton{}
			for _, b := range row {
				buttons = append(buttons, api.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data, URL: b.URL})
			}
			keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, buttons)
		}
	}
	var sent api.Message
	switch {
	case len(m.Photo) > 0:
		sent, err = client.SendPhoto(ctx, api.SendPhotoRequest{ChatID: m.ChatID, PNG: m.Photo, Caption: m.Text, ReplyMarkup: keyboard, ProtectContent: true})
	case len([]rune(m.Text)) > 4000:
		sent, err = client.SendDocument(ctx, api.SendDocumentRequest{ChatID: m.ChatID, Filename: "order.txt", Data: []byte(m.Text), ReplyMarkup: keyboard, ProtectContent: true})
	default:
		sent, err = client.SendMessage(ctx, api.SendMessageRequest{ChatID: m.ChatID, Text: m.Text, ReplyMarkup: keyboard, ProtectContent: true})
	}
	if err != nil {
		var upstream *api.APIError
		if errors.As(err, &upstream) {
			code := upstream.ErrorCode
			if code == 0 {
				code = upstream.HTTPStatus
			}
			return 0, &sendError{code: code, delay: upstream.RetryAfter, unknown: code >= 500}
		}
		return 0, &sendError{unknown: true}
	}
	return sent.MessageID, nil
}

type Leaser struct{ Client redis.UniversalClient }

func (l Leaser) Acquire(ctx context.Context, key string) (c.Lease, error) {
	return botlease.Acquire(ctx, l.Client, key, 30*time.Second)
}

var _ c.API = Adapter{}
var _ c.Leaser = Leaser{}
