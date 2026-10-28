package consumer

import (
	"context"
	"testing"

	affiliateapp "github.com/XManCodeing/XMall/internal/modules/affiliate/application"
	affiliatedomain "github.com/XManCodeing/XMall/internal/modules/affiliate/domain"
	affiliategormstore "github.com/XManCodeing/XMall/internal/modules/affiliate/infrastructure/gormstore"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	settingsintegration "github.com/XManCodeing/XMall/internal/modules/settings/schema/integration"

	"github.com/XManCodeing/XMall/internal/app/container"
	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/queue"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/XManCodeing/XMall/internal/testkit/memorysettings"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type affiliateWorkerOrders struct{ order *orderdomain.Order }

func (r affiliateWorkerOrders) GetByID(uint) (*orderdomain.Order, error) { return r.order, nil }

type affiliateWorkerProducts struct{}

func (affiliateWorkerProducts) ListByIDs([]uint) ([]productdomain.Product, error) {
	return []productdomain.Product{{ID: 1, IsAffiliateEnabled: true}}, nil
}

// 支付时佣金创建失败投递的重试任务，消费后应补上佣金。
func TestAffiliateOrderPaidWorkerCreatesCommission(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:affiliate_order_paid_worker?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	if err := db.AutoMigrate(&affiliatedomain.Profile{}, &affiliatedomain.Commission{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	profile := affiliatedomain.Profile{UserID: 1, AffiliateCode: "WORKER1", Status: constants.AffiliateProfileStatusActive}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("seed profile failed: %v", err)
	}
	settings := settingsapp.NewService(memorysettings.New())
	if _, err := settings.UpdateAffiliateSetting(settingsintegration.AffiliateSetting{Enabled: true, CommissionRate: 10}); err != nil {
		t.Fatalf("init affiliate setting failed: %v", err)
	}
	order := &orderdomain.Order{
		ID: 42, UserID: 2, Status: constants.OrderStatusPaid, AffiliateProfileID: &profile.ID,
		Items: []orderdomain.OrderItem{{ProductID: 1, TotalPrice: money.FromDecimal(decimal.NewFromInt(100))}},
	}
	store := affiliategormstore.New(db)
	c := New(&container.Container{
		AffiliateService: affiliateapp.NewService(store, nil, affiliateWorkerOrders{order}, affiliateWorkerProducts{}, settings),
	})

	task, err := queue.NewAffiliateOrderPaidTask(queue.AffiliateOrderPaidPayload{OrderID: order.ID})
	if err != nil {
		t.Fatalf("build task failed: %v", err)
	}
	if err := c.handleAffiliateOrderPaid(context.Background(), task); err != nil {
		t.Fatalf("worker handler failed: %v", err)
	}
	got, err := store.GetCommissionByOrderAndProfile(order.ID, profile.ID, constants.AffiliateCommissionTypeOrder)
	if err != nil || got == nil {
		t.Fatalf("expected commission created, got %+v err=%v", got, err)
	}
}
