package telegramsupportrecovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	ss "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"gorm.io/gorm"
)

// Authority is supplied by the recovery/groups owner. It must lock and validate
// the task's current permission, immutable two-sided snapshots, active scope and
// exact switched destination route with fresh forum capability proof. It is DB
// only, bound to tx, and must allow only the named recovery topic action while
// normal routes remain fenced. No permissive default is provided.
type Authority func(context.Context, *gorm.DB, c.SupportRestoration, time.Time) error

// Adapter composes real runtime drain proofs with task-owned route authorization.
// API requests are made only after the validation transaction commits.
type Adapter struct {
	Permission Permission
	DB         *gorm.DB
	Authorize  Authority
	Cipher     c.Cipher
	API        r.ForumTopicAPI
	Now        func() time.Time
}

// guard is bound to one transaction; it never accesses Adapter.DB.
type guard struct {
	tx        *gorm.DB
	authority Authority
}

// CheckSupportRestoration proves every admitted generation joined on both bots,
// then checks task/route scope on the same connection. Disabled targets are valid.
func (g guard) CheckSupportRestoration(ctx context.Context, plan c.SupportRestoration, now time.Time) error {
	if g.authority == nil {
		return c.ErrDenied
	}
	if err := g.authority(ctx, g.tx, plan, now); err != nil {
		return err
	}
	for _, side := range []c.RestorationRoute{plan.Source, plan.Destination} {
		if err := rs.New(g.tx).RecoveryDrained(ctx, r.RecoveryFence{BotID: side.BotID, TaskID: plan.TaskID, Version: side.Version, Generation: side.Generation}); err != nil {
			return err
		}
	}
	return nil
}

// Store returns the support store with fail-closed transactional recovery ports.
// Dependencies are immutable after construction; nil authority denies all work.
func (a *Adapter) Store() *ss.Store { return a.boundStore(a.DB) }

// boundStore attaches adapters to the supplied handle, preserving single-connection transactions.
func (a *Adapter) boundStore(db *gorm.DB) *ss.Store {
	return ss.New(db, a.Cipher).WithRecoveryDrain(func(tx *gorm.DB) r.RecoveryFenceStore { return rs.New(tx) }).
		WithRestorationGuard(func(tx *gorm.DB) c.RestorationGuard { return guard{tx: tx, authority: a.Authorize} })
}

// CreateRestorationTopic authorizes an exact new destination topic. It does not
// start runtime, inherit contacts or create a fake runtime execution context.
// The parent executor must retain its task lease across this entire call.
func (a *Adapter) CreateRestorationTopic(ctx context.Context, job c.RestorationTopic) (c.TopicResult, error) {
	plan, session := job.Plan, job.Claim.Operation.SessionID
	failed := c.TopicResult{State: "failed"}
	if a.DB == nil || a.Authorize == nil || a.Cipher == nil || a.API == nil || a.Now == nil || session == 0 {
		return failed, c.ErrDenied
	}
	if a.Permission != nil {
		p, ok := ctx.Value(permitKey{}).(permit)
		if !ok || p.Step.ID == 0 {
			return failed, c.ErrDenied
		}
	}
	if a.Permission != nil {
		if err := a.Permission(ctx, plan.ActorID, "resume"); err != nil {
			return failed, err
		}
	}
	var token string
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := a.boundStore(tx).AuthorizeRestorationTopic(ctx, job, a.Now().UTC()); err != nil {
			return err
		}
		instance, err := rs.New(tx).Get(ctx, plan.Destination.BotID)
		if err != nil {
			return err
		}
		if instance == nil || instance.Mode != "internal" {
			return c.ErrDenied
		}
		token, err = a.Cipher.Open(instance.TokenCipher)
		return err
	})
	if err != nil {
		return failed, c.ErrDenied
	}
	created, err := a.API.CreateForumTopic(ctx, token, r.ForumTopicRequest{ChatID: plan.Destination.ChatID, Name: fmt.Sprintf("Support #%d", session)})
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if a.Permission != nil {
		if permissionErr := a.Permission(finishCtx, plan.ActorID, "resume"); permissionErr != nil {
			return c.TopicResult{State: "unknown"}, permissionErr
		}
	}
	if guardErr := a.DB.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		return (guard{tx: tx, authority: a.Authorize}).CheckSupportRestoration(finishCtx, plan, a.Now().UTC())
	}); guardErr != nil {
		return c.TopicResult{State: "unknown"}, guardErr
	}
	if err == nil && created.ThreadID > 0 {
		return c.TopicResult{State: "succeeded", ThreadID: created.ThreadID}, nil
	}
	result := c.TopicResult{State: "unknown"}
	var platform r.APIError
	if errors.As(err, &platform) && !platform.Uncertain() && platform.StatusCode() >= 400 && platform.StatusCode() < 500 {
		result.State = "failed"
		if platform.StatusCode() == 429 {
			result.RetryAfterSeconds = int64(platform.RetrySeconds())
			if result.RetryAfterSeconds < 1 {
				result.RetryAfterSeconds = 5
			}
		}
	}
	return result, errors.New("support restoration topic unresolved")
}

var _ c.RestorationPlatform = (*Adapter)(nil)
