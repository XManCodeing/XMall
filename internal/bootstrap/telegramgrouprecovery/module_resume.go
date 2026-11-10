package telegramgrouprecovery

import (
	"context"

	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	domain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// moduleRetryStore is bootstrap-only: proof runs on the locked parent transaction.
// Generic recovery persistence never imports membership internals.
type moduleRetryStore interface {
	RetryGroupModule(context.Context, recovery.LocalGroupRetry, func(*gorm.DB, domain.Snapshot) error) (*domain.Task, error)
}

// membershipResumeChecker provides evidence only, never a substitute success receipt.
type membershipResumeChecker interface {
	CheckGroupResume(context.Context, *gorm.DB, membership.Request) error
}

// ResumeGroupModule reauthorizes the real caller before a same-transaction wrapper
// CAS and module scan. Snapshot is loaded by the locked store, never supplied by HTTP.
func (executor *Executor) ResumeGroupModule(ctx context.Context, input recovery.LocalGroupRetry) (*domain.Task, error) {
	if executor == nil || executor.Authorize == nil || input.ActorID == 0 {
		return nil, recovery.ErrInvalid
	}
	if err := executor.Authorize(ctx, input.ActorID, "resume"); err != nil {
		return nil, err
	}
	store, ok := executor.Store.(moduleRetryStore)
	if !ok {
		return nil, recovery.ErrInvalid
	}
	checker, ok := executor.Membership.(membershipResumeChecker)
	if !ok {
		return nil, recovery.ErrInvalid
	}
	return store.RetryGroupModule(ctx, input, func(tx *gorm.DB, snapshot domain.Snapshot) error {
		if snapshot.Old.TargetID == 0 || snapshot.Old.TargetID != snapshot.New.TargetID || snapshot.Old.BotID != snapshot.New.BotID || snapshot.Old.ChatID == snapshot.New.ChatID {
			return recovery.ErrStale
		}
		var count int64
		if err := tx.Table("telegram_group_targets").Where("id = ? AND version = ? AND purpose = ?", snapshot.New.TargetID, snapshot.ExpectedOld.Config+2, "membership").Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return recovery.ErrStale
		}
		return checker.CheckGroupResume(ctx, tx, membership.Request{TaskID: input.TaskID, ActorID: input.ActorID,
			TargetID: snapshot.New.TargetID, BotID: snapshot.New.BotID, OldChatID: snapshot.Old.ChatID, NewChatID: snapshot.New.ChatID,
			OldRouteVersion: snapshot.ExpectedOld.Route, NewRouteVersion: snapshot.ExpectedOld.Route + 1,
			Generation: snapshot.ExpectedNew.Generation, Purpose: "membership"})
	})
}
