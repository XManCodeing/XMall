package contract

import (
	"context"
	"errors"
	"time"
)

const AdminSettingsPath = "/admin/telegram-bot/support/settings/:bot_id"

var ErrPolicyInvalid = errors.New("invalid support policy")

// AdminPolicy uses explicit whole seconds; all six values are required on replacement.
type AdminPolicy struct {
	MaxMessages         int   `json:"max_messages"`
	MaxBytes            int64 `json:"max_bytes"`
	MaxAttempts         int   `json:"max_attempts"`
	RetentionSeconds    int64 `json:"retention_seconds"`
	ChallengeTTLSeconds int64 `json:"challenge_ttl_seconds"`
	CooldownSeconds     int64 `json:"cooldown_seconds"`
}

// Policy converts a bounded wire value to domain durations without integer overflow.
// Policy.Valid remains the single source of validation rules after the overflow check.
func (in AdminPolicy) Policy() (Policy, error) {
	const maxSeconds = int64((1<<63 - 1) / int64(time.Second))
	for _, seconds := range []int64{in.RetentionSeconds, in.ChallengeTTLSeconds, in.CooldownSeconds} {
		if seconds < 0 || seconds > maxSeconds {
			return Policy{}, ErrPolicyInvalid
		}
	}
	policy := Policy{MaxMessages: in.MaxMessages, MaxBytes: in.MaxBytes, MaxAttempts: in.MaxAttempts, Retention: time.Duration(in.RetentionSeconds) * time.Second, ChallengeTTL: time.Duration(in.ChallengeTTLSeconds) * time.Second, Cooldown: time.Duration(in.CooldownSeconds) * time.Second}
	if !policy.Valid() {
		return Policy{}, ErrPolicyInvalid
	}
	return policy, nil
}

// PolicyDTO projects a trusted policy into explicit seconds for clients.
func PolicyDTO(policy Policy) AdminPolicy {
	return AdminPolicy{MaxMessages: policy.MaxMessages, MaxBytes: policy.MaxBytes, MaxAttempts: policy.MaxAttempts, RetentionSeconds: int64(policy.Retention / time.Second), ChallengeTTLSeconds: int64(policy.ChallengeTTL / time.Second), CooldownSeconds: int64(policy.Cooldown / time.Second)}
}

// AdminSettings exposes defaults and the fixed closed production gate, not a configurable enable flag.
type AdminSettings struct {
	BotID            uint        `json:"bot_id"`
	Version          uint64      `json:"version"`
	UsingDefaults    bool        `json:"using_defaults"`
	Policy           AdminPolicy `json:"policy"`
	Defaults         AdminPolicy `json:"defaults"`
	VerificationMode string      `json:"verification_mode"`
	ProductionReady  bool        `json:"production_ready"`
}

// AdminSettingsInput replaces policy at one expected version; zero creates an explicit override.
type AdminSettingsInput struct {
	ExpectedVersion *uint64     `json:"expected_version"`
	Policy          AdminPolicy `json:"policy"`
}

// AdminSettingsAuthorize verifies backend permission for the exact GET/PUT settings route.
type AdminSettingsAuthorize func(context.Context, uint, uint, string) error

const AdminSessionsPath = "/admin/telegram-bot/support/sessions"
const AdminSessionPath = AdminSessionsPath + "/:id"
const AdminMessagesPath = AdminSessionPath + "/messages"
const AdminContentPath = AdminMessagesPath + "/:message_id/content"

const AdminDownstreamsPath = "/admin/telegram-bot/support/downstreams"
const AdminDownstreamPath = AdminDownstreamsPath + "/:id"
const AdminResolutionPath = AdminDownstreamPath + "/resolve"
const AdminTopicsPath = "/admin/telegram-bot/support/topic-operations"
const AdminTopicPath = AdminTopicsPath + "/:id"
const AdminTopicResolutionPath = AdminTopicPath + "/resolve"
const AdminUpstreamsPath = "/admin/telegram-bot/support/upstreams"
const AdminUpstreamPath = AdminUpstreamsPath + "/:id"
const AdminUpstreamResolutionPath = AdminUpstreamPath + "/resolve"

// AdminTopicOperation exposes local operation metadata, never claims or payloads.
type AdminTopicOperation struct {
	Mapping *AdminTopicMapping `json:"mapping,omitempty" gorm:"-"`
	// OperationVersion is the lossless deletion-CAS snapshot; Version retains legacy resolver compatibility.
	OperationVersion string    `json:"operation_version" gorm:"-"`
	ID               uint      `json:"id"`
	BotID            uint      `json:"bot_id"`
	SessionID        uint      `json:"session_id"`
	TargetID         uint      `json:"target_id"`
	RouteVersion     uint64    `json:"route_version"`
	Action           string    `json:"action"`
	State            string    `json:"state"`
	Version          uint64    `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// AdminUpstream uses the buffered-message ID, not a dispatch/downstream/topic ID.
// Version is the ReadyDispatch CAS revision; zero means no dispatch exists yet.
type AdminUpstream struct {
	ID         uint      `json:"id"`
	BotID      uint      `json:"bot_id"`
	SessionID  uint      `json:"session_id"`
	State      string    `json:"state"`
	Version    uint64    `json:"version"`
	DeliveryID uint      `json:"delivery_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AdminTopicResolutionInput confirms an observed result, not an instruction to retry.
// Failed requires explicit no_effect=true and no thread; succeeded requires a thread.
type AdminTopicResolutionInput struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Decision        string `json:"decision"`
	Evidence        string `json:"evidence"`
	ThreadID        int64  `json:"thread_id,string"`
	NoEffect        bool   `json:"no_effect"`
}

