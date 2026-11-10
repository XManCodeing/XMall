package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// verifyCleanupSnapshot 在平台撤销前重读退休证据、来源与租约；续费不恢复旧入口。
// operation 必须为清理撤销任务，snapshot 保持调用契约；返回真实栅栏错误。
func (service *AccessService) verifyCleanupSnapshot(ctx context.Context, operation *d.AccessOperation, snapshot c.AccessEligibility) error {
	guard, ok := service.Store.(c.InviteCleanupRepository)
	if !ok {
		return c.ErrCapability
	}
	_, err := guard.CleanupEligibility(ctx, operation, service.accessNow())
	return err
}
