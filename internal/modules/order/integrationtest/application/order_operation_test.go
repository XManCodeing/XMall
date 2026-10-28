package application_test

import (
	"errors"
	"sync"
	"testing"

	productgorm "github.com/XManCodeing/XMall/internal/modules/catalog/product/store/gormstore"
	usergorm "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	app "github.com/XManCodeing/XMall/internal/modules/order/application"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	ordergorm "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	promotiongorm "github.com/XManCodeing/XMall/internal/modules/promotion/infrastructure/gormstore"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
)

func TestCreateOrderOperationAtomicAndConcurrent(t *testing.T) {
	f := newCouponLimitRaceFixture(t, "telegram-operation", 100, 100)
	if err := f.db.AutoMigrate(&delivery.OrderOperation{}, &delivery.OrderSource{}, &delivery.Event{}); err != nil {
		t.Fatal(err)
	}
	store := ordergorm.New(f.db, "test-secret").EnableBusinessEvents()
	service := app.NewOrderService(app.OrderServiceOptions{OrderStore: store, UserStore: usergorm.New(f.db), ProductStore: productgorm.NewProductStore(f.db), ProductSKUStore: productgorm.NewSKUStore(f.db), CouponStore: f.couponRepo, CouponUsageStore: f.usageRepo, PromotionRepo: promotiongorm.New(f.db), Queue: f.queue})
	input := app.CreateOrderInput{UserID: f.users[0].ID, OperationKey: "bot:7:checkout:stable", RequestHash: "client-v1", Source: &delivery.Source{BotID: 7, TelegramID: 123, PrivateChatID: 123}, Items: []app.CreateOrderItem{{ProductID: f.product.ID, SKUID: f.sku.ID, Quantity: 1}}, SkipIPRiskControl: true}
	const workers = 8
	results := make(chan *orderdomain.Order, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := service.CreateOrder(input)
			if err != nil {
				failures <- err
			} else {
				results <- order
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("create: %v", err)
	}
	var id uint
	for order := range results {
		if id == 0 {
			id = order.ID
		}
		if id != order.ID {
			t.Fatalf("duplicate order %d vs %d", id, order.ID)
		}
	}
	if id == 0 {
		t.Fatal("no order")
	}
	var orders, sources, operations int64
	f.db.Model(&orderdomain.Order{}).Count(&orders)
	f.db.Model(&delivery.OrderSource{}).Count(&sources)
	f.db.Model(&delivery.OrderOperation{}).Count(&operations)
	if orders != 2 || sources != 2 || operations != 1 {
		t.Fatalf("orders=%d sources=%d operations=%d", orders, sources, operations)
	}
	if f.queue.enqueued.Load() != 1 {
		t.Fatalf("timeout jobs = %d", f.queue.enqueued.Load())
	}
	input.Items[0].Quantity = 2
	if _, err := service.CreateOrder(input); !errors.Is(err, delivery.ErrOperationConflict) {
		t.Fatalf("changed request: %v", err)
	}
	input.Items[0].Quantity = 1
	input.Source.BotID = 8
	if _, err := service.CreateOrder(input); !errors.Is(err, delivery.ErrOperationConflict) {
		t.Fatalf("changed source: %v", err)
	}
}
