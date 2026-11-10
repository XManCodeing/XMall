package gormstore

import (
	"fmt"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"testing"
	"time"

	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"

	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"

	usercontract "github.com/XManCodeing/XMall/internal/modules/identity/user/contract"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"

	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMembershipOrderScopeOnlySnapshots 验证无快照历史普通单不被当前商品配置误标，父子范围保持只读。
func TestMembershipOrderScopeOnlySnapshots(t *testing.T) {
	_, store, db := setupAdminSearchRepositoryTest(t)
	if err := db.AutoMigrate(&membership.ProductConfig{}, &membership.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	parentID := uint(1)
	orders := []orderdomain.Order{{ID: 1, OrderNo: "parent"}, {ID: 2, OrderNo: "member-child", ParentID: &parentID}, {ID: 3, OrderNo: "ordinary-child", ParentID: &parentID}, {ID: 4, OrderNo: "historical-ordinary"}}
	for index := range orders {
		if err := db.Create(&orders[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []orderdomain.OrderItem{{ID: 11, OrderID: 2, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Member"}}, {ID: 12, OrderID: 3, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Member"}}, {ID: 13, OrderID: 4, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Member"}}} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&membership.ProductConfig{ProductID: 88, Version: 1, Quantity: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&membership.OrderItemSnapshot{OrderItemID: 11, ProductID: 88, Quantity: 1, Targets: []membership.SnapshotTarget{}}).Error; err != nil {
		t.Fatal(err)
	}
	scoped := store.MembershipOnly()
	rows, total, err := scoped.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != parentID || len(rows[0].Children) != 2 {
		t.Fatalf("scope: %d %+v %v", total, rows, err)
	}
	for _, id := range []uint{3, 4} {
		found, err := scoped.GetByID(id)
		if err != nil || found != nil {
			t.Fatalf("ordinary detail leaked %d %+v %v", id, found, err)
		}
	}
	if err := db.Delete(&membership.ProductConfig{}, "product_id = 88").Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{1, 2} {
		found, err := scoped.GetByID(id)
		if err != nil || found == nil {
			t.Fatalf("snapshot history hidden %d %v", id, err)
		}
	}
	ordinary, err := store.GetByID(4)
	if err != nil || ordinary == nil {
		t.Fatalf("shared store mutated: %v", err)
	}
}

// TestOrdinaryAdminOrdersExcludeMembershipBeforePagination 验证互补快照范围在计数、分页、搜索前生效。
// t 使用隔离 SQLite；当前商品配置不影响历史归类，混合父单不能通过普通商品搜索泄露。
func TestOrdinaryAdminOrdersExcludeMembershipBeforePagination(t *testing.T) {
	_, store, db := setupAdminSearchRepositoryTest(t)
	if err := db.AutoMigrate(&membership.ProductConfig{}); err != nil {
		t.Fatal(err)
	}
	parentID := uint(1)
	orders := []orderdomain.Order{
		{ID: 1, OrderNo: "mixed-parent", UserID: 42, Status: "paid"},
		{ID: 2, OrderNo: "member-child", ParentID: &parentID, UserID: 42, Status: "paid"},
		{ID: 3, OrderNo: "ordinary-child", ParentID: &parentID, UserID: 42, Status: "paid"},
		{ID: 4, OrderNo: "historical-ordinary", UserID: 42, Status: "paid"},
		{ID: 5, OrderNo: "member-root", UserID: 42, Status: "paid"},
		{ID: 6, OrderNo: "ordinary-root", UserID: 42, Status: "paid"},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatal(err)
	}
	items := []orderdomain.OrderItem{
		{ID: 11, OrderID: 2, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Membership"}},
		{ID: 12, OrderID: 3, ProductID: 89, TitleJSON: jsonmap.JSON{"en-US": "Ordinary goods"}},
		{ID: 13, OrderID: 4, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Historical goods"}},
		{ID: 14, OrderID: 5, ProductID: 88, TitleJSON: jsonmap.JSON{"en-US": "Membership"}},
		{ID: 15, OrderID: 6, ProductID: 89, TitleJSON: jsonmap.JSON{"en-US": "Ordinary goods"}},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	for _, itemID := range []uint{11, 14} {
		if err := db.Create(&membership.OrderItemSnapshot{OrderItemID: itemID, ProductID: 88, Quantity: 1, Targets: []membership.SnapshotTarget{}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&membership.ProductConfig{ProductID: 88, Version: 1, Quantity: 1, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	for page, expectedID := range []uint{6, 4} {
		rows, total, err := store.ListAdmin(ordercontract.ListFilter{Page: page + 1, PageSize: 1, UserID: 42, Status: "paid"})
		if err != nil || total != 2 || len(rows) != 1 || rows[0].ID != expectedID {
			t.Fatalf("page %d: rows=%+v total=%d err=%v", page+1, rows, total, err)
		}
	}
	for _, orderNo := range []string{"mixed-parent", "member-root", "member-child", "ordinary-child"} {
		rows, total, err := store.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20, OrderNo: orderNo})
		if err != nil || total != 0 || len(rows) != 0 {
			t.Fatalf("order search leaked %s: rows=%+v total=%d err=%v", orderNo, rows, total, err)
		}
	}
	rows, total, err := store.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20, ProductKeyword: "Ordinary goods"})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != 6 {
		t.Fatalf("ordinary child search leaked mixed root: rows=%+v total=%d err=%v", rows, total, err)
	}
	// 移除当前会员配置后，历史会员范围仍然不变。
	if err := db.Delete(&membership.ProductConfig{}, "product_id = 88").Error; err != nil {
		t.Fatal(err)
	}
	for name, view := range map[string]*Store{"ordinary": store, "membership": store.MembershipOnly()} {
		want := int64(2)
		rows, total, err := view.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20})
		if err != nil || total != want || int64(len(rows)) != want {
			t.Fatalf("%s after config deletion: rows=%+v total=%d err=%v", name, rows, total, err)
		}
	}
	// 用户/Bot 使用的查询和通用详情不受普通后台列表隔离影响。
	rows, total, err = store.ListByUser(ordercontract.ListFilter{Page: 1, PageSize: 20, UserID: 42})
	if err != nil || total != 4 || len(rows) != 4 {
		t.Fatalf("user orders changed: total=%d err=%v", total, err)
	}
	order, err := store.GetByOrderNoAndUser("mixed-parent", 42)
	if err != nil || order == nil || len(order.Children) != 2 {
		t.Fatalf("user/Bot detail changed: order=%+v err=%v", order, err)
	}
}

func setupAdminSearchRepositoryTest(t *testing.T) (*userstore.Store, *Store, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:admin_search_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&userdomain.User{}, &externalidentitydomain.Identity{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &membership.OrderItemSnapshot{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	return userstore.New(db), New(db, "test-guest-credential-secret-with-32-bytes"), db
}

// TestMembershipIdentityPagination verifies beneficiary history after rebinding and before pagination.
func TestMembershipIdentityPagination(t *testing.T) {
	_, store, db := setupAdminSearchRepositoryTest(t)
	parent := uint(1)
	facts := []any{
		&orderdomain.Order{ID: 1, OrderNo: "root", UserID: 9}, &orderdomain.Order{ID: 2, OrderNo: "child", ParentID: &parent, UserID: 9},
		&orderdomain.Order{ID: 3, OrderNo: "second", UserID: 10}, &orderdomain.Order{ID: 4, OrderNo: "rebound", UserID: 9},
		&orderdomain.OrderItem{TitleJSON: jsonmap.JSON{}, ID: 21, OrderID: 2}, &orderdomain.OrderItem{TitleJSON: jsonmap.JSON{}, ID: 31, OrderID: 3}, &orderdomain.OrderItem{TitleJSON: jsonmap.JSON{}, ID: 41, OrderID: 4},
		&membership.OrderItemSnapshot{OrderItemID: 21, BeneficiaryTelegramID: 12345, Targets: []membership.SnapshotTarget{}},
		&membership.OrderItemSnapshot{OrderItemID: 31, BeneficiaryTelegramID: 12345, Targets: []membership.SnapshotTarget{}},
		&membership.OrderItemSnapshot{OrderItemID: 41, BeneficiaryTelegramID: 99999, Targets: []membership.SnapshotTarget{}},
		&externalidentitydomain.Identity{UserID: 9, Provider: "telegram", ProviderUserID: "99999", Username: "new_user"},
		&externalidentitydomain.Identity{UserID: 10, Provider: "telegram", ProviderUserID: "12345", Username: "old_user"},
		&externalidentitydomain.Identity{UserID: 11, Provider: "github", ProviderUserID: "99999", Username: "old_user"},
	}
	for _, fact := range facts {
		if err := db.Create(fact).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, keyword := range []string{"12345", "old_user", "@OLD_USER"} {
		for page, expected := range []uint{3, 1} {
			rows, total, err := store.MembershipOnly().ListAdmin(ordercontract.ListFilter{UserKeyword: keyword, Page: page + 1, PageSize: 1})
			if err != nil || total != 2 || len(rows) != 1 || rows[0].ID != expected {
				t.Fatalf("%s page %d total=%d rows=%+v err=%v", keyword, page, total, rows, err)
			}
		}
	}
	if err := db.Where("provider_user_id = ?", "12345").Delete(&externalidentitydomain.Identity{}).Error; err != nil {
		t.Fatal(err)
	}
	rows, total, err := store.MembershipOnly().ListAdmin(ordercontract.ListFilter{UserKeyword: "12345", Page: 1, PageSize: 10})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("history lost: %d %v", total, err)
	}
}

func TestUserRepositoryListSupportsOAuthKeyword(t *testing.T) {
	userRepo, _, db := setupAdminSearchRepositoryTest(t)

	user1 := userdomain.User{Email: "telegram_6059928735@login.local", PasswordHash: "hash", DisplayName: "TG Buyer", Status: "active"}
	user2 := userdomain.User{Email: "normal@example.com", PasswordHash: "hash", DisplayName: "Normal User", Status: "active"}
	if err := db.Create(&user1).Error; err != nil {
		t.Fatalf("create user1 failed: %v", err)
	}
	if err := db.Create(&user2).Error; err != nil {
		t.Fatalf("create user2 failed: %v", err)
	}
	if err := db.Create(&externalidentitydomain.Identity{
		UserID:         user1.ID,
		Provider:       "telegram",
		ProviderUserID: "6059928735",
		Username:       "tg_demo_user",
	}).Error; err != nil {
		t.Fatalf("create oauth identity failed: %v", err)
	}

	users, total, err := userRepo.List(usercontract.ListFilter{Page: 1, PageSize: 20, Keyword: "tg_demo_user"})
	if err != nil {
		t.Fatalf("list by username failed: %v", err)
	}
	if total != 1 || len(users) != 1 || users[0].ID != user1.ID {
		t.Fatalf("unexpected result by username: total=%d users=%+v", total, users)
	}

	users, total, err = userRepo.List(usercontract.ListFilter{Page: 1, PageSize: 20, Keyword: "6059928735"})
	if err != nil {
		t.Fatalf("list by provider user id failed: %v", err)
	}
	if total != 1 || len(users) != 1 || users[0].ID != user1.ID {
		t.Fatalf("unexpected result by provider user id: total=%d users=%+v", total, users)
	}

	users, total, err = userRepo.List(usercontract.ListFilter{Page: 1, PageSize: 20, UserID: user2.ID})
	if err != nil {
		t.Fatalf("list by user id failed: %v", err)
	}
	if total != 1 || len(users) != 1 || users[0].ID != user2.ID {
		t.Fatalf("unexpected result by user id: total=%d users=%+v", total, users)
	}
}

func TestOrderRepositoryListAdminSupportsUserKeyword(t *testing.T) {
	_, orderRepo, db := setupAdminSearchRepositoryTest(t)

	user := userdomain.User{Email: "telegram_6059928735@login.local", PasswordHash: "hash", DisplayName: "TG Buyer", Status: "active"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	if err := db.Create(&externalidentitydomain.Identity{
		UserID:         user.ID,
		Provider:       "telegram",
		ProviderUserID: "6059928735",
		Username:       "tg_demo_user",
	}).Error; err != nil {
		t.Fatalf("create oauth identity failed: %v", err)
	}

	order := orderdomain.Order{
		OrderNo:   "DJ-ORDER-TG-001",
		UserID:    user.ID,
		Status:    "paid",
		Currency:  "USD",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}

	rows, total, err := orderRepo.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20, UserKeyword: "tg_demo_user"})
	if err != nil {
		t.Fatalf("list orders by oauth username failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != order.ID {
		t.Fatalf("unexpected result by oauth username: total=%d rows=%+v", total, rows)
	}

	rows, total, err = orderRepo.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 20, UserKeyword: "6059928735"})
	if err != nil {
		t.Fatalf("list orders by provider user id failed: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != order.ID {
		t.Fatalf("unexpected result by provider user id: total=%d rows=%+v", total, rows)
	}
}
