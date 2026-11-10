package contract

import (
	"context"
	"errors"
	"fmt"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"time"
)

var ErrDenied = errors.New("support reference or identity denied")
var ErrConflict = errors.New("support concurrent transition; retry")

// These are bounded technical defaults, not confirmed business retention policy.
type Policy struct {
	MaxMessages                       int
	MaxBytes                          int64
	Retention, ChallengeTTL, Cooldown time.Duration
	MaxAttempts                       int
}

func DefaultPolicy() Policy {
	return Policy{20, 512 * 1024, 24 * time.Hour, time.Hour, 5 * time.Minute, 5}
}
func (p Policy) Valid() bool {
	return p.MaxMessages >= 1 && p.MaxMessages <= 100 && p.MaxBytes >= 1024 && p.MaxBytes <= 4*1024*1024 && p.Retention >= time.Minute && p.Retention <= 7*24*time.Hour && p.ChallengeTTL >= time.Minute && p.ChallengeTTL <= time.Hour && p.ChallengeTTL <= p.Retention && p.Cooldown >= time.Minute && p.Cooldown <= time.Hour && p.MaxAttempts >= 1 && p.MaxAttempts <= 10
}

// PolicyStore supplies runtime policy and CAS persistence, separate from group ModuleConfig.
type PolicyStore interface {
	Policy(context.Context, uint) (Policy, uint64, error)
	SavePolicy(context.Context, uint, uint, uint64, Policy, time.Time) (uint64, error)
}

// PolicyReader is the runtime-only read port; database errors must not fall back silently.
type PolicyReader interface {
	Policy(context.Context, uint) (Policy, uint64, error)
}

type Cipher interface {
	Seal(string) (string, error)
	Open(string) (string, error)
}
type Modules interface {
	Modules(context.Context, uint) ([]g.ModuleView, error)
}

// DeliveryCapability reports current route/runtime readiness; absent wiring stays closed.
type DeliveryCapability interface {
	Ready(context.Context, uint, uint) (bool, error)
}
type Outcome struct {
	Session   d.Session
	Challenge *d.Challenge
	Reason    string
}

// VerificationCleanupStore resolves a persisted successful callback for exact-message cleanup.
// The callback carries Telegram's observed message ID even when sendPhoto timed out.
type VerificationCleanupStore interface {
	VerificationCleanup(context.Context, uint, int64) (r.Update, error)
}

type Store interface {
	Accept(context.Context, uint, r.Update, string, int64, string, Policy, time.Time) (Outcome, error)
	Verify(context.Context, uint, r.Update, string, Policy, time.Time) (Outcome, error)
	Challenge(context.Context, string) (*d.Challenge, error)
	Ready(context.Context, uint, uint, int, time.Time) ([]d.BufferedMessage, error)
	Expire(context.Context, uint, time.Time) error
	Notices(context.Context, uint, int, time.Time) ([]Notice, error)
	Notice(context.Context, uint, string) (Notice, error)
	NoticeEnqueued(context.Context, Notice) error
	// CheckNotice rechecks the persisted notice recipient and current support block at send time.
	CheckNotice(context.Context, Notice, time.Time) error
}

// ReadyRejections terminalizes only claimed, definitely unprepared invalid content.
type ReadyRejections interface {
	RejectReady(context.Context, uint, uint, uint64, string, time.Time) error
}

// NoticeDeferral rotates a transiently failing notice behind other pending work.
// Implementations retain the durable pending flag and never discard its payload.
type NoticeDeferral interface {
	DeferNotice(context.Context, Notice, time.Time) error
}

// NoticeQuarantine isolates an invalid durable notice without deleting its audit row.
type NoticeQuarantine interface {
	QuarantineNotice(context.Context, Notice) error
}

// Notice is a projection of a challenge/message's pending business feedback, not
// an additional outbox. The runtime owns delivery attempts and unknown states.
type Notice struct {
	BotID                          uint
	ID                             uint
	Kind, Ref, Reason, Locale, Key string
	UserID, ChatID                 int64
}

// DeliveryBinding pins B3b's intended physical destination before external I/O.
// B3b must derive it from its authorized topic mapping, never from update text.
type DeliveryBinding struct {
	ChatID, ThreadID int64
	RouteVersion     uint64
}

func ReadyBusinessRef(session, message uint) string {
	return fmt.Sprintf("support:ready:%d:%d", session, message)
}

// B3b must enqueue Key == ReadyBusinessRef(sessionID,messageID), with
// TargetSupportGroup, Purpose support_ready, Operation send, TelegramUserID equal
// to the buffered customer, and the same encrypted Message.BusinessRef and pinned
// chat/thread/route. Ack verifies sent + platform MessageID and decrypts this
// persisted envelope. Runtime generation must match an enabled internal running
// instance in the same transaction; a caller-supplied larger number is rejected.
// ReadyConsumer reserves only the oldest unfinished item of a session. BeginSend
// is mandatory before external I/O for legacy consumers. B3b PrepareDispatch
// atomically performs that transition plus durable preparation instead. Expired
// legacy sending leases become unknown; prepared runtime-owned attempts are
// reconciled by Dispatches.ReconcileReady instead of using the business lease.
// Ack accepts delivered, retry (runtime-proven failure), failed or unknown. Retry
// keeps the same runtime row; it does not turn the message into a new ready item.
type ReadyConsumer interface {
	ClaimReady(context.Context, uint, uint, uint64, string, time.Time, time.Duration) (*d.BufferedMessage, error)
	BeginSend(context.Context, uint, uint, uint64, string, DeliveryBinding, time.Time) error
	AckReady(context.Context, uint, uint, uint64, string, string, uint, time.Time) error
}
