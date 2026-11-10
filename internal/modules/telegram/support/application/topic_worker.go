package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// TopicRoutes resolves current capabilities; never derive a destination from
// message text, topic titles, or an old session's physical chat ID.
type TopicRoutes interface {
	ResolveSupportRoute(context.Context, uint) (g.SupportRoute, error)
}

// TopicPlatform is credential-free. Its implementation checks the execution
// fence again immediately before calling Telegram; it must never retry HTTP.
type TopicPlatform interface {
	ExecuteTopic(context.Context, g.SupportRoute, string, int64, string) (c.TopicResult, error)
}

// TopicWorker provisions at most one physical topic per durable creation slot.
// Unknown outcomes intentionally stop here until an audited resolution exists.
type TopicWorker struct {
	Topics   c.Topics
	Routes   TopicRoutes
	Platform TopicPlatform
	NewClaim func() string
	Now      func() time.Time
}

// ReceiveTopic consumes native topic service updates before normal reply routing.
// bot and generation must come from the supervised inbox worker, not the update.
// handled stays true on rejection so a service message never falls into replies.
// No platform or queue method is invoked by this receiver.
func (w *TopicWorker) ReceiveTopic(ctx context.Context, bot uint, generation uint64, update r.Update) (handled bool, err error) {
	if update.TopicService == nil {
		return update.MessageKind == "service", nil
	}
	if w.Topics == nil || w.Routes == nil || w.Now == nil {
		return true, c.ErrDenied
	}
	if err = r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return true, err
	}
	route, err := w.Routes.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return true, err
	}
	if route.BotID != bot || route.Generation != generation || route.ChatID != update.ChatID {
		return true, c.ErrDenied
	}
	events, ok := w.Topics.(c.TopicEvents)
	if !ok {
		return true, c.ErrDenied
	}
	return true, events.ObserveTopic(ctx, c.TopicEvent{BotID: bot, TargetID: route.TargetID,
		Generation: generation, RouteVersion: route.RouteVersion, Update: update}, w.Now().UTC())
}

// Reconcile expires abandoned local claims without issuing platform requests.
// The active lease context and generation are checked before touching storage.
func (w *TopicWorker) Reconcile(ctx context.Context, bot uint, generation uint64, now time.Time) error {
	if err := r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return err
	}
	recovery, ok := w.Topics.(c.TopicRecovery)
	if !ok {
		return c.ErrDenied
	}
	return recovery.ExpireTopicClaims(ctx, bot, generation, now, 50)
}

func (w *TopicWorker) Ensure(ctx context.Context, bot, session uint) error {
	if w.Topics == nil || w.Routes == nil || w.Platform == nil || w.NewClaim == nil || w.Now == nil || bot == 0 || session == 0 {
		return c.ErrDenied
	}
	route, err := w.Routes.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return err
	}
	if err = r.CheckExecutionFence(ctx, bot, route.Generation); err != nil {
		return err
	}
	claim := w.NewClaim()
	job, err := w.Topics.ClaimTopic(ctx, bot, session, route.TargetID, route.RouteVersion, route.Generation, route.ChatID, claim, w.Now().UTC(), time.Minute)
	if err != nil {
		return err
	}
	if !job.Execute {
		if job.Mapping == nil || (job.Operation.State != "succeeded" && job.Operation.State != "failed") {
			return nil
		}
		if job.Mapping.State == "open" {
			return nil
		}
		if job.Mapping.State != "closed" {
			return nil
		}
		job, err = w.Topics.BeginTopicAction(ctx, bot, job.Mapping.ID, job.Mapping.Version, route.Generation, "reopen", claim, w.Now().UTC(), time.Minute)
		if err != nil || !job.Execute {
			return err
		}
	}
	// Claiming does not authorize stale network effects: route and lease may
	// have changed while the transaction was waiting for its connection.
	current, err := w.Routes.ResolveSupportRoute(ctx, bot)
	if err != nil || current != route {
		if err == nil {
			err = c.ErrConflict
		}
		return err // no external effect; durable claim expires conservatively
	}
	if err = r.CheckExecutionFence(ctx, bot, route.Generation); err != nil {
		return err
	}
	thread := int64(0)
	if job.Mapping != nil {
		thread = job.Mapping.ThreadID
	}
	title, err := w.topicTitle(ctx, bot, session)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, callErr := w.Platform.ExecuteTopic(callCtx, route, job.Operation.Action, thread, title)
	cancel()
	// Implementations must explicitly prove a rejection. Empty/invalid results
	// are uncertain, including cancellation after a successful platform call.
	if result.State != "succeeded" && result.State != "failed" && result.State != "unknown" {
		result = c.TopicResult{State: "unknown"}
	}
	if result.State == "succeeded" && job.Operation.Action == "create" && result.ThreadID <= 0 {
		result.State = "unknown"
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	// No external retry. A later tick reconciles the durable claim if storing
	// this result fails; an ambiguous create must never be recreated blindly.
	finishErr := w.Topics.FinishTopic(finishCtx, bot, job.Operation.ID, route.Generation, job.Operation.Claim, result, w.Now().UTC())
	return errors.Join(callErr, finishErr)
}

// topicTitle formats persisted identity within Telegram's 128-character bound.
// bot/session identify authoritative storage; legacy test adapters keep their existing fallback.
func (w *TopicWorker) topicTitle(ctx context.Context, bot, session uint) (string, error) {
	identities, ok := w.Topics.(c.VerificationControls)
	if !ok {
		return fmt.Sprintf("Support #%d", session), nil
	}
	owner, err := identities.TopicIdentity(ctx, bot, session)
	if err != nil {
		return "", err
	}
	name := strings.Join(strings.Fields(owner.DisplayName), " ")
	if name == "" {
		name = fmt.Sprintf("%d", owner.UserID)
	}
	suffix := fmt.Sprintf(" (用户ID:%d)", owner.UserID)
	characters := []rune(name)
	if len(characters) > 128-len([]rune(suffix)) {
		characters = characters[:128-len([]rune(suffix))]
	}
	return string(characters) + suffix, nil
}
