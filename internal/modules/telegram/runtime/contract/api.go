package contract

import "context"

// API decouples the runtime from the HTTP implementation and the test server.
type API interface {
	Identity(context.Context, string) (BotIdentity, error)
	Webhook(context.Context, string) (string, error)
	Poll(context.Context, string, int64) ([]Update, error)
	Send(context.Context, string, Message) (int64, error)
	Answer(context.Context, string, string, string) error
}

// BotProfile is the Telegram-facing identity for one bot instance.
type BotProfile struct {
	Name            string
	SyncName        bool
	Descriptions    map[string]string // locale (zh-CN/en-US/...) -> description
	SyncDescription bool
	CoverJPEG       []byte // nil keeps the current photo unless ClearCover
	CoverMP4        []byte // animated photo; takes precedence over CoverJPEG
	MainFrameSecond float64
	ClearCover      bool
}

// ProfileAPI is optional; notification-only adapters need not implement it.
type ProfileAPI interface {
	ApplyBotProfile(context.Context, string, BotProfile) error
}
type BotIdentity struct {
	ID       int64
	Username string
}
type Update struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	ChatID       int64  `json:"chat_id"`
	Private      bool   `json:"private"`
	Text         string `json:"text"`
	MessageID    int64  `json:"message_id,omitempty"`
	MessageKind  string `json:"message_kind,omitempty"`
	CallbackID   string `json:"callback_id,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
	Locale       string `json:"locale,omitempty"`
	Username     string `json:"username,omitempty"`
}
type Button struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
	// Style is "primary", "success" or "danger"; older clients ignore it.
	Style string `json:"style,omitempty"`
}
type Message struct {
	Operation       string     `json:"operation,omitempty"`
	Purpose         string     `json:"purpose,omitempty"`
	TargetMessageID int64      `json:"target_message_id,omitempty"`
	TargetKind      string     `json:"target_kind,omitempty"`
	PanelRevision   uint64     `json:"panel_revision,omitempty"`
	PanelEpoch      string     `json:"panel_epoch,omitempty"`
	ParseMode       string     `json:"parse_mode,omitempty"`
	FileID          string     `json:"file_id,omitempty"`
	MediaName       string     `json:"media_name,omitempty"`
	ForceDocument   bool       `json:"force_document,omitempty"`
	ReplacePanel    bool       `json:"replace_panel,omitempty"`
	OwnerUserID     uint       `json:"owner_user_id,omitempty"`
	IdentityStamp   string     `json:"identity_stamp,omitempty"`
	ChatID          int64      `json:"chat_id"`
	Text            string     `json:"text"`
	Buttons         [][]Button `json:"buttons,omitempty"`
	Photo           []byte     `json:"photo,omitempty"`
	PhotoURL        string     `json:"photo_url,omitempty"`
}
type DeliveryResult struct {
	MessageID   int64
	FileID      string
	MessageKind string
}

// ApplyingAPI is optional so existing notification senders remain compatible.
type ApplyingAPI interface {
	Apply(context.Context, string, Message) (DeliveryResult, error)
}

type callbackResponderKey struct{}

// WithCallbackResponder gives handlers a narrow, once-only feedback capability.
func WithCallbackResponder(ctx context.Context, answer func(string)) context.Context {
	return context.WithValue(ctx, callbackResponderKey{}, answer)
}
func AnswerCallback(ctx context.Context, text string) {
	if answer, ok := ctx.Value(callbackResponderKey{}).(func(string)); ok {
		answer(text)
	}
}

type APIError interface {
	error
	StatusCode() int
	RetrySeconds() int
	Uncertain() bool
}
type Lease interface {
	Renew(context.Context) error
	Release(context.Context) error
}
type Leaser interface {
	Acquire(context.Context, string) (Lease, error)
}

// Handler must be idempotent for Update.ID; business writes use separate stable keys.
type Handler interface {
	Handle(context.Context, uint, Update) ([]Message, error)
	AuthorizeDelivery(context.Context, uint, int64, uint) error
}
