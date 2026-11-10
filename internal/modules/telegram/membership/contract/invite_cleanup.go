package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"time"
)

// InviteCleanupRepository 是清理专用最终权益校验端口；不授权创建或批准邀请。
// 清理 worker 在外部调用前后检查同一历史身份的全部来源，续费竞态保留未知证据。
type InviteCleanupRepository interface {
	CleanupEligibility(context.Context, *d.AccessOperation, time.Time) (AccessEligibility, error)
}
