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
	IsForum   bool   `json:"is_forum,omitempty"`
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type Message struct {
	ForumTopicClosed    *ForumTopicClosed     `json:"forum_topic_closed,omitempty"`
	ForumTopicReopened  *ForumTopicReopened   `json:"forum_topic_reopened,omitempty"`
	HasProtectedContent bool                  `json:"has_protected_content,omitempty"`
	MessageThreadID     int64                 `json:"message_thread_id,omitempty"`
	IsTopicMessage      bool                  `json:"is_topic_message,omitempty"`
	SenderChat          *Chat                 `json:"sender_chat,omitempty"`
	CaptionEntities     []MessageEntity       `json:"caption_entities,omitempty"`
	MediaGroupID        string                `json:"media_group_id,omitempty"`
	Video               *Video                `json:"video,omitempty"`
	Voice               *Voice                `json:"voice,omitempty"`
	Audio               *Audio                `json:"audio,omitempty"`
	Animation           *Video                `json:"animation,omitempty"`
	VideoNote           *VideoNote            `json:"video_note,omitempty"`
	Sticker             *Sticker              `json:"sticker,omitempty"`
	MigrateToChatID     int64                 `json:"migrate_to_chat_id,omitempty"`
	MigrateFromChatID   int64                 `json:"migrate_from_chat_id,omitempty"`
	MessageID           int64                 `json:"message_id"`
	Date                int64                 `json:"date"`
	From                *User                 `json:"from,omitempty"`
	Chat                Chat                  `json:"chat"`
	Text                string                `json:"text,omitempty"`
	Caption             string                `json:"caption,omitempty"`
	Photo               []PhotoSize           `json:"photo,omitempty"`
	Document            *Document             `json:"document,omitempty"`
	Entities            []MessageEntity       `json:"entities,omitempty"`
	ReplyToMessage      *Message              `json:"reply_to_message,omitempty"`
	ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// ForumTopicClosed and ForumTopicReopened are empty Bot API service payloads.
// Pointer presence distinguishes a real service message from a normal message.
// Telegram does not publish a forum_topic_deleted service message.
type ForumTopicClosed struct{}
type ForumTopicReopened struct{}

// MessageEntity offsets and lengths count UTF-16 code units, not Go bytes.
type MessageEntity struct {
	Language      string `json:"language,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	URL           string `json:"url,omitempty"`
	User          *User  `json:"user,omitempty"`
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
	MyChatMember      *ChatMemberUpdated `json:"my_chat_member,omitempty"`
	ChatMember        *ChatMemberUpdated `json:"chat_member,omitempty"`
	ChatJoinRequest   *ChatJoinRequest   `json:"chat_join_request,omitempty"`
	UpdateID          int64              `json:"update_id"`
	Message           *Message           `json:"message,omitempty"`
	EditedMessage     *Message           `json:"edited_message,omitempty"`
	ChannelPost       *Message           `json:"channel_post,omitempty"`
	EditedChannelPost *Message           `json:"edited_channel_post,omitempty"`
	CallbackQuery     *CallbackQuery     `json:"callback_query,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
	Style        string `json:"style,omitempty"`
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
	ThreadID            int64                 `json:"message_thread_id,omitempty"`
	ChatID              int64                 `json:"chat_id"`
	Text                string                `json:"text"`
	ParseMode           string                `json:"parse_mode,omitempty"`
	DisableNotification bool                  `json:"disable_notification,omitempty"`
	ProtectContent      bool                  `json:"protect_content,omitempty"`
	ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type SendPhotoRequest struct {
	ChatID              int64
	PNG                 []byte // Deprecated: use Data; retained for callers sending PNG.
	Data                []byte
	Filename            string
	FileID              string
	URL                 string
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

// PhotoSize contains the reusable Telegram file identifier for one photo size.
type PhotoSize struct {
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileID       string `json:"file_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Document struct {
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileID       string `json:"file_id"`
}

// PhotoFileID selects the largest returned photo, rather than assuming array order.
func (m Message) PhotoFileID() string {
	var best PhotoSize
	for _, p := range m.Photo {
		if p.FileID != "" && (best.FileID == "" || int64(p.Width)*int64(p.Height) > int64(best.Width)*int64(best.Height) || (p.Width == best.Width && p.Height == best.Height && p.FileSize > best.FileSize)) {
			best = p
		}
	}
	return best.FileID
}

type EditMessageTextRequest struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type EditMessageCaptionRequest struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Caption     string                `json:"caption"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageMediaRequest supports photo replacement, including text to photo.
// URL is passed directly to Telegram; this client never fetches it.
type EditMessageMediaRequest struct {
	ChatID      int64
	MessageID   int64
	Data        []byte
	Filename    string
	FileID      string
	URL         string
	Caption     string
	ParseMode   string
	ReplyMarkup *InlineKeyboardMarkup
}

type EditMessageReplyMarkupRequest struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup"`
}

type DeleteMessageRequest struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

type AnswerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
	URL             string `json:"url,omitempty"`
	CacheTime       int    `json:"cache_time,omitempty"`
}
