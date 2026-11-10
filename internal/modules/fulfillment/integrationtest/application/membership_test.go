package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	card "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	app "github.com/XManCodeing/XMall/internal/modules/fulfillment/application"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fd "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	fs "github.com/XManCodeing/XMall/internal/modules/fulfillment/infrastructure/gormstore"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	os "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"gorm.io/gorm"
)

// membershipFixture 创建单连接 SQLite 真实订单与会员快照，返回服务、项与可信付款时间。
func membershipFixture(t *testing.T, mixed bool) (*gorm.DB, *app.Service, od.Order, od.OrderItem, time.Time) {
	t.Helper()
	db := setupFulfillmentServiceTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&fd.ItemReceipt{}, &md.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().UTC().Truncate(time.Second)
	order := od.Order{OrderNo: fmt.Sprintf("member-%d", paidAt.UnixNano()), UserID: 10, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paidAt}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := od.OrderItem{TitleJSON: jsonmap.JSON{"en-US": "test"}, OrderID: order.ID, ProductID: 21, SKUID: 22, Quantity: 1, FulfillmentType: constants.FulfillmentTypeManual}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := md.OrderItemSnapshot{OrderItemID: item.ID, ProductID: item.ProductID, SKUID: item.SKUID, AccountID: order.UserID, BeneficiaryTelegramID: 123, IdentityVersion: 1, ConfigVersion: 1, Kind: md.TermMonths, Months: 2, DurationSeconds: 5184000, Quantity: 1, Targets: []md.SnapshotTarget{{TargetID: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -123}}}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if mixed {
		ordinary := od.OrderItem{TitleJSON: jsonmap.JSON{"en-US": "test"}, OrderID: order.ID, ProductID: 31, SKUID: 32, Quantity: 1, FulfillmentType: constants.FulfillmentTypeManual}
		if err := db.Create(&ordinary).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, app.New(app.Options{OrderStore: os.New(db, "test-guest-credential-secret-with-32-bytes"), FulfillmentStore: fs.New(db)}), order, item, paidAt
}

// completeMember 在真实账本事务边界模拟权益已落库后的系统完成调用。
func completeMember(db *gorm.DB, ids []uint, paidAt time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error { return fs.New(tx).CompleteMembership(context.Background(), ids, paidAt) })
}

// assertOrderStatus 从数据库读取最终商城状态，任何查询错误立即失败。
func assertOrderStatus(t *testing.T, db *gorm.DB, id uint, status string) {
	t.Helper()
	var order od.Order
	if err := db.First(&order, id).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != status {
		t.Fatalf("order %d status=%s want=%s", id, order.Status, status)
	}
}

// TestMembershipReceiptIdempotencyAndTransaction 验证无管理员系统交付、重放及回滚。
func TestMembershipReceiptIdempotencyAndTransaction(t *testing.T) {
	db, service, order, item, paidAt := membershipFixture(t, false)
	if err := fs.New(db).CompleteMembership(context.Background(), []uint{item.ID}, paidAt); !errors.Is(err, fc.ErrTransactionRequired) {
		t.Fatalf("nontransaction: %v", err)
	}
	if _, err := service.CreateManual(app.CreateManualInput{OrderID: order.ID, AdminID: 1, Payload: "fake entitlement"}); err == nil {
		t.Fatal("manual bypass allowed")
	}
	rollback := errors.New("rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := fs.New(tx).CompleteMembership(context.Background(), []uint{item.ID}, paidAt); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertOrderStatus(t, db, order.ID, constants.OrderStatusPaid)
	for range 2 {
		if err := completeMember(db, []uint{item.ID, item.ID}, paidAt); err != nil {
			t.Fatal(err)
		}
	}
	assertOrderStatus(t, db, order.ID, constants.OrderStatusDelivered)
	var count int64
	if err := db.Model(&fd.ItemReceipt{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("receipts=%d", count)
	}
	var fulfillment fd.Fulfillment
	if err := db.First(&fulfillment).Error; err != nil {
		t.Fatal(err)
	}
	if fulfillment.DeliveredBy != nil || fulfillment.Payload != "" || fulfillment.Status != fd.ReceiptEntitlementRecorded {
		t.Fatalf("fake system delivery: %+v", fulfillment)
	}
}

// TestMembershipMixedManualBothOrders 验证人工先/会员先均不会提前完成同一叶单。
func TestMembershipMixedManualBothOrders(t *testing.T) {
	for _, memberFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(memberFirst), func(t *testing.T) {
			db, service, order, item, paidAt := membershipFixture(t, true)
			manual := func() {
				if _, err := service.CreateManual(app.CreateManualInput{OrderID: order.ID, AdminID: 8, Payload: "actual manual delivery"}); err != nil {
					t.Fatal(err)
				}
			}
			member := func() {
				if err := completeMember(db, []uint{item.ID}, paidAt); err != nil {
					t.Fatal(err)
				}
			}
			if memberFirst {
				member()
			} else {
				manual()
			}
			assertOrderStatus(t, db, order.ID, constants.OrderStatusFulfilling)
			if memberFirst {
				manual()
			} else {
				member()
			}
			assertOrderStatus(t, db, order.ID, constants.OrderStatusDelivered)
			var receipts []fd.ItemReceipt
			if err := db.Find(&receipts).Error; err != nil {
				t.Fatal(err)
			}
			if len(receipts) != 2 {
				t.Fatalf("receipts=%d", len(receipts))
			}
		})
	}
}

