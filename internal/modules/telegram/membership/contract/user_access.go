package contract

import (
	"context"
	"time"
)

// UserAccessGroup 是网站账户级只读白名单，不包含邀请链接、密文或平台凭据。
// 同目标有不同历史受益人时使用最近购买身份，绝不合并不同身份的资格。
// ExpiresAt 永久或尚未形成账本资格时为 null；CanRequest 不是外部动作授权。
type UserAccessGroup struct {
	TargetID              uint       `json:"target_id,string"`
	Title                 string     `json:"title"`
	BeneficiaryTelegramID int64      `json:"beneficiary_telegram_id,string"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at"`
	State                 string     `json:"state"`
	MemberStatus          string     `json:"member_status"`
	CanRequest            bool       `json:"can_request"`
}

// OrderAccessReader resolves immutable target IDs within one owned order and its children.
// Implementations must verify current account/beneficiary binding and exclude unrelated orders.
type OrderAccessReader interface {
	OrderMembershipTargets(context.Context, uint, int64, uint) ([]uint, error)
}

// OrderMembershipRoute is an immutable purchase-time physical route, without invitation secrets.
// Titles were not captured by order snapshots; ChatID remains the historical display fact.
type OrderMembershipRoute struct {
	BotInstanceID uint   `json:"bot_instance_id"`
	ChatID        int64  `json:"chat_id,string"`
	RouteVersion  uint64 `json:"route_version"`
}

// OrderMembershipTarget groups one logical target's distinct routes from this order only.
// Routes never includes unrelated purchases or the target's global route history.
type OrderMembershipTarget struct {
	TargetID uint                   `json:"target_id,string"`
	Routes   []OrderMembershipRoute `json:"routes"`
}

// OrderMembershipHistoryReader optionally exposes order-scoped immutable route provenance.
// account, beneficiary and order use the same ownership/binding checks as OrderAccessReader.
// Implementations return unique target IDs and unique physical routes without changing snapshots.
type OrderMembershipHistoryReader interface {
	OrderMembershipHistory(ctx context.Context, account uint, beneficiary int64, order uint) ([]OrderMembershipTarget, error)
}

// UserAccessReader 从认证会话提供的 accountID 查询账户历史，不能由 IP/body 选择账号。
// afterTargetID 是排他目标游标，limit 范围 1..100，now 为服务端时间。
// 返回按 target_id 升序的独立 DTO；申请邀请仍须重新验证身份、资格和当前路由。
type UserAccessReader interface {
	ListUserAccess(ctx context.Context, accountID, afterTargetID uint, limit int, now time.Time) ([]UserAccessGroup, error)
}
