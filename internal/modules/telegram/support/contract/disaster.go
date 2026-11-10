package contract

import (
	"context"
	"time"
)

// DisasterContinuation is deliberately local-only: retire every source-bot support
// topic and close its sessions, then require fresh private consultations. It does
// not rebuild topics, replay deliveries, move payloads, or grant reachability.
// The authorized orchestrator MUST stop and drain the source runtime before Begin
// and keep it stopped until LocalComplete; no in-flight HTTP can be cancelled here.
// Before restarting the same bot it MUST install a fresh support route revision.
// For another bot, users must independently start/verify with that bot. Existing
// delivery reconciliation remains owned by runtime; retained work is NOT restored.
type DisasterContinuation interface {
	BeginDisasterContinuation(context.Context, DisasterContinuationInput, time.Time) (DisasterProgress, error)
	AdvanceDisasterContinuation(context.Context, string, uint64, int, time.Time) (DisasterProgress, error)
	DisasterContinuationProgress(context.Context, string) (DisasterProgress, error)
}

// DisasterContinuationInput is a trusted orchestration contract, not an HTTP DTO.
// Key is an immutable idempotency key (1..64 ASCII identifier characters); actor
// and ticket/audit/case evidence must already be authorized by the caller. Scope
// is ALL support data of SourceBotID, not an individual group/session. Source
// generation pins a stopped, disabled or enabled internal instance. Destination
// is informational only; no destination records or platform identifiers migrate.
type DisasterContinuationInput struct {
	Key                           string
	SourceBotID, DestinationBotID uint
	SourceGeneration              uint64
	ActorID                       uint
	Evidence                      string
}

// DisasterProgress reports a bounded snapshot, never overall disaster completion.
// Revision is CAS; retry after a lost response by reading progress. LocalComplete
// only means all snapshot rows were isolated. RetainedMessages/Replies count
// unfinished rows left in place (including unknown); they are not replayed or
// declared delivered. New consultation may still be blocked by retained unknown
// messages on a same-bot session; resolving retired-route delivery evidence is
// outside this contract and current-route recovery APIs may reject that evidence.
// Such sessions remain legacy/manual work, never reported as restored.
// No message_id/file_id, customer identity or reachability is exposed or copied.
type DisasterProgress struct {
	Key                                                string
	Revision                                           uint64
	Phase                                              string
	Cursor, UpperBound                                 uint
	LocalComplete                                      bool
	RetiredTopics, ClosedSessions, CancelledMessages   uint64
	RetainedMessages, RetainedReplies, UncertainTopics uint64
	ContinuationPolicy                                 string
}
