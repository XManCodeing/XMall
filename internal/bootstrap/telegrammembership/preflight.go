// Package telegrammembership assembles transaction-free capability refresh orchestration.
package telegrammembership

import (
	"context"
	"errors"
	"log/slog"
	"time"

	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	ga "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

const preflightTimeout = 10 * time.Second
const probeAttempts = 8 // 每次请求的网络工作预算，不限制商品可配置的群数量。

// Preflight 只持有全局只读配置端口和已装配 runtime fence 的 groups 服务。
type Preflight struct {
	configs   c.ConfigReader
	snapshots c.SnapshotReader
	groups    *ga.Service
	sale      func([]uint) error
	slots     chan struct{}
}

// New 由生产装配传入共享 groups 服务和销售门控；返回事务外窄端口。
func New(configs c.ConfigReader, snapshots c.SnapshotReader, groups *ga.Service, sale func([]uint) error) *Preflight {
	return &Preflight{configs: configs, snapshots: snapshots, groups: groups, sale: sale, slots: make(chan struct{}, 2)}
}

// Checkout 先识别会员配置，再执行销售门控，最后仅补查未知/过期目标。
// lines 为可信商品引用；返回配置和路由凭据供原有事务核对。
func (p *Preflight) Checkout(ctx context.Context, lines []c.CheckoutLine) (*c.CapabilityProof, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	proof := &c.CapabilityProof{Versions: map[uint]uint64{}, Routes: map[uint]uint64{}}
	ids := map[uint]uint64{}
	products := []uint{}
	for _, line := range lines {
		config, err := p.configs.Config(ctx, line.ProductID)
		if errors.Is(err, c.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, seen := proof.Versions[line.ProductID]; seen || line.Quantity != 1 || line.SKUID == 0 {
			return nil, c.ErrInvalid
		}
		proof.Versions[line.ProductID] = config.Version
		products = append(products, line.ProductID)
		for _, target := range config.Targets {
			ids[target.TargetID] = 0
		}
	}
	if len(products) == 0 {
		return proof, ctx.Err()
	}
	if p.sale == nil {
		return nil, c.ErrSalesClosed
	}
	if err := p.sale(products); err != nil {
		return nil, err
	}
	routes, err := p.refresh(ctx, ids)
	proof.Routes = routes
	return proof, err
}

// Payment 只读取已持久化实际订单项快照；不按当前套餐重建历史目标。
// order 来自支付服务可信查询；后续同事务 PaymentPreflight 仍须完整执行。
func (p *Preflight) Payment(ctx context.Context, order *od.Order) error {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	if order == nil {
		return c.ErrInvalid
	}
	ids := map[uint]uint64{}
	products := []uint{}
	var collect func(*od.Order) error
	collect = func(current *od.Order) error {
		for _, item := range current.Items {
			snapshot, err := p.snapshots.Snapshot(ctx, item.ID)
			if errors.Is(err, c.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if snapshot.AccountID != order.UserID || current.UserID != order.UserID {
				return c.ErrUntrustedIdentity
			}
			products = append(products, item.ProductID)
			for _, target := range snapshot.Targets {
				if prior, exists := ids[target.TargetID]; exists && prior != target.RouteVersion {
					return c.ErrConflict
				}
				ids[target.TargetID] = target.RouteVersion
			}
		}
		for index := range current.Children {
			if err := collect(&current.Children[index]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(order); err != nil {
		return err
	}
	if len(products) == 0 {
		return ctx.Err()
	}
	if p.sale == nil {
		return c.ErrSalesClosed
	}
	if err := p.sale(products); err != nil {
		return err
	}
	_, err := p.refresh(ctx, ids)
	return err
}

// refresh 在所有数据库读取完成后逐个调用既有 Check；全局最多两个请求探测。
// 单请求最多八次且总计十秒，剩余目标下次重试；不创建无界 goroutine/等待队列。
func (p *Preflight) refresh(ctx context.Context, ids map[uint]uint64) (map[uint]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	routes := map[uint]uint64{}
	pending := []gd.Target{}
	for id, expected := range ids {
		target, err := p.groups.Store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if !target.Enabled || target.Purpose != "membership" || target.BotInstanceID == 0 || target.TelegramChatID >= 0 || target.RouteVersion == 0 {
			return nil, c.ErrCapability
		}
		if expected != 0 && expected != target.RouteVersion {
			return nil, c.ErrConflict
		}
		routes[id] = target.RouteVersion
		view := p.groups.View(*target)
		if view.Fresh {
			if !ready(*target, p.groups) {
				return nil, c.ErrCapability
			}
		} else {
			pending = append(pending, *target)
		}
	}
	if len(ids) == 0 {
		return nil, c.ErrCapability
	}
	if len(pending) == 0 {
		return routes, ctx.Err()
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		slog.Warn("membership capability preflight rejected", "reason", "capacity_busy")
		return nil, c.ErrProbeRetry
	}
	for index, target := range pending {
		if index >= probeAttempts {
			slog.Warn("membership capability preflight rejected", "reason", "attempt_budget")
			return nil, c.ErrProbeRetry
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// 另一个请求可能已完成补查；读取最新观测避免重复网络请求。
		current, err := p.groups.Store.Get(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		if current.RouteVersion != target.RouteVersion {
			return nil, c.ErrConflict
		}
		if !current.Enabled || current.Purpose != "membership" {
			return nil, c.ErrCapability
		}
		if ready(*current, p.groups) {
			continue
		}
		if p.groups.View(*current).Fresh {
			return nil, c.ErrCapability
		}
		detail, err := p.groups.Check(ctx, target.ID, target.RouteVersion, 0, "checkout")
		if err != nil {
			return nil, err
		}
		if detail.RouteVersion != target.RouteVersion {
			return nil, c.ErrConflict
		}
		if !ready(detail.Target, p.groups) {
			return nil, c.ErrCapability
		}
	}
	return routes, ctx.Err()
}

// ready 复用群能力/新鲜度定义，并拒绝未来观测或失效成员状态。
func ready(target gd.Target, groups *ga.Service) bool {
	view := groups.View(target)
	return target.Enabled && target.Purpose == "membership" && view.Fresh && target.Observation.State == "ready" &&
		target.Observation.IsMember && target.Observation.ObservedAt != nil && !target.Observation.ObservedAt.After(groups.Now()) && len(view.MissingCapabilities) == 0
}
