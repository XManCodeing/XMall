package contract

import (
	"context"
	"time"
)

// AdminDeliveryRecoveryInput accepts only a business CAS and an audit reference.
// No runtime state, receipt, generation, destination or source can be supplied.
type AdminDeliveryRecoveryInput struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Evidence        string `json:"evidence"`
}

// AdminDeliveryRecovery reports persisted evidence, not a claim of platform lookup.
// ManualReview remains true whenever runtime cannot prove the external outcome.
type AdminDeliveryRecovery struct {
	ID             uint   `json:"id"`
	Version        uint64 `json:"version"`
	State          string `json:"state"`
	DeliveryID     uint   `json:"delivery_id"`
	RuntimeState   string `json:"runtime_state"`
	EvidenceSource string `json:"evidence_source"`
	ManualReview   bool   `json:"manual_review"`
	RetryQueued    bool   `json:"retry_queued"`
}

// AdminDeliveryRecoverer atomically checks/requeues a business task and audits CAS.
// Direction is upstream/downstream; action is reconcile/retry. Actor/generation
// are trusted application values. Errors roll back business and transport changes.
type AdminDeliveryRecoverer interface {
	RecoverDelivery(context.Context, uint, uint, uint64, uint, string, string, AdminDeliveryRecoveryInput, time.Time) (AdminDeliveryRecovery, error)
}

// AdminRecoveryAuthorize checks the exact POST path for a real backend actor.
type AdminRecoveryAuthorize func(context.Context, uint, string) error
