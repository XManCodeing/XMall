package abuse_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	bridge "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess"
	"github.com/XManCodeing/XMall/internal/constants"
	external "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	externalstore "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/infrastructure/gormstore"
	users "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	orders "github.com/XManCodeing/XMall/internal/modules/order/domain"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	groupstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	membershipapp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	membershipcontract "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestOrderRouteProjectionWithoutInvite proves original snapshots remain immutable and
// old orders, but not new purchases, receive the replacement information without an invite.
func TestOrderRouteProjectionWithoutInvite(t *testing.T) {
	bot, actor, db := orderProjectionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var before []membership.OrderItemSnapshot
	orderMust(t, db.Order("order_item_id").Find(&before).Error)
	old, err := bot.Targets(ctx, actor, 4, actor.UserID)
	orderMust(t, err)
	if len(old) != 1 || old[0].TargetID != "2" || old[0].Title != "Observed current chat" || !old[0].Replaced || !old[0].CanRequest {
		t.Fatalf("old order lost current entry: %+v", old)
	}
	if !reflect.DeepEqual(old[0].HistoricalChats, []string{"-10 (routeVersion=4)"}) {
		t.Fatalf("unexpected or duplicate history: %+v", old)
	}
	current, err := bot.Targets(ctx, actor, 6, actor.UserID)
	orderMust(t, err)
	if len(current) != 1 || current[0].Replaced || len(current[0].HistoricalChats) != 0 || !current[0].CanRequest {
		t.Fatalf("new purchase borrowed old order history: %+v", current)
	}
	var after []membership.OrderItemSnapshot
	orderMust(t, db.Order("order_item_id").Find(&after).Error)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read projection modified immutable snapshots")
	}
	var invites int64
	orderMust(t, db.Model(&membership.AccessInvite{}).Count(&invites).Error)
	if invites != 0 {
		t.Fatal("projection requested an invitation")
	}
}

// TestOrderProjectionRejectsWrongScope checks ownership, identity and current Bot/purpose
// boundaries; an active recovery cannot grant a request button through the Bot projection.
func TestOrderProjectionRejectsWrongScope(t *testing.T) {
	for _, scenario := range []string{"owner", "foreign_order", "beneficiary", "bot", "purpose", "recovery"} {
		t.Run(scenario, func(t *testing.T) {
			bot, actor, db := orderProjectionFixture(t)
			owner := actor.UserID
			switch scenario {
			case "owner":
				owner++
			case "foreign_order":
				orderMust(t, db.Model(&orders.Order{}).Where("id = 4").Update("user_id", 8).Error)
			case "beneficiary":
				actor.TelegramID++
			case "bot":
				orderMust(t, db.Model(&groups.Target{}).Where("id = 2").Update("bot_instance_id", 8).Error)
			case "purpose":
				orderMust(t, db.Model(&groups.Target{}).Where("id = 2").Update("purpose", "notification").Error)
			case "recovery":
				orderMust(t, db.Model(&groups.Target{}).Where("id = 2").Update("recovery_task_id", 3).Error)
			}
			rows, err := bot.Targets(context.Background(), actor, 4, owner)
			if scenario == "recovery" {
				orderMust(t, err)
				if len(rows) != 1 || rows[0].CanRequest {
					t.Fatalf("recovery bypass: %+v", rows)
				}
			} else if err == nil && len(rows) != 0 {
				t.Fatalf("scope disclosed: %+v", rows)
			}
			if scenario == "bot" || scenario == "purpose" {
				page, err := bot.List(context.Background(), actor, 0, 20)
				orderMust(t, err)
				if len(page.Items) != 0 {
					t.Fatalf("list scope disclosed: %+v", page)
				}
			}
		})
	}
}

