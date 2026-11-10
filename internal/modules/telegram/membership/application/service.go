package application

import (
	"context"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// Service 提供会员配置应用入口，不暴露销售就绪的可写开关。
type Service struct {
	store c.Store
}

// New 使用数据库窄接口 store 构造服务并返回指针；不执行网络或数据库操作。
func New(store c.Store) *Service { return &Service{store: store} }

// Config 按现有商品 productID 读取配置；ctx 控制取消，缺失返回 ErrNotFound。
func (service *Service) Config(ctx context.Context, productID uint) (*d.ProductConfig, error) {
	if productID == 0 {
		return nil, c.ErrInvalid
	}
	return service.store.Config(ctx, productID)
}

// SaveConfig 原子替换 input 的配置并记录不含身份或秘密的版本审计日志。
// ctx 控制事务取消；返回已保存配置或验证/CAS/存储错误。
func (service *Service) SaveConfig(ctx context.Context, input c.ConfigInput) (*d.ProductConfig, error) {
	config, err := service.store.SaveConfig(ctx, input, time.Now().UTC())
	if err != nil {
		slog.WarnContext(ctx, "membership configuration rejected", "product_id", input.ProductID, "expected_version", input.ExpectedVersion)
		return nil, err
	}
	slog.InfoContext(ctx, "membership configuration saved", "product_id", config.ProductID, "version", config.Version)
	return config, nil
}

// CheckSale 检查 productIDs 对应配置是否启用；缺少商品引用时拒绝。
// 返回当前配置读取错误或停售错误，交易仍须执行事务内完整校验。
func (service *Service) CheckSale(productIDs ...uint) error {
	if len(productIDs) == 0 {
		return c.ErrSalesClosed
	}
	for _, id := range productIDs {
		config, err := service.Config(context.Background(), id)
		if err != nil {
			return err
		}
		if config == nil || !config.Enabled {
			return c.ErrSalesClosed
		}
	}
	return nil
}
