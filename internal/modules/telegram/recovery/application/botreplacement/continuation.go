package botreplacement

import (
	"context"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"time"
)

// Continuation provides atomic local checkpoints. Every method rechecks parent CAS and both fences.
// All methods run without HTTP; module adapters receive no raw DB or credential.
type Continuation interface {
	SwitchTarget(context.Context, uint, uint64, uint) error
	MoveModules(context.Context, uint, uint64) (*d.Task, error)
	Observe(context.Context, uint, uint64, groups.RecoveryObservation) error
	RecordModuleProof(context.Context, uint, uint64, ModuleProof) error
	Finalize(context.Context, uint, uint64) (*d.Task, error)
}

// ModuleRequest binds the full immutable inventory and authenticated actor to one recovery task.
// Adapters must verify old/new identity and retain liabilities/unknown outcomes independently.
type ModuleRequest struct {
	TaskID   uint
	ActorID  uint
	Snapshot d.Snapshot
}

// ModuleProof is positive nonempty module evidence, never a bare success boolean.
// Remaining or Unknown must keep the parent fenced, even when LocalComplete is true.
type ModuleProof struct {
	Module        string
	Receipt       string
	LocalComplete bool
	Remaining     uint64
	Unknown       uint64
}

// Modules performs module-owned continuation under the confirmed parent fence.
// Implementations must be replay-safe; nil, empty, partial and unknown proofs fail closed.
type Modules interface {
	ContinueBot(context.Context, ModuleRequest) ([]ModuleProof, error)
}

// continueRecovery switches each target in an independent atomic transaction, then moves configuration.
// Real permission probes run before mutations and again on switched routes without starting a worker.
func (service *Service) continueRecovery(ctx context.Context, task *d.Task, snapshot d.Snapshot, actor uint) (*d.Task, error) {
	if task.State == d.Switching {
		for _, route := range snapshot.BotRoutes {
			if err := service.authorize(ctx, actor, "resume"); err != nil {
				return task, err
			}
			input := probeInput(task.ID, snapshot, route, false)
			observation, err := service.Probe.ProbeRecovery(ctx, input)
			if err != nil {
				return task, err
			}
			if !permissionReady(route.Purpose, input, observation) {
				return task, ErrContinuationRequired
			}
			if err := service.authorize(ctx, actor, "resume"); err != nil {
				return task, err
			}
			if err := service.Continuation.SwitchTarget(ctx, task.ID, task.Version, route.TargetID); err != nil {
				return task, err
			}
		}
		var err error
		task, err = service.Continuation.MoveModules(ctx, task.ID, task.Version)
		if err != nil {
			return task, err
		}
	}
	for _, route := range snapshot.BotRoutes {
		if err := service.authorize(ctx, actor, "resume"); err != nil {
			return task, err
		}
		input := probeInput(task.ID, snapshot, route, true)
		observation, err := service.Probe.ProbeRecovery(ctx, input)
		if err != nil {
			return task, err
		}
		if err := service.authorize(ctx, actor, "resume"); err != nil {
			return task, err
		}
		if err := service.Continuation.Observe(ctx, task.ID, task.Version, groups.RecoveryObservation{RecoveryProbeInput: input, ExpectedTargetVersion: route.Version + 2, Observation: observation}); err != nil {
			return task, err
		}
	}
	if service.Modules == nil {
		return task, ErrContinuationRequired
	}
	proofs, err := service.Modules.ContinueBot(ctx, ModuleRequest{TaskID: task.ID, ActorID: actor, Snapshot: snapshot})
	if err != nil {
		return task, err
	}
	if err := service.authorize(ctx, actor, "resume"); err != nil {
		return task, err
	}
	if len(proofs) == 0 {
		return task, ErrContinuationRequired
	}
	seen := map[string]bool{}
	for _, proof := range proofs {
		if seen[proof.Module] || proof.Receipt == "" || !proof.LocalComplete || proof.Remaining != 0 || proof.Unknown != 0 {
			return task, ErrContinuationRequired
		}
		seen[proof.Module] = true
		if err := service.Continuation.RecordModuleProof(ctx, task.ID, task.Version, proof); err != nil {
			return task, err
		}
	}
	for _, route := range snapshot.BotRoutes {
		if !seen[route.Purpose] {
			return task, ErrContinuationRequired
		}
	}
	// Both business adapters are mandatory even without current targets: historical
	// support sessions or membership liabilities may survive a prior route change.
	if !seen["membership"] || !seen["support"] {
		return task, ErrContinuationRequired
	}
	if err := service.authorize(ctx, actor, "resume"); err != nil {
		return task, err
	}
	return service.Continuation.Finalize(ctx, task.ID, task.Version)
}

// permissionReady rejects empty, stale or insufficient platform results before route mutation.
func permissionReady(purpose string, input groups.RecoveryProbeInput, observation gd.Observation) bool {
	if observation.BotTelegramID != input.TelegramID || observation.Generation != input.Generation || observation.RouteVersion != input.RouteVersion || observation.State != "ready" || observation.ErrorClass != "" || observation.ObservedAt == nil || time.Since(*observation.ObservedAt) < 0 || time.Since(*observation.ObservedAt) > time.Minute {
		return false
	}
	return len(gd.MissingCapabilities(gd.Target{Purpose: purpose, Observation: observation})) == 0
}

// probeInput derives destination permission checks solely from the immutable inventory.
func probeInput(id uint, snapshot d.Snapshot, route d.BotRoute, switched bool) groups.RecoveryProbeInput {
	version := route.RouteVersion
	if switched {
		version++
	}
	return groups.RecoveryProbeInput{TaskID: id, TargetID: route.TargetID, BotID: snapshot.New.BotID, Version: snapshot.ExpectedNew.Config, Generation: snapshot.ExpectedNew.Generation, TelegramID: snapshot.New.TelegramID, ChatID: route.DestinationChat(), RouteVersion: version}
}
