package contract

import (
	"context"
	"time"
)

// PublicReplyRef identifies one exact persisted public shop response.
type PublicReplyRef struct {
	BotID      uint      `json:"bot_id"`
	TelegramID int64     `json:"telegram_id"`
	Generation string    `json:"generation"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// PublicReplyAuthorizer validates persisted content at enqueue, dequeue and send.
type PublicReplyAuthorizer interface {
	AuthorizePublicReply(context.Context, uint, Message) error
}

// ConfirmedPanelCleanup binds cleanup to a successful panel replacement receipt.
type ConfirmedPanelCleanup struct {
	SourceDeliveryID uint `json:"source_delivery_id"`
}

// PanelCleanupStore proves replacement and previous physical panel receipts.
type PanelCleanupStore interface {
	AuthorizePanelCleanup(context.Context, uint, int64, int64, ConfirmedPanelCleanup) error
}

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
type Button struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
	// Style is "primary", "success" or "danger"; older clients ignore it.
	Style string `json:"style,omitempty"`
}
type Message struct {
	JoinAbuseDelivery *JoinAbuseDeliveryRef  `json:"join_abuse_delivery,omitempty"`
	ProactiveDelivery *ProactiveDeliveryRef  `json:"proactive_delivery,omitempty"`
	PanelCleanup      *ConfirmedPanelCleanup `json:"panel_cleanup,omitempty"`
	// PublicReply is an exact, persisted shop reply capability, never an owner bypass.
	PublicReply      *PublicReplyRef      `json:"public_reply,omitempty"`
	ReminderDelivery *ReminderDeliveryRef `json:"reminder_delivery,omitempty"`
	InviteDelivery   *InviteDeliveryRef   `json:"invite_delivery,omitempty"`
	// Transfer is an encrypted support-only envelope; never mix with legacy content.
	Transfer        *TransferRequest `json:"transfer,omitempty"`
	TargetType      TargetType       `json:"target_type,omitempty"`
	BusinessRef     string           `json:"business_ref,omitempty"`
	RouteVersion    uint64           `json:"route_version,omitempty"`
	ThreadID        int64            `json:"thread_id,omitempty"`
	Operation       string           `json:"operation,omitempty"`
	Purpose         string           `json:"purpose,omitempty"`
	TargetMessageID int64            `json:"target_message_id,omitempty"`
	TargetKind      string           `json:"target_kind,omitempty"`
	PanelRevision   uint64           `json:"panel_revision,omitempty"`
	PanelEpoch      string           `json:"panel_epoch,omitempty"`
	ParseMode       string           `json:"parse_mode,omitempty"`
	FileID          string           `json:"file_id,omitempty"`
	MediaName       string           `json:"media_name,omitempty"`
	ForceDocument   bool             `json:"force_document,omitempty"`
	ReplacePanel    bool             `json:"replace_panel,omitempty"`
	OwnerUserID     uint             `json:"owner_user_id,omitempty"`
	IdentityStamp   string           `json:"identity_stamp,omitempty"`
	ChatID          int64            `json:"chat_id"`
	Text            string           `json:"text"`
	Buttons         [][]Button       `json:"buttons,omitempty"`
	Photo           []byte           `json:"photo,omitempty"`
	PhotoURL        string           `json:"photo_url,omitempty"`
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
