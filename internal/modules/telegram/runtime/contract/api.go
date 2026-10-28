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
	CallbackID   string `json:"callback_id,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
	Locale       string `json:"locale,omitempty"`
	Username     string `json:"username,omitempty"`
}
type Button struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}
type Message struct {
	OwnerUserID   uint       `json:"owner_user_id,omitempty"`
	IdentityStamp string     `json:"identity_stamp,omitempty"`
	ChatID        int64      `json:"chat_id"`
	Text          string     `json:"text"`
	Buttons       [][]Button `json:"buttons,omitempty"`
	Photo         []byte     `json:"photo,omitempty"`
	PhotoURL      string     `json:"photo_url,omitempty"`
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
