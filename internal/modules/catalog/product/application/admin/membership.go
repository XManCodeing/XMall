package productadmin

import (
	"context"
	"log/slog"
	"math"
	"strconv"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// UpdateMembershipStatus 在原商品事务内校验 id/version 并原子更新配置及商品状态。
// 返回提交后的商品；上架验证完整配置，下架不依赖群权限且不修改历史权益。
func (s *AdminService) UpdateMembershipStatus(ctx context.Context, id string, active bool, version uint64) (*productdomain.Product, error) {
	productID, err := membershipOperationID(id, version)
	if err != nil {
		return nil, err
	}
	err = s.transactions.WithinTransaction(func(repositories DeleteRepositories) error {
		if repositories.Membership == nil {
			return membership.ErrTransactionRequired
		}
		if err := repositories.Membership.PrepareProductOperation(ctx, productID, version, active, false); err != nil {
			return err
		}
		updater, ok := repositories.Products.(interface {
			QuickUpdate(string, map[string]interface{}) error
		})
		if !ok {
			return productcontract.ErrSalePolicyUnavailable
		}
		return updater.QuickUpdate(id, map[string]interface{}{"is_active": active})
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "membership product sale state updated", "product_id", productID, "version", version+1, "enabled", active)
	return s.products.GetAdminByID(id)
}

// DeleteMembership 使用原商品级联删除，要求 id 是会员且 version 匹配。
// 锁内复核库存/订单/快照后才清理扩展；所有清理共享事务，历史记录永不删除。
func (s *AdminService) DeleteMembership(ctx context.Context, id string, version uint64) error {
	productID, err := membershipOperationID(id, version)
	if err != nil {
		return err
	}
	err = s.transactions.WithinTransaction(func(repositories DeleteRepositories) error {
		if repositories.Membership == nil {
			return membership.ErrTransactionRequired
		}
		if err := repositories.Membership.PrepareProductOperation(ctx, productID, version, false, true); err != nil {
			return err
		}
		if repositories.Stock == nil {
			return productcontract.ErrSalePolicyUnavailable
		}
		available, err := repositories.Stock.CountAvailable(productID, 0)
		if err != nil {
			return err
		}
		reserved, err := repositories.Stock.CountReserved(productID, 0)
		if err != nil {
			return err
		}
		if available > 0 || reserved > 0 {
			return productcontract.ErrProductHasStock
		}
		return deleteProductRelations(repositories, productID, id)
	})
	if err == nil {
		slog.InfoContext(ctx, "membership product deleted", "product_id", productID, "version", version)
	}
	return err
}

// membershipOperationID 校验外部 id 和正版本，返回平台 uint；拒绝版本溢出。
func membershipOperationID(id string, version uint64) (uint, error) {
	parsed, err := strconv.ParseUint(id, 10, strconv.IntSize)
	if err != nil || parsed == 0 || version == 0 || version >= math.MaxInt64 {
		return 0, membership.ErrInvalid
	}
	return uint(parsed), nil
}