// TestBotRepurchaseKeepsCurrentRightsAndInviteHistory exercises production read/queue services.
// t owns an isolated SQLite database; returns no value and never calls a platform or payment API.
func TestBotRepurchaseKeepsCurrentRightsAndInviteHistory(t *testing.T) {
	bot, actor, db := orderProjectionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, now := memberstore.New(db), time.Now().UTC()
	orderMust(t, db.Create(&membership.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 3}).Error)
	orderMust(t, db.Create(&membership.LedgerRevocation{OrderItemID: 5, TargetID: 2, BeneficiaryTelegramID: 42, RevokedAt: now.Add(-time.Minute)}).Error)
	orderMust(t, db.Model(&orders.Order{}).Where("id = 4").Update("status", constants.OrderStatusRefunded).Error)
	orderMust(t, db.Create(&membership.LedgerSource{ID: 2, TargetID: 2, AccountID: 3, OrderID: 6, OrderItemID: 7, Operation: "grant", BeneficiaryTelegramID: 42, PaidAt: now.Add(-time.Second), Kind: membership.TermMonths, Months: 2}).Error)
	old := membership.AccessInvite{ID: 1, AccountID: 3, TelegramID: 42, TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, Generation: 1, BindingVersion: 1, EntitlementVersion: 1, SourceID: 1, State: "revoked", Term: membership.InviteTermLongTerm}
	orderMust(t, db.Create(&old).Error)
	orderMust(t, db.Create(&membership.AccessSlot{TelegramID: 42, TargetID: 2, InviteID: old.ID, Version: 1}).Error)
	routes := &repurchaseRoutes{route: membershipcontract.AccessRoute{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, Generation: 1}}
	service := &membershipapp.AccessService{Store: store, Authorizer: bot.Website.Identity, Routes: routes}
	bot.Website.Access, bot.Website.Invites, bot.Deliveries = service, store, routes
	for _, orderID := range []uint{4, 6} {
		rows, err := bot.Targets(ctx, actor, orderID, actor.UserID)
		orderMust(t, err)
		if len(rows) != 1 || rows[0].State != "available" || !rows[0].CanRequest {
			t.Fatalf("order %d borrowed historical revocation: %+v", orderID, rows)
		}
	}
	page, err := bot.List(ctx, actor, 0, 10)
	orderMust(t, err)
	entry, err := bot.GetExisting(ctx, actor, 2)
	orderMust(t, err)
	if len(page.Items) != 1 || page.Items[0].State != "available" || entry.State != "revoked" || entry.InviteURL != "" {
		t.Fatalf("rights and invitation lost independent meaning: %+v %+v", page, entry)
	}
	var operations int64
	orderMust(t, db.Model(&membership.AccessOperation{}).Count(&operations).Error)
	if operations != 0 {
		t.Fatal("read-only projection created access work")
	}
	requested, err := bot.Request(ctx, actor, 2)
	orderMust(t, err)
	if requested.State != "processing" || requested.InviteURL != "" {
		t.Fatalf("explicit repurchase request failed: %+v", requested)
	}
	orderMust(t, db.Model(&membership.AccessOperation{}).Count(&operations).Error)
	if operations != 1 {
		t.Fatalf("request queued %d operations", operations)
	}
	orderMust(t, db.Create(&membership.LedgerRevocation{OrderItemID: 7, TargetID: 2, BeneficiaryTelegramID: 42, RevokedAt: now}).Error)
	rows, err := bot.Targets(ctx, actor, 6, actor.UserID)
	orderMust(t, err)
	entry, err = bot.GetExisting(ctx, actor, 2)
	orderMust(t, err)
	if len(rows) != 1 || rows[0].State != "revoked" || rows[0].CanRequest || entry.State != "unavailable" || entry.InviteURL != "" {
		t.Fatalf("real revocation must remain authoritative: %+v %+v", rows, entry)
	}
	if _, err := bot.Request(ctx, actor, 2); err == nil {
		t.Fatal("fully revoked entitlement was authorized")
	}
}

// TestBotMissingInvitationRemainsRequestable distinguishes no invitation from pending work.
// t owns a synthetic fixture; returns no value and queues locally without platform calls.
func TestBotMissingInvitationRemainsRequestable(t *testing.T) {
	bot, actor, db := orderProjectionFixture(t)
	store := memberstore.New(db)
	orderMust(t, db.Create(&membership.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 1}).Error)
	routes := &repurchaseRoutes{route: membershipcontract.AccessRoute{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, Generation: 1}}
	bot.Website.Access = &membershipapp.AccessService{Store: store, Authorizer: bot.Website.Identity, Routes: routes}
	bot.Website.Invites, bot.Deliveries = store, routes
	entry, err := bot.GetExisting(context.Background(), actor, 2)
	orderMust(t, err)
	if entry.State != "unavailable" || entry.InviteURL != "" {
		t.Fatalf("missing invite fabricated pending work: %+v", entry)
	}
	requested, err := bot.Request(context.Background(), actor, 2)
	orderMust(t, err)
	if requested.State != "processing" {
		t.Fatalf("explicit request did not enqueue: %+v", requested)
	}
}

// repurchaseRoutes returns only the isolated fixture route, without external discovery.
type repurchaseRoutes struct {
	route membershipcontract.AccessRoute
}

// MembershipRoute takes the synthetic target and returns its route or a fail-closed error.
func (routes *repurchaseRoutes) MembershipRoute(_ context.Context, target uint) (membershipcontract.AccessRoute, error) {
	if target != routes.route.TargetID {
		return membershipcontract.AccessRoute{}, membershipcontract.ErrNotFound
	}
	return routes.route, nil
}

// InviteDelivery accepts synthetic bot/invite IDs and returns an untouched queue receipt.
// No message is enqueued or sent by this read-only test port.
func (*repurchaseRoutes) InviteDelivery(context.Context, uint, uint) (runtime.InviteDeliveryStatus, error) {
	return runtime.InviteDeliveryStatus{State: "not_requested"}, nil
}

