package contract

import "context"

// RecoveryReleaseEvidenceRequest binds the original freeze CAS, both immutable
// routes and the confirmed generation. Generation is historical, not the current
// runtime generation. This contract never authorizes a new release.
type RecoveryReleaseEvidenceRequest struct {
	RecoveryInput
	Generation uint64
}

// RecoveryReleaseEvidence contains the persisted original release result. It is
// evidence of that task's completed action only, never of current route readiness.
type RecoveryReleaseEvidence struct {
	ReleasedVersion uint64
	ObservationSeq  uint64
	Generation      uint64
	BotTelegramID   int64
}

// RecoveryReleaseReader reads exact task-bound historical evidence without writes
// or platform calls. Nil evidence means the matching receipt exists but was not
// released. Missing/mismatched/incomplete receipts return errors, never success.
// A later route or task does not invalidate historical evidence and must not be
// mutated when this evidence is used to repair the original parent's step record.
type RecoveryReleaseReader interface {
	ReadRecoveryRelease(context.Context, RecoveryReleaseEvidenceRequest) (*RecoveryReleaseEvidence, error)
}
