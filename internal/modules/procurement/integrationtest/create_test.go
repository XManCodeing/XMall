package procurement_test

import (
	"context"
	"errors"
	procurementstore "github.com/XManCodeing/XMall/internal/modules/procurement/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/modules/siteconnection/application"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"

	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"

	"github.com/XManCodeing/XMall/internal/constants"
)

func TestRecoveryContextReachesProcurementQueries(t *testing.T) {
	for _, table := range []string{"orders", "procurement_orders", "product_mappings", "site_connections", "sku_mappings"} {
		t.Run(table, func(t *testing.T) {
			db := setupProcurementTestDB(t)
			order := createProcTestOrder(t, db, "CTX-RECOVERY", "paid", "upstream")
			pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
			if err := db.Create(pm).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201}).Error; err != nil {
				t.Fatal(err)
			}
			conn := newTestSiteConnectionService(db, "test-key", t.TempDir())
			if _, err := conn.Create(application.CreateInput{Name: "test", BaseURL: "https://invalid.example", ApiKey: "test-key", ApiSecret: "test-secret", Protocol: constants.ConnectionProtocolDujiaoNext}); err != nil {
				t.Fatal(err)
			}
			svc := newTestProcurementService(db, conn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := make(chan struct{})
			if err := db.Callback().Query().Before("gorm:query").Register("test:cancel", func(tx *gorm.DB) {
				if tx.Statement.Table != table {
					return
				}
				close(reached)
				<-tx.Statement.Context.Done()
			}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- svc.CreateForOrderContext(ctx, order.ID) }()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("query not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("query did not observe recovery cancellation")
			}
		})
	}
}

func TestMissingPaidProcurementCompensationCandidates(t *testing.T) {
	db := setupProcurementTestDB(t)
	paid := createProcTestOrder(t, db, "MISSING-PAID", "paid", "upstream")
	_ = createProcTestOrder(t, db, "MISSING-REFUNDED", "refunded", "upstream")
	_ = createProcTestOrder(t, db, "MISSING-AUTO", "paid", "auto")
	ids, err := procurementstore.New(db).ListMissingProcurement(200)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != paid.ID {
		t.Fatalf("wrong repair candidates: %v", ids)
	}
}

func TestAtomicConcurrentProcurementCreate(t *testing.T) {
	db := setupProcurementTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	repo := procurementstore.New(db)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			errs <- repo.Create(&ProcurementOrder{ConnectionID: 1, LocalOrderID: 99, LocalOrderNo: "SAME", Status: "pending", Currency: "USD"})
		})
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrExists) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("created %d rows", successes)
	}
}

// ── CreateForOrder tests ──

func TestCreateForOrder_SkipsNonUpstreamItems(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SKIP-001", constants.OrderStatusPaid, constants.FulfillmentTypeAuto)

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	svc := newTestProcurementService(db, connSvc)

	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("CreateForOrder: %v", err)
	}

	// 验证没有创建采购单
	var count int64
	db.Model(&ProcurementOrder{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no procurement orders for auto fulfillment, got %d", count)
	}
}

func TestCreateForOrder_IdempotentSkipsDuplicate(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-DUP-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	_, err := connSvc.Create(application.CreateInput{Name: "snapshot-test", BaseURL: "https://invalid.example", ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext})
	if err != nil {
		t.Fatal(err)
	}
	db.Create(&mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201})
	svc := newTestProcurementService(db, connSvc)

	// 第一次创建成功
	if err := svc.CreateForOrder(order.ID); err != nil {
		t.Fatalf("first CreateForOrder: %v", err)
	}

	// 第二次应该返回 ErrExists
	err = svc.CreateForOrder(order.ID)
	if err != ErrExists {
		t.Errorf("expected ErrExists on duplicate, got: %v", err)
	}
}