// legacyOrderProjection hides the optional history port to exercise old integrations.
type legacyOrderProjection struct {
	membershipcontract.UserAccessReader
	membershipcontract.OrderAccessReader
}

// TestOrderProjectionLegacyReader preserves old adapters without guessing a history.
func TestOrderProjectionLegacyReader(t *testing.T) {
	bot, actor, db := orderProjectionFixture(t)
	store := memberstore.New(db)
	bot.Website.Reader = legacyOrderProjection{UserAccessReader: store, OrderAccessReader: store}
	rows, err := bot.Targets(context.Background(), actor, 4, actor.UserID)
	orderMust(t, err)
	if len(rows) != 1 || rows[0].Replaced || len(rows[0].HistoricalChats) != 0 || !rows[0].CanRequest {
		t.Fatalf("legacy reader incompatible: %+v", rows)
	}
}

// TestOrderProjectionRouteVersionChange checks reuse of a chat with another route version.
func TestOrderProjectionRouteVersionChange(t *testing.T) {
	bot, actor, db := orderProjectionFixture(t)
	orderMust(t, db.Model(&groups.Target{}).Where("id = 2").Update("telegram_chat_id", -10).Error)
	rows, err := bot.Targets(context.Background(), actor, 4, actor.UserID)
	orderMust(t, err)
	if len(rows) != 1 || !rows[0].Replaced || len(rows[0].HistoricalChats) != 1 {
		t.Fatalf("route generation ignored: %+v", rows)
	}
}

// orderProjectionFixture wires real read-only stores on one SQLite connection; no
// invitation service is installed. It returns the adapter, trusted actor and database.
func orderProjectionFixture(t *testing.T) (*bridge.Bot, shop.Actor, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "order.db")), &gorm.Config{})
	orderMust(t, err)
	connection, err := db.DB()
	orderMust(t, err)
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { orderMust(t, connection.Close()) })
	models := append(memberstore.AccessModels(), memberstore.LedgerModels()...)
	models = append(models, &membership.OrderItemSnapshot{}, &users.User{}, &external.Identity{}, &groups.Target{}, &orders.Order{}, &orders.OrderItem{})
	orderMust(t, db.AutoMigrate(models...))
	now, paid := time.Now().UTC(), time.Now().UTC().Add(-time.Hour)
	orderMust(t, db.Create(&users.User{ID: 3, Email: "order@example.invalid", Status: constants.UserStatusActive}).Error)
	orderMust(t, db.Create(&external.Identity{ID: 1, UserID: 3, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "42"}).Error)
	orderMust(t, db.Create(&groups.Target{ID: 2, Name: "Logical old label", Purpose: "membership", Enabled: true, RouteVersion: 5, BotInstanceID: 1, TelegramChatID: -20, OccupancySlot: "chat:-20", Observation: groups.Observation{Title: "Observed current chat", RouteVersion: 5, ChatType: "supergroup", MemberStatus: "administrator", State: "ready", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, ObservedAt: &now}}).Error)
	orderMust(t, db.Create(&membership.LedgerSource{ID: 1, TargetID: 2, AccountID: 3, OrderID: 4, OrderItemID: 5, Operation: "grant", BeneficiaryTelegramID: 42, PaidAt: paid, Kind: membership.TermMonths, Months: 2}).Error)
	for _, orderID := range []uint{4, 6} {
		route := membership.SnapshotTarget{TargetID: 2, BotInstanceID: 1, TelegramChatID: -10, RouteVersion: 4}
		if orderID == 6 {
			route.TelegramChatID, route.RouteVersion = -20, 5
		}
		orderMust(t, db.Create(&orders.Order{ID: orderID, UserID: 3, OrderNo: fmt.Sprintf("route-order-%d", orderID), Status: constants.OrderStatusPaid, PaidAt: &paid, Currency: "CNY"}).Error)
		orderMust(t, db.Create(&orders.OrderItem{ID: orderID + 1, OrderID: orderID, ProductID: 9, SKUID: 9, Quantity: 1, TitleJSON: jsonmap.JSON{}}).Error)
		orderMust(t, db.Create(&membership.OrderItemSnapshot{OrderItemID: orderID + 1, ProductID: 9, SKUID: 9, AccountID: 3, BeneficiaryTelegramID: 42, Quantity: 1, Kind: membership.TermMonths, Months: 2, Targets: []membership.SnapshotTarget{route, route}}).Error)
	}
	website := &bridge.Website{Identity: bridge.Identity{Users: userstore.New(db), Bindings: externalstore.New(db)}, Reader: memberstore.New(db), Groups: groupstore.New(db)}
	return &bridge.Bot{Website: website}, shop.Actor{BotID: 1, UserID: 3, TelegramID: 42, Generation: "1:3"}, db
}

// orderMust fails t for a fixture or projection error; it has no return value.
func orderMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
