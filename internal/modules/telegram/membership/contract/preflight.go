package contract

import (
	"context"
	"errors"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"time"
)

// PaymentPreflight 仅复核已持久化的实际订单项快照，不从当前套餐重建权益。
// 返回是否含会员项及复核错误；实现必须绑定调用者事务，普通项不查询身份。
type PaymentPreflight interface {
	CheckPayment(context.Context, *od.Order, time.Time) (bool, error)
}

// PaymentPreflightProvider 为支付工作单元提供同事务历史订单复核。
type PaymentPreflightProvider interface {
	MembershipPaymentPreflight() (PaymentPreflight, error)
}

// ErrProbeRetry 表示补查资源忙或预算耗尽，调用者须稍后重试，不能继续交易。
var ErrProbeRetry = errors.New("membership capability preflight busy; retry later")

// CapabilityPreflight 仅由事务外购买/继续支付编排调用，禁止注入事务仓储或公开列表。
// Checkout 返回所读配置/路由凭据；Payment 只从历史快照定位目标。
type CapabilityPreflight interface {
	Checkout(context.Context, []CheckoutLine) (*CapabilityProof, error)
	Payment(context.Context, *od.Order) error
}

// CapabilityProof 固定补查之前的配置及路由版本，最终事务不得接受另一套目标。
type CapabilityProof struct {
	Versions map[uint]uint64
	Routes   map[uint]uint64
}

// Validate 对最终事务产生的 plan 作纯内存核对；不访问数据库或网络。
func (proof *CapabilityProof) Validate(plan *CheckoutPlan) error {
	if proof == nil {
		return nil
	}
	if plan == nil {
		if len(proof.Versions) != 0 {
			return ErrConflict
		}
		return nil
	}
	if len(plan.Lines) != len(proof.Versions) {
		return ErrConflict
	}
	for _, line := range plan.Lines {
		if proof.Versions[line.ProductID] != line.ConfigVersion {
			return ErrConflict
		}
		for _, target := range plan.Targets[line.ProductID] {
			if proof.Routes[target.TargetID] != target.RouteVersion {
				return ErrConflict
			}
		}
	}
	return nil
}