// TestMembershipRejectsUntrustedFacts 验证伪造项、未付款、取消、退款和快照损坏。
func TestMembershipRejectsUntrustedFacts(t *testing.T) {
	for _, scenario := range []string{"unknown", "unpaid", "canceled", "refund", "snapshot", "time"} {
		t.Run(scenario, func(t *testing.T) {
			db, _, order, item, paidAt := membershipFixture(t, false)
			id := item.ID
			var err error
			switch scenario {
			case "unknown":
				id += 1000
			case "unpaid":
				err = db.Model(&order).Update("status", constants.OrderStatusPendingPayment).Error
			case "canceled":
				err = db.Model(&order).Update("canceled_at", paidAt).Error
			case "refund":
				err = db.Model(&order).Update("refunded_amount", 1).Error
			case "snapshot":
				err = db.Model(&md.OrderItemSnapshot{}).Where("order_item_id = ?", id).Update("account_id", 999).Error
			case "time":
				paidAt = paidAt.Add(time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := completeMember(db, []uint{id}, paidAt); err == nil {
				t.Fatal("invalid facts accepted")
			}
			var count int64
			if err := db.Model(&fd.ItemReceipt{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("partial receipt persisted")
			}
		})
	}
}

// TestMembershipParentAggregation 验证无通知队列时父单同事务聚合，不受子项交付顺序影响。
func TestMembershipParentAggregation(t *testing.T) {
	db, service, order, item, paidAt := membershipFixture(t, false)
	parent := od.Order{OrderNo: "parent", UserID: order.UserID, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paidAt}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&order).Update("parent_id", parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	child := od.Order{OrderNo: "ordinary-child", ParentID: &parent.ID, UserID: order.UserID, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paidAt}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	ordinary := od.OrderItem{TitleJSON: jsonmap.JSON{"en-US": "test"}, OrderID: child.ID, ProductID: 31, SKUID: 32, Quantity: 1, FulfillmentType: constants.FulfillmentTypeManual}
	if err := db.Create(&ordinary).Error; err != nil {
		t.Fatal(err)
	}
	if err := completeMember(db, []uint{item.ID}, paidAt); err != nil {
		t.Fatal(err)
	}
	assertOrderStatus(t, db, parent.ID, constants.OrderStatusPartiallyDelivered)
	if _, err := service.CreateManual(app.CreateManualInput{OrderID: child.ID, AdminID: 8, Payload: "delivered ordinary"}); err != nil {
		t.Fatal(err)
	}
	assertOrderStatus(t, db, parent.ID, constants.OrderStatusDelivered)
}

// TestMembershipManualCardMixedAllPermutations 覆盖同叶会员、人工、卡密的全部先后顺序。
func TestMembershipManualCardMixedAllPermutations(t *testing.T) {
	for _, sequence := range []string{"mca", "mac", "cma", "cam", "amc", "acm"} {
		t.Run(sequence, func(t *testing.T) {
			db, service, order, item, paidAt := membershipFixture(t, true)
			cardItem := od.OrderItem{TitleJSON: jsonmap.JSON{"en-US": "card"}, OrderID: order.ID, ProductID: 41, SKUID: 42, Quantity: 1, FulfillmentType: constants.FulfillmentTypeAuto}
			if err := db.Create(&cardItem).Error; err != nil {
				t.Fatal(err)
			}
			secret := card.Secret{ProductID: 41, SKUID: 42, Secret: "test-real-card", Status: card.StatusAvailable}
			if err := db.Create(&secret).Error; err != nil {
				t.Fatal(err)
			}
			for index, action := range sequence {
				var err error
				switch action {
				case 'm':
					err = completeMember(db, []uint{item.ID}, paidAt)
				case 'a':
					_, err = service.CreateManual(app.CreateManualInput{OrderID: order.ID, AdminID: 8, Payload: "manual-result"})
				case 'c':
					_, err = service.CreateAuto(order.ID)
				}
				if err != nil {
					t.Fatalf("%c: %v", action, err)
				}
				want := constants.OrderStatusFulfilling
				if index == 2 {
					want = constants.OrderStatusDelivered
				}
				assertOrderStatus(t, db, order.ID, want)
			}
			var result fd.Fulfillment
			if err := db.First(&result).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Payload, "manual-result") || !strings.Contains(result.Payload, "test-real-card") {
				t.Fatalf("lost delivery: %s", result.Payload)
			}
			var count int64
			if err := db.Model(&fd.ItemReceipt{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 3 {
				t.Fatalf("receipt count=%d", count)
			}
		})
	}
}
