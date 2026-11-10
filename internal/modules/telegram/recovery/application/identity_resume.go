package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// Resume commits the reviewed mapping once and polls existing runtime/lifecycle evidence.
// expectedVersion is mandatory; no Telegram call, invitation or cleanup is manufactured here.
// Completed is reported only after the backend atomically records real cleanup proof.
func (executor *IdentityExecutor) Resume(ctx context.Context, id uint, expectedVersion uint64, actor uint) (*d.Task, error) {
	detail, err := executor.load(ctx, id, actor, "identity-resume")
	if err != nil {
		return nil, err
	}
	if expectedVersion == 0 || detail.Task.Version != expectedVersion {
		return nil, c.ErrStale
	}
	switch detail.Task.State {
	case d.Completed:
		return &detail.Task, nil
	case d.Fencing:
		if err := executor.Backend.Commit(ctx, detail); err != nil {
			return nil, err
		}
		detail, err = executor.Store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
	case d.Recovering:
	default:
		return nil, c.ErrStale
	}
	if _, err := executor.Backend.Completed(ctx, detail); err != nil {
		return nil, err
	}
	detail, err = executor.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &detail.Task, nil
}
