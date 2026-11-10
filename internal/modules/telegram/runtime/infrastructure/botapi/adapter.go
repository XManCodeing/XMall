package botapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"

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
func (a Adapter) Poll(ctx context.Context, token string, offset int64) ([]c.Update, error) {
	return a.PollWithSubscription(ctx, token, offset, []string{"message", "callback_query"})
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
	kind        string
}

func (e *sendError) Error() string     { return "telegram delivery failed" }
func (e *sendError) StatusCode() int   { return e.code }
func (e *sendError) RetrySeconds() int { return e.delay }
func (e *sendError) Uncertain() bool   { return e.unknown }
func (e *sendError) Kind() string {
	if e.kind == "" {
		return "unknown"
	}
	return e.kind
}

// Send retains the legacy signature and always sends a new message.
func (a Adapter) Send(ctx context.Context, token string, m c.Message) (int64, error) {
	m.Operation = "send"
	result, err := a.Apply(ctx, token, m)
	return result.MessageID, err
}

func messageKind(m api.Message) string {
	if m.Video != nil {
		return "video"
	}
	if m.Voice != nil {
		return "voice"
	}
	if m.Audio != nil {
		return "audio"
	}
	if m.Animation != nil {
		return "animation"
	}
	if m.VideoNote != nil {
		return "video_note"
	}
	if m.Sticker != nil {
		return "sticker"
	}
	if m.MigrateToChatID != 0 || m.MigrateFromChatID != 0 {
		return "migration"
	}
	if len(m.Photo) > 0 {
		return "photo"
	}
	if m.Document != nil {
		return "document"
	}
	if m.Text != "" {
		return "text"
	}
	return "unknown"
}