// AdminRecoveryStore reads two independent namespaces with whitelisted projections.
type AdminRecoveryStore interface {
	TopicOperations(context.Context, AdminFilter) ([]AdminTopicOperation, int64, error)
	TopicOperation(context.Context, uint) (AdminTopicOperation, error)
	Upstreams(context.Context, AdminFilter) ([]AdminUpstream, int64, error)
	Upstream(context.Context, uint) (AdminUpstream, error)
}

// AdminDownstream is the metadata-only task projection. It contains no Telegram payload or receipt IDs.
type AdminDownstream struct {
	ID         uint      `json:"id"`
	BotID      uint      `json:"bot_id"`
	SessionID  uint      `json:"session_id"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Version    uint64    `json:"version"`
	DeliveryID uint      `json:"delivery_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AdminResolutionInput accepts only CAS, a decision and a non-sensitive evidence reference.
// DeliveryID refers to an existing runtime row; it is never proof by itself.
type AdminResolutionInput struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Decision        string `json:"decision"`
	Evidence        string `json:"evidence"`
	DeliveryID      uint   `json:"delivery_id"`
}

// AdminDownstreamStore reads bounded task metadata without loading source/envelope ciphertext.
type AdminDownstreamStore interface {
	Downstreams(context.Context, AdminFilter) ([]AdminDownstream, int64, error)
	Downstream(context.Context, uint) (AdminDownstream, error)
}

// AdminGeneration loads the current internal running runtime generation from trusted storage.
type AdminGeneration func(context.Context, uint) (uint64, error)

var ErrAdminInvalid = errors.New("invalid support query")
var ErrAdminNotFound = errors.New("support record not found")
var ErrAdminUnavailable = errors.New("support admin dependency unavailable")

// AdminFilter bounds metadata queries; Telegram IDs remain decimal strings on the wire.
type AdminFilter struct {
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	BotID     uint   `json:"bot_id"`
	State     string `json:"state"`
	Unreplied *bool  `json:"unreplied,omitempty"`
	Blocked   *bool  `json:"blocked,omitempty"`
	Failure   string `json:"failure,omitempty"`
	HasTopic  *bool  `json:"has_topic,omitempty"`
}

// ValidSessionOperations validates only finite read-only session filters.
// It accepts empty failure as all; callers retain their pagination/state validation.
func (filter AdminFilter) ValidSessionOperations() bool {
	return filter.Failure == "" || filter.Failure == "any" || filter.Failure == "upstream" || filter.Failure == "downstream" || filter.Failure == "none"
}

// AdminSession is an explicit metadata projection, never a serialized domain model.
type AdminSession struct {
	ID                      uint       `json:"id"`
	BotID                   uint       `json:"bot_id"`
	UserID                  int64      `json:"user_id,string"`
	ChatID                  int64      `json:"chat_id,string"`
	State                   string     `json:"state"`
	Version                 uint64     `json:"version"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	Unreplied               bool       `json:"unreplied"`
	UnrepliedSince          *time.Time `json:"unreplied_since"`
	LastCustomerDeliveredAt *time.Time `json:"last_customer_delivered_at"`
	LastReplyDeliveredAt    *time.Time `json:"last_reply_delivered_at"`
	Blocked                 bool       `json:"blocked"`
	BlockExpiresAt          *time.Time `json:"block_expires_at"`
	UpstreamFailed          int64      `json:"upstream_failed"`
	UpstreamUnknown         int64      `json:"upstream_unknown"`
	DownstreamFailed        int64      `json:"downstream_failed"`
	DownstreamUnknown       int64      `json:"downstream_unknown"`
	TopicChatID             int64      `json:"topic_chat_id,string"`
	TopicThreadID           int64      `json:"topic_thread_id,string"`
	TopicState              string     `json:"topic_state"`
	TopicURL                string     `json:"topic_url" gorm:"-"`
}

// AdminMessage excludes ciphertext, transport references, claims and free-form errors.
type AdminMessage struct {
	ID          uint      `json:"id"`
	SessionID   uint      `json:"session_id"`
	BotID       uint      `json:"bot_id"`
	Sequence    uint64    `json:"sequence"`
	State       string    `json:"state"`
	RetainUntil time.Time `json:"retain_until"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AdminPage always returns an array, including an empty page.
type AdminPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// AdminContent returns only display-safe fields; unavailable content has a finite reason.
type AdminContent struct {
	Available  bool     `json:"available"`
	Reason     string   `json:"reason,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Text       string   `json:"text,omitempty"`
	Caption    string   `json:"caption,omitempty"`
	MediaKinds []string `json:"media_kinds,omitempty"`
}

// AdminPayload is internal-only and cannot accidentally serialize sensitive fields.
type AdminPayload struct {
	Cipher      string    `json:"-" gorm:"column:payload_cipher"`
	RetainUntil time.Time `json:"-"`
}

// AdminReadStore queries only the existing session and buffered-message tables.
type AdminReadStore interface {
	Sessions(context.Context, AdminFilter) ([]AdminSession, int64, error)
	Session(context.Context, uint) (AdminSession, error)
	Messages(context.Context, uint, AdminFilter) ([]AdminMessage, int64, error)
	Payload(context.Context, uint, uint) (AdminPayload, error)
}

// AdminAuthorize must resolve a real backend administrator, not a Telegram identity.
// exact requires an explicit GET policy for the exact route template (or a super admin).
type AdminAuthorize func(ctx context.Context, adminID uint, path string, exact bool) error

// AdminAudit persists only backend actor, local IDs and a finite outcome; failure denies content.
type AdminAudit func(ctx context.Context, adminID, sessionID, messageID uint, outcome string) error
