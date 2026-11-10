package contract

import (
	"context"
	"errors"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
)

// ErrMembershipInvalid 表示真实订单、付款事实或不可变会员快照不匹配。
var ErrMembershipInvalid = errors.New("invalid membership fulfillment facts")

// ErrTransactionRequired 表示系统履约必须绑定调用者的实际数据库事务。
var ErrTransactionRequired = errors.New("fulfillment requires an active database transaction")

// MembershipSystemStore 由会员账本在贡献、投影与任务写入的同一事务中调用。
// itemIDs 必须为实际叶订单会员项，paidAt 必须为固定可信付款时间；失败回滚整个事务。
// 成功仅表示权益已记录，不表示用户已入群；实现不能执行 HTTP 或通知发送。
type MembershipSystemStore interface {
	CompleteMembership(context.Context, []uint, time.Time) error
}

// OrdinaryItemStore 隔离人工/卡密与会员项；只供已绑定订单事务的内部履约编排。
type OrdinaryItemStore interface {
	// OrdinaryItems 返回 kind 对应且尚未交付的非会员项；会员项永不允许文本或卡密替代。
	OrdinaryItems(orderID uint, kind string) ([]uint, error)
	// SaveOrdinary 保存或合并普通交付内容，保留既有人工/卡密内容。
	SaveOrdinary(*d.Fulfillment) error
	// CompleteOrdinary 持久化普通项回执并汇总叶单与父单；返回叶单是否全部完成。
	CompleteOrdinary(*d.Fulfillment) (bool, error)
}
