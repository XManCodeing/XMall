package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// LocalGroupRetry identifies one interrupted database step or read-only probe and
// the current parent/step CAS. ActorID is authenticated by the resume authorizer.
// This grants no success: the original exact-version repository operation must
// still verify its durable receipt or perform its original atomic CAS.
type LocalGroupRetry struct {
	TaskID      uint
	TaskVersion uint64
	StepID      uint
	StepVersion uint64
	ActorID     uint
}

// GroupModuleResumer is an optional bootstrap capability, separate from local retries.
// Implementations must atomically validate module evidence and parent/step CAS.
// Success only reopens the wrapper; normal module execution still owns completion.
type GroupModuleResumer interface {
	ResumeGroupModule(context.Context, LocalGroupRetry) (*d.Task, error)
}

// LocalGroupResumer is optional and excludes module/platform mutations. The only
// platform whitelist entry is the read-only route capability query. Implementations
// verify exact scope, prerequisites and inactive leases before allowing a retry.
type LocalGroupResumer interface {
	RetryLocalGroupStep(context.Context, LocalGroupRetry) (*d.Task, error)
}
