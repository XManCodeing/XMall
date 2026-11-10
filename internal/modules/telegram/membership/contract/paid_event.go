package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"time"
)

// PaidEventAppender 必须绑定可信付款事务；AppendPaid 重读本地事实并按快照决定是否写入。
type PaidEventAppender interface {
	AppendPaid(context.Context, uint, uint) error
}

// PaidEventProvider 是支付事务的窄扩展，生产必须提供，普通历史测试桩可不实现。
type PaidEventProvider interface {
	MembershipPaidEvents() (PaidEventAppender, error)
}

// PaidEventStore 提供持久恢复和带随机 owner 的租约 CAS；所有时间均为调度时间而非权益起算点。
type PaidEventStore interface {
	ClaimPaid(context.Context, string, time.Time, time.Duration) (*d.PaidEvent, error)
	FinishPaid(context.Context, uint, string, time.Time) error
	RetryPaid(context.Context, uint, string, time.Time, time.Time) error
	RecoverPaid(context.Context, uint, int) (uint, int, error)
}