// Apply executes exactly one operation. Replacement/fallback belongs to runtime.
func (a Adapter) Apply(ctx context.Context, token string, m c.Message) (c.DeliveryResult, error) {
	if m.Transfer != nil {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "unsupported_target"}
	}
	// Group Apply is restricted to explicitly authorized staff controls; customer
	// content still uses Transfer, never this plain-text path.
	target := m.TargetType.Normalize()
	control := target == c.TargetSupportGroup && m.Purpose == "support_control"
	if control {
		if !validSupportControl(m) {
			return c.DeliveryResult{}, &sendError{code: 400, kind: "unsupported_target"}
		}
	} else if target == c.TargetSupportPrivate {
		if !validSupportGate(m) && !validSupportGateCleanup(m) {
			return c.DeliveryResult{}, &sendError{code: 400, kind: "unsupported_target"}
		}
	} else if target != c.TargetShopPrivate {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "unsupported_target"}
	}
	if m.ThreadID != 0 && !control {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "unsupported_target"}
	}
	if m.TargetKind != "" && m.TargetKind != "text" && m.TargetKind != "photo" && m.TargetKind != "document" {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	client, err := a.client(token)
	if err != nil {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	keyboard := &api.InlineKeyboardMarkup{InlineKeyboard: [][]api.InlineKeyboardButton{}}
	for _, row := range m.Buttons {
		buttons := []api.InlineKeyboardButton{}
		for _, b := range row {
			buttons = append(buttons, api.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data, URL: b.URL, Style: b.Style})
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, buttons)
	}
	operation := m.Operation
	if operation == "" {
		operation = "send"
	}
	if operation != "send" && m.TargetMessageID <= 0 {
		return c.DeliveryResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	hasPhoto := len(m.Photo) > 0 || m.FileID != "" || m.PhotoURL != ""
	var sent api.Message
	kind := m.TargetKind
	switch operation {
	case "send":
		// Preserve legacy protection on all sends; delivery files are explicitly
		// protected too. Never derive protection from markup or parse mode.
		switch {
		case m.ForceDocument || (!hasPhoto && m.Purpose != "panel" && m.Purpose != "broadcast" && len([]rune(m.Text)) > 4000):
			name := m.MediaName
			if name == "" {
				name = "order.txt"
			}
			sent, err = client.SendDocument(ctx, api.SendDocumentRequest{ChatID: m.ChatID, Filename: name, Data: []byte(m.Text), ReplyMarkup: keyboard, ProtectContent: true})
			kind = "document"
		case hasPhoto:
			sent, err = client.SendPhoto(ctx, api.SendPhotoRequest{ChatID: m.ChatID, Data: m.Photo, FileID: m.FileID, URL: m.PhotoURL, Filename: m.MediaName, Caption: m.Text, ParseMode: m.ParseMode, ReplyMarkup: keyboard, ProtectContent: true})
			kind = "photo"
		default:
			sent, err = client.SendMessage(ctx, api.SendMessageRequest{ChatID: m.ChatID, ThreadID: m.ThreadID, Text: m.Text, ParseMode: m.ParseMode, ReplyMarkup: keyboard, ProtectContent: true})
			kind = "text"
		}
	case "edit":
		if m.ForceDocument {
			return c.DeliveryResult{}, &sendError{code: 400, kind: "invalid_content"}
		}
		switch {
		case hasPhoto:
			sent, err = client.EditMessageMedia(ctx, api.EditMessageMediaRequest{ChatID: m.ChatID, MessageID: m.TargetMessageID, Data: m.Photo, FileID: m.FileID, URL: m.PhotoURL, Filename: m.MediaName, Caption: m.Text, ParseMode: m.ParseMode, ReplyMarkup: keyboard})
			kind = "photo"
		case m.TargetKind == "photo":
			sent, err = client.EditMessageCaption(ctx, api.EditMessageCaptionRequest{ChatID: m.ChatID, MessageID: m.TargetMessageID, Caption: m.Text, ParseMode: m.ParseMode, ReplyMarkup: keyboard})
			kind = "photo"
		default:
			sent, err = client.EditMessageText(ctx, api.EditMessageTextRequest{ChatID: m.ChatID, MessageID: m.TargetMessageID, Text: m.Text, ParseMode: m.ParseMode, ReplyMarkup: keyboard})
			kind = "text"
		}
	case "markup":
		// Cleanup is always a clear operation, even if stale buttons were queued.
		sent, err = client.EditMessageReplyMarkup(ctx, api.EditMessageReplyMarkupRequest{ChatID: m.ChatID, MessageID: m.TargetMessageID, ReplyMarkup: &api.InlineKeyboardMarkup{InlineKeyboard: [][]api.InlineKeyboardButton{}}})
	case "delete":
		err = client.DeleteMessage(ctx, api.DeleteMessageRequest{ChatID: m.ChatID, MessageID: m.TargetMessageID})
	default:
		return c.DeliveryResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	if err != nil {
		var upstream *api.APIError
		if errors.As(err, &upstream) {
			if (operation != "send" && upstream.Kind() == "not_modified") || (operation == "delete" && upstream.Kind() == "target_missing") {
				return c.DeliveryResult{MessageID: m.TargetMessageID, FileID: m.FileID, MessageKind: kind}, nil
			}
			code := upstream.ErrorCode
			if code == 0 {
				code = upstream.HTTPStatus
			}
			return c.DeliveryResult{}, &sendError{code: code, delay: upstream.RetryAfter, unknown: operation == "send" && (code >= 500 || upstream.HTTPStatus >= 500), kind: upstream.Kind()}
		}
		var validation *api.ValidationError
		if errors.As(err, &validation) {
			return c.DeliveryResult{}, &sendError{code: 400, kind: validation.Kind()}
		}
		if operation != "send" {
			return c.DeliveryResult{}, &sendError{kind: "retryable"}
		}
		return c.DeliveryResult{}, &sendError{unknown: true}
	}
	if sent.MessageID == 0 && operation != "send" {
		sent.MessageID = m.TargetMessageID
	}
	fileID := sent.PhotoFileID()
	if fileID == "" && kind == "photo" {
		fileID = m.FileID
	}
	return c.DeliveryResult{MessageID: sent.MessageID, FileID: fileID, MessageKind: kind}, nil
}

// ApplyBotProfile pushes display name / description / photo to Telegram for one token.
func (a Adapter) ApplyBotProfile(ctx context.Context, token string, profile c.BotProfile) error {
	client, err := a.client(token)
	if err != nil {
		return err
	}
	step := func(name string, err error) error {
		if err == nil {
			return nil
		}
		attrs := []any{"step", name}
		var upstream *api.APIError
		var validation *api.ValidationError
		switch {
		case errors.As(err, &upstream):
			attrs = append(attrs, "http_status", upstream.HTTPStatus, "error_code", upstream.ErrorCode, "retry_after", upstream.RetryAfter, "kind", upstream.Kind())
		case errors.As(err, &validation):
			attrs = append(attrs, "kind", validation.Kind())
		default:
			attrs = append(attrs, "error", err.Error())
		}
		slog.Warn("telegram_bot_profile_sync_failed", attrs...)
		return err
	}
	if profile.SyncName {
		if err := step("setMyName", client.SetMyName(ctx, api.TruncateRunes(profile.Name, 64), "")); err != nil {
			return err
		}
	}
	if profile.SyncDescription {
		defaultDesc := ""
		for _, locale := range []string{"zh-CN", "en-US", "zh-TW"} {
			if value := strings.TrimSpace(profile.Descriptions[locale]); value != "" {
				defaultDesc = value
				break
			}
		}
		if err := step("setMyDescription", client.SetMyDescription(ctx, api.TruncateRunes(defaultDesc, 512), "")); err != nil {
			return err
		}
		if err := step("setMyShortDescription", client.SetMyShortDescription(ctx, api.TruncateRunes(defaultDesc, 120), "")); err != nil {
			return err
		}
		zh := strings.TrimSpace(profile.Descriptions["zh-CN"])
		if zh == "" {
			zh = strings.TrimSpace(profile.Descriptions["zh-TW"])
		}
		for _, localized := range []struct{ code, value string }{{"en", strings.TrimSpace(profile.Descriptions["en-US"])}, {"zh", zh}} {
			// Empty string removes a stale dedicated translation and falls back to the default.
			value := localized.value
			if value == defaultDesc {
				value = ""
			}
			if err := step("setMyDescription:"+localized.code, client.SetMyDescription(ctx, api.TruncateRunes(value, 512), localized.code)); err != nil {
				return err
			}
			if err := step("setMyShortDescription:"+localized.code, client.SetMyShortDescription(ctx, api.TruncateRunes(value, 120), localized.code)); err != nil {
				return err
			}
		}
	}
	if profile.ClearCover {
		return step("removeMyProfilePhoto", client.RemoveMyProfilePhoto(ctx))
	}
	if len(profile.CoverMP4) > 0 {
		return step("setMyProfilePhoto:animated", client.SetMyAnimatedProfilePhoto(ctx, profile.CoverMP4, profile.MainFrameSecond))
	}
	if len(profile.CoverJPEG) == 0 {
		return nil
	}
	return step("setMyProfilePhoto", client.SetMyProfilePhoto(ctx, profile.CoverJPEG))
}

type Leaser struct{ Client redis.UniversalClient }

func (l Leaser) Acquire(ctx context.Context, key string) (c.Lease, error) {
	return botlease.Acquire(ctx, l.Client, key, 30*time.Second)
}

var _ c.API = Adapter{}
var _ c.ProfileAPI = Adapter{}
var _ c.Leaser = Leaser{}
