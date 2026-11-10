// Package botreplacement owns cross-Bot recovery under persistent runtime and target fences.
// Every local phase is replayable; module proofs and live permissions gate destination release.
package botreplacement

import (
	"context"
	"errors"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"strings"
	"time"
)

// ErrContinuationRequired explicitly distinguishes safe preparation from completed migration.
var ErrContinuationRequired = errors.New("recovery: cross-bot route and module continuation required")

// Input contains administrator intent only; all identities and routing scope come from storage.
type Input struct {
	SourceBotID      uint               `json:"source_bot_id,string"`
	DestinationBotID uint               `json:"destination_bot_id,string"`
	RequestKey       string             `json:"request_key"`
	Reason           string             `json:"reason"`
	Routes           []RouteDestination `json:"routes,omitempty"`
}

// RouteDestination contains administrator intent only; versions and source identity are server-derived.
type RouteDestination struct {
	TargetID uint  `json:"target_id,string"`
	ChatID   int64 `json:"chat_id,string"`
}

// RoutedBackend resolves optional destination chats within the same snapshot transaction.
// Implementations must reject duplicate, occupied or out-of-scope destinations.
type RoutedBackend interface {
	SnapshotRoutes(context.Context, uint, uint, []RouteDestination) (d.Snapshot, error)
}

// Backend provides atomic scope confirmation/fencing and local drain checkpoints.
// Snapshot and Confirm must use consistent database reads, never platform HTTP.
type Backend interface {
	Snapshot(context.Context, uint, uint) (d.Snapshot, error)
	Confirm(context.Context, uint, c.Confirmation) (*d.Task, error)
	Checkpoint(context.Context, uint, uint64) (*d.Task, error)
}

// Drainer is the existing runtime stop-and-join capability, not a timeout-based substitute.
type Drainer interface {
	BlockAndDrainRecovery(context.Context, runtime.RecoveryFence) error
}

// Service composes exact action authorization, existing task storage and runtime drain.
type Service struct {
	Continuation Continuation
	Probe        groups.RecoveryProber
	Modules      Modules
	Store        c.Store
	Backend      Backend
	Drain        Drainer
	Authorize    func(context.Context, uint, string) error
}

// authorize validates assembly and checks the fixed action for actor on every invocation.
func (service *Service) authorize(ctx context.Context, actor uint, action string) error {
	if service == nil || service.Store == nil || service.Backend == nil || service.Authorize == nil || actor == 0 {
		return c.ErrInvalid
	}
	return service.Authorize(ctx, actor, action)
}

// Preview persists a bounded, database-derived preflight; it neither stops bots nor sends HTTP.
// actor is authenticated by the transport, input never accepts Telegram identities or versions.
func (service *Service) Preview(ctx context.Context, actor uint, input Input) (*d.Task, error) {
	if err := service.authorize(ctx, actor, "bot-preflight"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 512 {
		return nil, c.ErrInvalid
	}
	var snapshot d.Snapshot
	var err error
	if len(input.Routes) == 0 {
		snapshot, err = service.Backend.Snapshot(ctx, input.SourceBotID, input.DestinationBotID)
	} else {
		routed, ok := service.Backend.(RoutedBackend)
		if !ok || len(input.Routes) > 100 {
			return nil, c.ErrInvalid
		}
		snapshot, err = routed.SnapshotRoutes(ctx, input.SourceBotID, input.DestinationBotID, input.Routes)
	}
	if err != nil {
		return nil, err
	}
	return service.Store.CreatePreflight(ctx, c.PreflightInput{Kind: d.BotReplace, RequestKey: input.RequestKey, Reason: strings.TrimSpace(input.Reason), ActorID: actor, Snapshot: snapshot, TTL: 5 * time.Minute})
}

// ExecutionBlocker reports missing execution dependencies without performing work.
// The result describes assembly only, not current Telegram permissions or completion.
func (service *Service) ExecutionBlocker() string {
	if service == nil || service.Store == nil || service.Backend == nil || service.Authorize == nil {
		return "execution_unavailable"
	}
	if service.Drain == nil {
		return "runtime_drain_unavailable"
	}
	if service.Continuation == nil || service.Probe == nil || service.Modules == nil {
		return "route_and_module_continuation_required"
	}
	return ""
}

// Confirm authorizes before replay and atomically binds the exact task/ref to both runtime fences.
// First confirmation requires executable assembly; exact replays retain backend CAS checks.
// No HTTP is executed; failed CAS rolls back task, scope slots and both fence writes.
func (service *Service) Confirm(ctx context.Context, id uint, input c.Confirmation) (*d.Task, error) {
	if err := service.authorize(ctx, input.ActorID, "confirm"); err != nil {
		return nil, err
	}
	detail, err := service.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail.Task.Kind != d.BotReplace || detail.Task.PreflightRef != input.PreflightRef {
		return nil, c.ErrStale
	}
	// The backend verifies original confirmation actor/version, including after expiry.
	// Missing dependencies must not prevent reading the result of an accepted request.
	if detail.Task.ConfirmedAt == nil {
		switch service.ExecutionBlocker() {
		case "runtime_drain_unavailable":
			return nil, runtime.ErrRecoveryBlocked
		case "route_and_module_continuation_required":
			return nil, ErrContinuationRequired
		case "execution_unavailable":
			return nil, c.ErrInvalid
		}
	}
	return service.Backend.Confirm(ctx, id, input)
}

// Resume waits for real exit proofs outside transactions and checkpoints their verification.
// Missing proofs leave fencing; subsequent phases retain fences until every module proves completion.
func (service *Service) Resume(ctx context.Context, id uint, version uint64, actor uint) (*d.Task, error) {
	if err := service.authorize(ctx, actor, "resume"); err != nil {
		return nil, err
	}
	detail, err := service.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail.Task.Kind == d.BotReplace && detail.Task.State == d.Completed && detail.Task.Version == version {
		return &detail.Task, nil
	}
	if detail.Task.Kind != d.BotReplace || detail.Task.ConfirmedAt == nil || detail.Task.Version != version || (detail.Task.State != d.Fencing && detail.Task.State != d.Switching && detail.Task.State != d.Recovering) {
		return nil, c.ErrStale
	}
	if service.Drain == nil {
		return &detail.Task, runtime.ErrRecoveryBlocked
	}
	snapshot := detail.Snapshot
	for _, fence := range []runtime.RecoveryFence{
		{BotID: snapshot.Old.BotID, TaskID: id, Version: snapshot.ExpectedOld.Config, Generation: snapshot.ExpectedOld.Generation},
		{BotID: snapshot.New.BotID, TaskID: id, Version: snapshot.ExpectedNew.Config, Generation: snapshot.ExpectedNew.Generation},
	} {
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = service.Drain.BlockAndDrainRecovery(waitCtx, fence)
		cancel()
		if err != nil {
			return &detail.Task, err
		}
	}
	// Revocation during a potentially slow drain prevents advancement, but never removes fences.
	if err := service.authorize(ctx, actor, "resume"); err != nil {
		return &detail.Task, err
	}
	task := &detail.Task
	if task.State == d.Fencing {
		task, err = service.Backend.Checkpoint(ctx, id, version)
		if err != nil {
			return task, err
		}
	}
	if service.Continuation == nil || service.Probe == nil {
		return task, ErrContinuationRequired
	}
	return service.continueRecovery(ctx, task, detail.Snapshot, actor)
}
