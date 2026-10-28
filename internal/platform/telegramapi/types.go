// Package telegramapi provides a bounded, context-aware Telegram Bot API client.
package telegramapi

// User is a Telegram user or bot. Telegram identifiers require 64-bit storage.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type Message struct {
	MessageID      int64                 `json:"message_id"`
	Date           int64                 `json:"date"`
	From           *User                 `json:"from,omitempty"`
	Chat           Chat                  `json:"chat"`
	Text           string                `json:"text,omitempty"`
	Caption        string                `json:"caption,omitempty"`
	Entities       []MessageEntity       `json:"entities,omitempty"`
	ReplyToMessage *Message              `json:"reply_to_message,omitempty"`
	ReplyMarkup    *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// MessageEntity offsets and lengths count UTF-16 code units, not Go bytes.
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

type CallbackQuery struct {
	ID              string   `json:"id"`
	From            User     `json:"from"`
	Message         *Message `json:"message,omitempty"`
	InlineMessageID string   `json:"inline_message_id,omitempty"`
	ChatInstance    string   `json:"chat_instance,omitempty"`
	Data            string   `json:"data,omitempty"`
	GameShortName   string   `json:"game_short_name,omitempty"`
}

type Update struct {
	UpdateID          int64          `json:"update_id"`
	Message           *Message       `json:"message,omitempty"`
	EditedMessage     *Message       `json:"edited_message,omitempty"`
	ChannelPost       *Message       `json:"channel_post,omitempty"`
	EditedChannelPost *Message       `json:"edited_channel_post,omitempty"`
	CallbackQuery     *CallbackQuery `json:"callback_query,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}

type WebhookInfo struct {
	URL                          string   `json:"url"`
	HasCustomCertificate         bool     `json:"has_custom_certificate"`
	PendingUpdateCount           int      `json:"pending_update_count"`
	IPAddress                    string   `json:"ip_address,omitempty"`
	LastErrorDate                int64    `json:"last_error_date,omitempty"`
	LastErrorMessage             string   `json:"last_error_message,omitempty"`
	LastSynchronizationErrorDate int64    `json:"last_synchronization_error_date,omitempty"`
	MaxConnections               int      `json:"max_connections,omitempty"`
	AllowedUpdates               []string `json:"allowed_updates,omitempty"`
}

type GetUpdatesRequest struct {
	Offset int64 `json:"offset,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	// Timeout is the server-side long-poll duration in seconds (0..50).
	Timeout int `json:"timeout,omitempty"`
	// Nil preserves Telegram's previous filter; a non-nil empty slice resets it.
	AllowedUpdates *[]string `json:"allowed_updates,omitempty"`
}

type SendMessageRequest struct {
	ChatID              int64                 `json:"chat_id"`
	Text                string                `json:"text"`
	ParseMode           string                `json:"parse_mode,omitempty"`
	DisableNotification bool                  `json:"disable_notification,omitempty"`
	ProtectContent      bool                  `json:"protect_content,omitempty"`
	ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type SendPhotoRequest struct {
	ChatID              int64
	PNG                 []byte
	Caption             string
	ParseMode           string
	DisableNotification bool
	ProtectContent      bool
	ReplyMarkup         *InlineKeyboardMarkup
}

type SendDocumentRequest struct {
	ChatID              int64
	Filename            string
	Data                []byte
	Caption             string
	ParseMode           string
	DisableNotification bool
	ProtectContent      bool
	ReplyMarkup         *InlineKeyboardMarkup
}

type AnswerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
	URL             string `json:"url,omitempty"`
	CacheTime       int    `json:"cache_time,omitempty"`
}
