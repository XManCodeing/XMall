package application

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"log/slog"
	"time"
)

// RestorationWorker performs one durable topic attempt per invocation. It never
// enables a normal runtime or copies history into the new delivery queue.
type RestorationWorker struct {
	Store    c.RestorationStore
	Platform c.RestorationPlatform
	NewClaim func() string
	Now      func() time.Time
}

// RunTopic claims one new mapping, calls the task-authorized platform outside
// transactions and persists a classified result. Interruptions remain unknown;
// only explicit proven failure may later be handled through audited recovery.
func (w *RestorationWorker) RunTopic(ctx context.Context, task uint, targets ...uint) error {
	if w.Store == nil || w.Platform == nil || w.NewClaim == nil || w.Now == nil || task == 0 {
		return c.ErrDenied
	}
	job, err := w.Store.ClaimRestorationTopic(ctx, task, w.NewClaim(), w.Now().UTC(), targets...)
	if err != nil || !job.Claim.Execute {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, callErr := w.Platform.CreateRestorationTopic(callCtx, job)
	cancel()
	if result.State != "succeeded" && result.State != "failed" && result.State != "unknown" {
		result = c.TopicResult{State: "unknown"}
	}
	if result.State == "succeeded" && result.ThreadID <= 0 {
		result = c.TopicResult{State: "unknown"}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	finishErr := w.Store.FinishRestorationTopic(finishCtx, task, job.EntryID, job.Claim.Operation.Claim, result, w.Now().UTC())
	slog.InfoContext(ctx, "support_restoration_topic_result", "task_id", task, "entry_id", job.EntryID, "state", result.State, "persisted", finishErr == nil)
	return errors.Join(callErr, finishErr)
}
