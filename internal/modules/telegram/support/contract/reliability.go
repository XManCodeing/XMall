package contract

import (
	"context"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"time"
)

// RouteReader checks the effective configured route and current capabilities.
// Infrastructure binds this reader to the SAME transaction connection. It must
// not perform HTTP or query a global DB; missing wiring is fail-closed.
type RouteReader interface {
	CheckSupportRoute(context.Context, uint, uint, DeliveryBinding, time.Time) error
}

// TopicClaim returns Execute only to the worker that acquired the durable CAS.
// Persist first, commit, then invoke Telegram outside the database transaction.
type TopicClaim struct {
	Operation d.TopicOperation
	Mapping   *d.TopicMapping
	Execute   bool
}

// TopicResult describes a definite success/failure or unknown external outcome.
// Failed means proven no effect, not timeout or a transport disconnection.
type TopicResult struct {
	State    string
	ThreadID int64
	// RetryAfterSeconds is populated only for a definite 429 rejection.
	RetryAfterSeconds int64
}

// TopicDeletionConfirmation names one exact physical mapping and its CAS revision.
// Only a separately authorized administrator may confirm externally verified deletion.
type TopicDeletionConfirmation struct {
	OperationID      uint
	OperationVersion uint64
	Generation       uint64
	ActorID          uint
	ExpectedVersion  uint64
	ChatID           int64
	ThreadID         int64
	RouteVersion     uint64
	Evidence         string
}

// TopicDeletions invalidates a proven-deleted mapping; it never executes Telegram APIs.
type TopicDeletions interface {
	ConfirmTopicDeleted(context.Context, uint, uint, TopicDeletionConfirmation, time.Time) error
}

// Resolution is accepted only behind an administrator authorization boundary.
// Evidence must be a non-sensitive audit reference, never a token or raw body.
type Resolution struct {
	// Generation is populated by the backend for administrative topic resolution only.
	Generation      uint64
	ActorID         uint
	ExpectedVersion uint64
	Decision        string
	Evidence        string
	ThreadID        int64
	DeliveryID      uint
}

// Topics exposes persistent topic operations without exposing HTTP or DB handles.
type Topics interface {
	ClaimTopic(context.Context, uint, uint, uint, uint64, uint64, int64, string, time.Time, time.Duration) (TopicClaim, error)
	FinishTopic(context.Context, uint, uint, uint64, string, TopicResult, time.Time) error
	BeginTopicAction(context.Context, uint, uint, uint64, uint64, string, string, time.Time, time.Duration) (TopicClaim, error)
	ResolveTopic(context.Context, uint, uint, Resolution, time.Time) error
}

// TopicEvent binds a native runtime update to the current resolved support route.
// It is constructed only by the supervised receiver, never from customer text.
type TopicEvent struct {
	BotID        uint
	TargetID     uint
	Generation   uint64
	RouteVersion uint64
	Update       r.Update
}

// TopicEvents applies observations to existing physical mappings only. It never
// creates a topic or resolves an unknown create from an uncorrelated thread ID.
type TopicEvents interface {
	ObserveTopic(context.Context, TopicEvent, time.Time) error
}

// TopicRecovery bounds local lease expiry independently of incoming consultations.
// It never authorizes an external retry or guesses a physical topic identity.
type TopicRecovery interface {
	ExpireTopicClaims(context.Context, uint, uint64, time.Time, int) error
}

// PreparedReady contains the original trusted update, not caller-selected text.
// Build an envelope from Source and Binding, then PrepareDispatch freezes it.
type PreparedReady struct {
	Message d.BufferedMessage
	Binding DeliveryBinding
	Source  r.Update
	Sources []r.Update // ordered bounded album sources; nil for a single message
}

// Dispatches is the optional B3b recovery contract. Call ScanReady with a bounded
// ID cursor; for sending/unknown call ReconcileReady before attempting any claim.
// PrepareDispatch is durable before enqueue; PendingDispatch reconstructs the
// SAME runtime request after a crash. Enqueue through runtime, never send here.
type Dispatches interface {
	ScanReady(context.Context, uint, uint, int) ([]d.BufferedMessage, error)
	LoadReady(context.Context, uint, uint, uint64, string, time.Time) (PreparedReady, error)
	PrepareDispatch(context.Context, uint, uint, uint64, string, r.Message, time.Time) (r.DeliveryRequest, error)
	PendingDispatch(context.Context, uint, uint, uint64, time.Time) (*r.DeliveryRequest, error)
	ReconcileReady(context.Context, uint, uint, uint64, time.Time) (string, error)
	ResolveReady(context.Context, uint, uint, uint64, Resolution, time.Time) error
}

// NativeReplies resolves explicit registered replies or unique open topic routes.
// Topic-only results have ID zero; callers must not fabricate persistent mappings.
// Callers MUST separately check current group membership for the observed actor on every
// outbound reply; this interface deliberately does not send or grant staff roles.
type NativeReplies interface {
	ResolveNativeReply(context.Context, uint, r.Update, time.Time) (*d.ReplyMapping, error)
	RegisterReply(context.Context, uint, uint, uint64, time.Time) error
}
