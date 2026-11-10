package application

import (
	"context"
	"errors"
	"fmt"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"time"
)

// ReadyQueue adapts the existing runtime durable Enqueue. No platform HTTP is
// performed here. Enqueue MUST deduplicate by (bot,key), preserving existing rows.
type ReadyQueue interface {
	EnqueueDelivery(context.Context, r.DeliveryRequest) error
}

// ReadyBuilder must be deterministic and derive content only from Source. Store
// PrepareDispatch independently verifies it against its trusted encoder.
type ReadyBuilder interface {
	BuildReady(c.PreparedReady) (r.Message, error)
}

// ReadyWorker is opt-in; construction does not enable production DeliveryReady.
// Its bounded tick handles only business scheduling. Runtime delivers later.
type ReadyWorker struct {
	Store    c.Dispatches
	Consumer c.ReadyConsumer
	Queue    ReadyQueue
	Builder  ReadyBuilder
	NewClaim func() string
	// Topics reuses durable creation slots after a proven missing-topic rejection.
	Topics interface {
		Ensure(context.Context, uint, uint) error
	}
}

// Tick scans one bounded ID page for a live bot generation. It returns the final
// scanned ID (zero means wrap), and joins per-row failures without dropping work.
// now and generation originate from the runtime lease owner, never client input.
func (worker *ReadyWorker) Tick(ctx context.Context, bot uint, generation uint64, after uint, limit int, now time.Time) (uint, error) {
	if worker.Store == nil || worker.Consumer == nil || worker.Queue == nil || worker.Builder == nil || worker.NewClaim == nil {
		return after, c.ErrDenied
	}
	if err := r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return after, err
	}
	rows, err := worker.Store.ScanReady(ctx, bot, after, limit)
	if err != nil {
		return after, err
	}
	var failures []error
	for _, row := range rows {
		if e := ctx.Err(); e != nil {
			return after, errors.Join(append(failures, e)...)
		}
		after = row.ID
		var e error
		if row.State == "sending" || row.State == "unknown" {
			state, reconcileErr := worker.Store.ReconcileReady(ctx, bot, row.ID, generation, now)
			e = reconcileErr
			if e == nil && state == "topic_missing" && worker.Topics != nil {
				e = worker.Topics.Ensure(ctx, bot, row.SessionID)
				if e == nil {
					state, e = worker.Store.ReconcileReady(ctx, bot, row.ID, generation, now)
				}
			}
			if e == nil && state == "sending" {
				e = worker.enqueue(ctx, bot, row.ID, generation, now)
			}
		} else {
			claim := worker.NewClaim()
			claimed, claimErr := worker.Consumer.ClaimReady(ctx, bot, row.SessionID, generation, claim, now, time.Minute)
			e = claimErr
			if e == nil && claimed != nil {
				prepared, loadErr := worker.Store.LoadReady(ctx, bot, claimed.ID, generation, claim, now)
				if loadErr != nil {
					e = fmt.Errorf("load ready: %w", loadErr)
				} else {
					e = nil
				}
				if e == nil {
					envelope, buildErr := worker.Builder.BuildReady(prepared)
					e = buildErr
					if errors.Is(buildErr, r.ErrInvalidTransfer) {
						if rejections, ok := worker.Store.(c.ReadyRejections); ok {
							if rejectErr := rejections.RejectReady(ctx, bot, claimed.ID, generation, claim, now); rejectErr != nil {
								e = fmt.Errorf("reject ready: %w", rejectErr)
							} else {
								e = nil
							}
						}
					}
					if buildErr == nil {
						request, prepareErr := worker.Store.PrepareDispatch(ctx, bot, claimed.ID, generation, claim, envelope, now)
						e = prepareErr
						if e == nil {
							e = worker.Queue.EnqueueDelivery(ctx, request)
						}
					}
				}
			}
		}
		if e != nil {
			failures = append(failures, fmt.Errorf("support ready message %d: %w", row.ID, e))
		}
	}
	if len(rows) < limit {
		after = 0
	}
	return after, errors.Join(failures...)
}

// enqueue restores only prepared requests missing from the runtime queue.
func (worker *ReadyWorker) enqueue(ctx context.Context, bot, message uint, generation uint64, now time.Time) error {
	request, err := worker.Store.PendingDispatch(ctx, bot, message, generation, now)
	if err != nil || request == nil {
		return err
	}
	return worker.Queue.EnqueueDelivery(ctx, *request)
}

// ResolutionAuthorizer must verify current administrator permission on every
// resolution. Group membership is NOT permission to adjudicate unknown effects.
type ResolutionAuthorizer interface {
	AuthorizeSupportResolution(context.Context, uint, uint) error
}

// Resolver is the application authorization boundary around repository CAS.
type Resolver struct {
	Topics     c.Topics
	Dispatches c.Dispatches
	Authorizer ResolutionAuthorizer
}

// ResolveTopic checks actor/bot authority before an audited topic resolution.
func (resolver *Resolver) ResolveTopic(ctx context.Context, bot, id uint, in c.Resolution, now time.Time) error {
	if resolver.Authorizer == nil || resolver.Topics == nil {
		return c.ErrDenied
	}
	if err := resolver.Authorizer.AuthorizeSupportResolution(ctx, in.ActorID, bot); err != nil {
		return err
	}
	return resolver.Topics.ResolveTopic(ctx, bot, id, in, now)
}

// ResolveReady checks actor/bot authority before an audited delivery resolution.
func (resolver *Resolver) ResolveReady(ctx context.Context, bot, id uint, generation uint64, in c.Resolution, now time.Time) error {
	if resolver.Authorizer == nil || resolver.Dispatches == nil {
		return c.ErrDenied
	}
	if err := resolver.Authorizer.AuthorizeSupportResolution(ctx, in.ActorID, bot); err != nil {
		return err
	}
	return resolver.Dispatches.ResolveReady(ctx, bot, id, generation, in, now)
}
