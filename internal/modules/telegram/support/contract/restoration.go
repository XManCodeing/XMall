package contract

import (
	"context"
	"regexp"
	"time"
)

var recoveryEvidence = regexp.MustCompile(`^(ticket|audit|case):[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// ValidRecoveryEvidence validates an opaque audit reference using the shared
// backend recovery rule; whitespace, URLs and free-form assertions are rejected.
func ValidRecoveryEvidence(evidence string) bool { return recoveryEvidence.MatchString(evidence) }

// SupportRestoration identifies a task-owned route replacement. This is an
// internal orchestration contract, never a receipt-bearing admin request.
// Both runtime snapshots must be fenced and genuinely drained before any write.
type SupportRestoration struct {
	TaskID, ActorID     uint
	Source, Destination RestorationRoute
	Evidence            string
}

// RestorationRoute pins the complete runtime and logical/physical route tuple.
// Destination may be disabled with generation zero; task authority, not running
// state, permits recovery topic creation. No private reachability is implied.
type RestorationRoute struct {
	BotID, TargetID                   uint
	Version, Generation, RouteVersion uint64
	ChatID                            int64
}

// RestorationGuard runs on the caller's transaction connection. Implementations
// must check task ownership/authorization, both actual runtime drain proofs and
// exact switched destination route/capability freshness while fences remain held.
// An absent adapter fails closed; stopped state alone is never a drain proof.
type RestorationGuard interface {
	CheckSupportRestoration(context.Context, SupportRestoration, time.Time) error
}

// RestorationStore scans at most 100 sessions and persists CAS progress. Topic
// claims use the existing topic operation/mapping machinery. Unknown outcomes
// and prior deliveries are retained, never replayed or adjudicated by recovery.
type RestorationStore interface {
	BeginSupportRestoration(context.Context, SupportRestoration, time.Time) (RestorationProgress, error)
	AdvanceSupportRestoration(context.Context, uint, uint64, int, time.Time, ...uint) (RestorationProgress, error)
	SupportRestorationProgress(context.Context, uint, ...uint) (RestorationProgress, error)
	ClaimRestorationTopic(context.Context, uint, string, time.Time, ...uint) (RestorationTopic, error)
	FinishRestorationTopic(context.Context, uint, uint, string, TopicResult, time.Time) error
	AuthorizeRestorationTopic(context.Context, RestorationTopic, time.Time) error
	ResolveRestorationTopic(context.Context, uint, uint, Resolution, time.Time) error
	ReconcileRestorationLegacy(context.Context, uint, uint, uint, Resolution, time.Time) error
}

// RestorationProgress distinguishes mapped histories from fresh-consultation
// deferrals and legacy work. Scanned is not equivalent to restored or complete.
type RestorationProgress struct {
	TaskID, TargetID                                         uint
	Revision                                                 uint64
	Cursor, UpperBound                                       uint
	Scanned                                                  bool
	Pending, Restored, AwaitingConsultation, Legacy, Unknown uint64
}

// RestorationTopic carries only a trusted new topic intent, never message/file
// IDs from the old bot. Execute false means no external retry is authorized.
type RestorationTopic struct {
	Plan    SupportRestoration
	EntryID uint
	Claim   TopicClaim
}

// RestorationPlatform executes only a task-authorized forum operation outside
// transactions. It must recheck persisted task/route/fences immediately before
// HTTP and use the ordinary forum API/error classification, with no HTTP retry.
// Disabled/fenced destination runtime support must be explicit, not an invented
// live ExecutionFence or temporary enabling of the normal runtime.
type RestorationPlatform interface {
	CreateRestorationTopic(context.Context, RestorationTopic) (TopicResult, error)
}
