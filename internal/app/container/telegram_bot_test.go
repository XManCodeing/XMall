package container

import (
	"context"
	"encoding/json"
	"errors"
	telegrambootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fd "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	fs "github.com/XManCodeing/XMall/internal/modules/fulfillment/infrastructure/gormstore"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	pd "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	ma "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	ms "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	supportapp "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	sc "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	sd "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	supportstore "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	wd "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"go/ast"
	"go/parser"
	"go/token"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMembershipActualRecoveryAssembly 验证真实付款事件→账本→系统履约在单连接上闭环。
// 不迁移 Bot 联系人或 Redis，不开放销售；重启恢复不能加时或伪造管理员交付。
func TestMembershipActualRecoveryAssembly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "membership.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&od.Order{}, &od.OrderItem{}, &pd.Payment{}, &wd.Transaction{}, &md.OrderItemSnapshot{}, &md.PaidEvent{}, &md.IdentityMigration{}, &md.LedgerSource{}, &md.LedgerRevocation{}, &md.LedgerProjection{}, &md.LedgerOutbox{}, &fd.Fulfillment{}, &fd.ItemReceipt{}); err != nil {
		t.Fatal(err)
	}
	paid := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	processed := paid.Add(time.Minute)
	amount := money.FromDecimal(decimal.NewFromInt(10))
	rows := []any{
		&od.Order{ID: 1, OrderNo: "synthetic-membership-parent", UserID: 7, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &processed, TotalAmount: amount, OnlinePaidAmount: amount},
		&od.OrderItem{ID: 1, OrderID: 1, ProductID: 3, SKUID: 4, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: constants.FulfillmentTypeManual},
		&pd.Payment{ID: 1, OrderID: 1, Status: constants.PaymentStatusSuccess, PaidAt: &paid, Currency: "CNY", Amount: amount},
		&md.OrderItemSnapshot{OrderItemID: 1, ProductID: 3, SKUID: 4, AccountID: 7, BeneficiaryTelegramID: 9007199254740993, IdentityVersion: 1, ConfigVersion: 1, Kind: md.TermMonths, Months: 2, DurationSeconds: 60 * 86400, Quantity: 1, Targets: []md.SnapshotTarget{{TargetID: 2, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -1001}}, CreatedAt: paid},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		store, err := ms.InTransaction(tx)
		if err != nil {
			return err
		}
		return store.AppendPaid(context.Background(), 1, 1)
	}); err != nil {
		t.Fatal(err)
	}
	old := gormdb.DB
	gormdb.DB = db
	defer func() { gormdb.DB = old }()
	container := &Container{}
	container.initMembershipRecovery()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- container.MembershipRecovery.Start(ctx) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var event md.PaidEvent
		if err := db.First(&event, 1).Error; err != nil {
			t.Fatal(err)
		}
		if event.CompletedAt != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("real ledger recovery did not complete")
		case <-ticker.C:
		}
	}
	if err := container.MembershipRecovery.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	assertMembershipAssemblyResult(t, db, paid)
}

// TestMembershipWalletEventAssembly 检验真实钱包支付记录（非零 payment ID）可被统一事件消费者授予。
func TestMembershipWalletEventAssembly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "membership-wallet.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&od.Order{}, &od.OrderItem{}, &pd.Payment{}, &wd.Transaction{}, &md.OrderItemSnapshot{}, &md.PaidEvent{}, &md.IdentityMigration{}, &md.LedgerSource{}, &md.LedgerRevocation{}, &md.LedgerProjection{}, &md.LedgerOutbox{}, &fd.Fulfillment{}, &fd.ItemReceipt{}); err != nil {
		t.Fatal(err)
	}
	paid := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	amount, id := money.FromDecimal(decimal.NewFromInt(10)), uint(1)
	rows := []any{
		&od.Order{ID: id, OrderNo: "synthetic-wallet-membership", UserID: 7, Status: constants.OrderStatusPaid, Currency: "CNY", PaidAt: &paid, TotalAmount: amount, WalletPaidAmount: amount},
		&od.OrderItem{ID: 1, OrderID: 1, ProductID: 3, SKUID: 4, Quantity: 1, TitleJSON: jsonmap.JSON{}, FulfillmentType: constants.FulfillmentTypeManual},
		&pd.Payment{ID: 1, OrderID: 1, ProviderType: constants.PaymentProviderWallet, Status: constants.PaymentStatusSuccess, PaidAt: &paid, Currency: "CNY", Amount: amount},
		&wd.Transaction{UserID: 7, OrderID: &id, Type: constants.WalletTxnTypeOrderPay, Direction: constants.WalletTxnDirectionOut, Amount: amount, Currency: "CNY", Reference: "synthetic-wallet-membership"},
		&md.OrderItemSnapshot{OrderItemID: 1, ProductID: 3, SKUID: 4, AccountID: 7, BeneficiaryTelegramID: 9007199254740993, IdentityVersion: 1, ConfigVersion: 1, Kind: md.TermMonths, Months: 2, DurationSeconds: 60 * 86400, Quantity: 1, Targets: []md.SnapshotTarget{{TargetID: 2, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -1001}}, CreatedAt: paid},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		store, err := ms.InTransaction(tx)
		if err != nil {
			return err
		}
		return store.AppendPaid(context.Background(), 1, 1)
	}); err != nil {
		t.Fatal(err)
	}
	store := ms.New(db).WithSystemFulfillment(func(tx *gorm.DB) fc.MembershipSystemStore { return fs.New(tx) })
	worker, err := ma.NewPaidWorker(store, store, time.Now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := worker.RunOnce(context.Background(), 10); err != nil || completed != 1 {
		t.Fatalf("wallet grant completed=%d err=%v", completed, err)
	}
	assertMembershipAssemblyResult(t, db, paid)
}

// assertMembershipAssemblyResult 核对 db 中真实账本、回执及商城状态，paid 为可信付款时点。
func assertMembershipAssemblyResult(t *testing.T, db *gorm.DB, paid time.Time) {
	t.Helper()
	var source md.LedgerSource
	if err := db.First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if !source.PaidAt.Equal(paid) {
		t.Fatal("grant used processing time instead of trusted paid time")
	}
	var projection md.LedgerProjection
	if err := db.First(&projection).Error; err != nil {
		t.Fatal(err)
	}
	if projection.ExpiresAt == nil || !projection.ExpiresAt.Equal(paid.Add(60*24*time.Hour)) {
		t.Fatal("expiry drift")
	}
	var receipt fd.ItemReceipt
	if err := db.First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Status != fd.ReceiptEntitlementRecorded || !receipt.PaidAt.Equal(paid) {
		t.Fatal("receipt changed grant time or claimed admission")
	}
	var fulfillment fd.Fulfillment
	if err := db.First(&fulfillment).Error; err != nil {
		t.Fatal(err)
	}
	if fulfillment.DeliveredBy != nil || fulfillment.Type != fd.SystemMembershipType {
		t.Fatal("system fulfillment impersonated admin")
	}
	var order od.Order
	if err := db.First(&order, 1).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != constants.OrderStatusDelivered {
		t.Fatalf("order remains %s", order.Status)
	}
}

func TestTelegramGroupsActualContainerAssembly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "groups.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	if err = db.AutoMigrate(&rd.Instance{}, &gd.Target{}, &gd.Route{}, &gd.Operation{}, &gd.ModuleConfig{}); err != nil {
		t.Fatal(err)
	}
	old := gormdb.DB
	gormdb.DB = db
	defer func() { gormdb.DB = old }()
	c := &Container{Config: &config.Config{}}
	c.Config.App.SecretKey = "synthetic-groups-key"
	c.initTelegramBot()
	if c.TelegramGroups == nil {
		t.Fatal("groups not assembled")
	}
	instance := rd.Instance{TelegramID: 123, Mode: "internal", State: "stopped", TokenCipher: "synthetic"}
	if err = db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	group, err := c.TelegramGroups.Save(ctx, 0, gc.Input{Name: "test", Purpose: "support", BotInstanceID: instance.ID, TelegramChatID: "-1001"})
	if !errors.Is(err, gc.ErrChatUnresolved) {
		t.Fatalf("stopped instance must not register an unverified title: %+v %v", group, err)
	}
	// Existing targets remain checkable even when their instance is stopped.
	legacy := gd.Target{Name: "legacy", Purpose: "support", BotInstanceID: instance.ID, TelegramChatID: -1001, Version: 1, RouteVersion: 1}
	if err = db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	group, err = c.TelegramGroups.Check(ctx, legacy.ID, 1, 7, "manual")
	if err != nil || group.Observation.ErrorClass != "instance_stopped" {
		t.Fatalf("stopped probe: %+v %v", group, err)
	}
	source := readContainerSource(t, "telegram_bot.go")
	for _, required := range []string{"\"my_chat_member\": c.TelegramGroups", "Subscriptions: []telegrambootstrap.UpdateSubscriptions{c.TelegramGroups, accessRuntime}", "c.TelegramGroups.Reconcile(ctx, botID)"} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing %s", required)
		}
	}
}

// TestProductDeepLinkGuardIsWired prevents working unit fixtures from hiding a missing production guard.
// t parses composition only; it never opens databases, secrets or Telegram connections.
func TestProductDeepLinkGuardIsWired(t *testing.T) {
	source := readContainerSource(t, "telegram_bot.go")
	if !strings.Contains(source, "Secret: c.Config.App.SecretKey") {
		t.Fatal("shop product-link decoder must receive the server-only application secret")
	}
	file, err := parser.ParseFile(token.NewFileSet(), "telegram_bot.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		field, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "ProductLinks" {
			return true
		}
		adapter, ok := field.Value.(*ast.CompositeLit)
		if !ok {
			return true
		}
		name, ok := adapter.Type.(*ast.SelectorExpr)
		if !ok || name.Sel.Name != "MembershipProductLinks" {
			return true
		}
		dependencies := map[string]bool{}
		for _, element := range adapter.Elts {
			dependency, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			label, ok := dependency.Key.(*ast.Ident)
			if !ok {
				continue
			}
			_, constructed := dependency.Value.(*ast.CallExpr)
			dependencies[label.Name] = constructed
		}
		found = dependencies["Configs"] && dependencies["Targets"]
		return true
	})
	if !found {
		t.Fatal("shop must receive current membership config and group target readers for product deep links")
	}
}

// Check the production composition itself, without opening a DB, Redis or secrets.
func TestTelegramRuntimeUsesUpdateDispatcherAndRetainsBusinessDispatcher(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "telegram_bot.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	updateVar := ""
	shopVar := ""
	runtimeHandler := ""
	businessShop := ""
	business := false
	ast.Inspect(file, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok {
			for i, expr := range assign.Rhs {
				call, ok := expr.(*ast.CallExpr)
				if !ok {
					continue
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if sel.Sel.Name == "NewUpdateDispatcher" && len(call.Args) == 3 {
					lhs, _ := assign.Lhs[i].(*ast.Ident)
					first, _ := call.Args[0].(*ast.Ident)
					second, _ := call.Args[1].(*ast.Ident)
					if lhs != nil && first != nil && second != nil && first.Name == second.Name {
						updateVar = lhs.Name
						shopVar = first.Name
					}
				}
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "NewRuntime":
			if len(call.Args) == 3 {
				if arg, ok := call.Args[2].(*ast.Ident); ok {
					runtimeHandler = arg.Name
				}
			}
		case "NewBusinessDispatcher":
			business = true
			if len(call.Args) > 2 {
				if arg, ok := call.Args[2].(*ast.Ident); ok {
					businessShop = arg.Name
				}
			}
		}
		return true
	})
	if updateVar == "" || runtimeHandler != updateVar {
		t.Fatal("runtime bypasses update dispatcher or missing legacy classifier")
	}
	if !business || businessShop != shopVar {
		t.Fatal("business dispatcher no longer uses original shop")
	}
}

// supportAssemblyStore records port usage without any database or HTTP calls.
type supportAssemblyStore struct {
	telegrambootstrap.SupportStore
	sc.Store
	readyScans, replyScans, claims, topics, checks, accepted int
	operation                                                sd.OperationReference
	applied                                                  int
}

// VerificationSessions provides an empty daily-card scan for this runtime assembly fixture.
// Inputs must remain fenced to the synthetic bot; production scanning is exercised by store tests.
func (store *supportAssemblyStore) VerificationSessions(_ context.Context, bot, after uint, limit int, _ time.Time) ([]sd.Session, error) {
	if bot != 1 || after != 0 || limit != 50 {
		return nil, sc.ErrDenied
	}
	return nil, nil
}

// TopicIdentity is unused by the empty daily scan and refuses unexpected lookups.
func (*supportAssemblyStore) TopicIdentity(context.Context, uint, uint) (sd.Session, error) {
	return sd.Session{}, sc.ErrDenied
}

// EnsureVerificationControl refuses unexpected creation outside the empty scan.
func (*supportAssemblyStore) EnsureVerificationControl(context.Context, uint, uint, uint64, time.Time) (*sd.OperationControl, error) {
	return nil, sc.ErrDenied
}

// ApplyVerificationOperation refuses actions not represented by this manual-menu fixture.
func (*supportAssemblyStore) ApplyVerificationOperation(context.Context, uint, rc.Update, string, uint64, time.Time) error {
	return sc.ErrDenied
}

func (store *supportAssemblyStore) ExpireTopicClaims(_ context.Context, bot uint, generation uint64, _ time.Time, limit int) error {
	if bot != 1 || generation != 7 || limit != 50 {
		return sc.ErrDenied
	}
	return nil
}

func (store *supportAssemblyStore) ScanReady(context.Context, uint, uint, int) ([]sd.BufferedMessage, error) {
	store.readyScans++
	return []sd.BufferedMessage{{ID: 1, SessionID: 2, State: "ready"}}, nil
}
func (store *supportAssemblyStore) ScanDownstream(context.Context, uint, uint, int) ([]sd.Downstream, error) {
	store.replyScans++
	return nil, nil
}
func (store *supportAssemblyStore) ClaimTopic(context.Context, uint, uint, uint, uint64, uint64, int64, string, time.Time, time.Duration) (sc.TopicClaim, error) {
	store.topics++
	return sc.TopicClaim{Operation: sd.TopicOperation{State: "succeeded"}, Mapping: &sd.TopicMapping{State: "open"}}, nil
}
func (store *supportAssemblyStore) ClaimReady(context.Context, uint, uint, uint64, string, time.Time, time.Duration) (*sd.BufferedMessage, error) {
	store.claims++
	return nil, nil
}
func (store *supportAssemblyStore) ResolveNativeReply(context.Context, uint, rc.Update, time.Time) (*sd.ReplyMapping, error) {
	return &sd.ReplyMapping{ChatID: -100, RouteVersion: 1}, nil
}
func (store *supportAssemblyStore) AcceptDownstream(context.Context, uint, rc.Update, bool, time.Time) (*sd.Downstream, error) {
	store.accepted++
	return &sd.Downstream{ID: 1}, nil
}
func (store *supportAssemblyStore) DownstreamRequest(context.Context, uint, uint, time.Time) (*rc.DeliveryRequest, error) {
	return &rc.DeliveryRequest{BotID: 1, Key: sc.DownstreamBusinessRef(1)}, nil
}
func (store *supportAssemblyStore) CheckDownstream(context.Context, uint, rc.Message, time.Time) (*sd.Downstream, error) {
	store.checks++
	return &sd.Downstream{SourceChatID: -100, ActorID: 55, RouteVersion: 1}, nil
}

// PrepareOperation records the actor-bound contract for dispatcher tests.
func (store *supportAssemblyStore) PrepareOperation(_ context.Context, bot uint, update rc.Update, version, generation uint64, action, note, ref string, now time.Time) (sd.OperationReference, error) {
	store.operation = sd.OperationReference{Ref: ref, BotID: bot, ActorID: update.UserID, ChatID: update.ChatID, ThreadID: update.ThreadID, RouteVersion: version, Generation: generation, Action: action, NoteCipher: note, ExpiresAt: now.Add(time.Minute)}
	return store.operation, nil
}

// Operation loads only the reference prepared for the current bot.
func (store *supportAssemblyStore) Operation(_ context.Context, bot uint, ref string) (sd.OperationReference, error) {
	if bot != store.operation.BotID || ref != store.operation.Ref {
		return sd.OperationReference{}, sc.ErrDenied
	}
	return store.operation, nil
}

// ApplyOperation observes service authorization; persistence CAS is tested by support.
func (store *supportAssemblyStore) ApplyOperation(context.Context, uint, rc.Update, string, time.Time) error {
	store.applied++
	return nil
}

type supportAssemblyRuntime struct {
	rc.Store
	queued int
}

func (store *supportAssemblyRuntime) Get(context.Context, uint) (*rd.Instance, error) {
	return &rd.Instance{ID: 1, Generation: 7}, nil
}
func (store *supportAssemblyRuntime) EnqueueRequest(_ context.Context, request *rc.DeliveryRequest) error {
	if request.Key != sc.DownstreamBusinessRef(1) {
		return sc.ErrDenied
	}
	store.queued++
	return nil
}

type supportAssemblyMembers struct {
	removed bool
	calls   int
}

func (members *supportAssemblyMembers) ResolveSupportRoute(context.Context, uint) (gc.SupportRoute, error) {
	return gc.SupportRoute{BotID: 1, TargetID: 1, ChatID: -100, RouteVersion: 1, Generation: 7}, nil
}
func (members *supportAssemblyMembers) AuthorizeSupportMember(context.Context, uint, int64, int64, uint64) error {
	members.calls++
	if members.removed {
		return sc.ErrDenied
	}
	return nil
}

type supportAssemblyFence struct{}

func (supportAssemblyFence) Check(_ context.Context, bot uint, generation uint64) error {
	if bot != 1 || generation != 7 {
		return rc.ErrStale
	}
	return nil
}

type supportAssemblyShop struct{ handled, notices int }

func (shop *supportAssemblyShop) Handle(context.Context, uint, rc.Update) ([]rc.Message, error) {
	shop.handled++
	return nil, nil
}
func (*supportAssemblyShop) ConsumeLegacyInput(context.Context, uint, rc.Update) ([]rc.Message, bool, error) {
	return nil, false, nil
}
func (*supportAssemblyShop) AuthorizeDelivery(context.Context, uint, int64, uint) error { return nil }
func (*supportAssemblyShop) AuthorizeStamp(context.Context, uint, int64, string) error  { return nil }
func (shop *supportAssemblyShop) AuthorizeTarget(context.Context, uint, rc.Message) error {
	shop.notices++
	return nil
}

// TestSupportProductionWiringUsesFencedGate checks the actual composition root.
func TestSupportProductionWiringUsesFencedGate(t *testing.T) {
	source := readContainerSource(t, "telegram_bot.go")
	for _, required := range []string{
		"WithReadyEncoder(supportcontract.EncodeReadyTransfer)",
		"NewSupportRuntime(supportStore, store, store, c.TelegramBotManager, c.TelegramGroups)",
		"GroupMessages: supportRuntime.Replies",
		"SupportOperations: supportRuntime.Operations",
		"SupportPrivateAuthorizer{Notices: support, Replies: supportRuntime.Replies}",
		"SupportGroupAuthorizer{Ready: supportStore, Controls: supportRuntime.Operations}",
		"support.DeliveryReady = supportstore.DeliveryCapability{Store: supportStore}",
		"WithReadiness(support.ReadyEnabled)",
		"WithPolicyRecoveryGuard",
		"supportRuntime.Reconcile(ctx, botID)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing production support wiring: %s", required)
		}
	}
	if strings.Index(source, "support.DeliveryReady =") < strings.Index(source, "if c.Config.Queue.Enabled {") {
		t.Fatal("production acceptance must require the supervised runtime")
	}
}

// TestSupportOperationCallbacksKeepActorAndNamespace exercises real service routing.
func TestSupportOperationCallbacksKeepActorAndNamespace(t *testing.T) {
	store, runtimeStore, members := &supportAssemblyStore{}, &supportAssemblyRuntime{}, &supportAssemblyMembers{}
	assembly := telegrambootstrap.NewSupportRuntime(store, runtimeStore, runtimeStore, nil, members)
	verification, shop, member := &supportAssemblyShop{}, &supportAssemblyShop{}, &supportAssemblyShop{}
	dispatch := telegrambootstrap.NewUpdateDispatcher(shop, shop, telegrambootstrap.UpdateModules{
		SupportCallbacks: verification, SupportOperations: assembly.Operations, MemberCallbacks: member,
	})
	ctx := rc.WithExecutionFence(context.Background(), supportAssemblyFence{})
	update := rc.Update{Type: "callback_query", ChatType: "supergroup", ChatID: -100, UserID: 55, ThreadID: 20, IsTopicMessage: true, CallbackID: "click"}
	if assembly.Operations.Store != store || assembly.Operations.Members != members || assembly.Operations.Now == nil {
		t.Fatal("operation dependencies missing")
	}
	intent := sc.OperationIntent{Update: update, Action: sc.OperationClose}
	if _, err := dispatch.PrepareSupportOperation(context.Background(), 1, intent); !errors.Is(err, rc.ErrStale) {
		t.Fatal("unfenced preparation", err)
	}
	data, err := dispatch.PrepareSupportOperation(ctx, 1, intent)
	if err != nil || !strings.HasPrefix(data, sc.OperationCallbackPrefix) || len(data) != len(sc.OperationCallbackPrefix)+24 {
		t.Fatal("explicit prepare entry", data, err)
	}
	update.CallbackData = data
	for _, cause := range []string{"actor", "bot", "thread", "private", "removed", "unfenced", "malformed"} {
		t.Run(cause, func(t *testing.T) {
			candidate, bot, callCtx := update, uint(1), ctx
			switch cause {
			case "actor":
				candidate.UserID++
			case "bot":
				bot++
			case "thread":
				candidate.ThreadID++
			case "private":
				candidate.Private, candidate.ChatID, candidate.ChatType = true, 55, "private"
			case "removed":
				members.removed = true
			case "unfenced":
				callCtx = context.Background()
			case "malformed":
				candidate.CallbackData = sc.OperationCallbackPrefix + "short"
			}
			messages, callErr := dispatch.Handle(callCtx, bot, candidate)
			members.removed = false
			if (callErr != nil && !errors.Is(callErr, rc.ErrStale)) || len(messages) != 0 || store.applied != 0 || verification.handled != 0 {
				t.Fatal("operation escaped authorization/namespace", messages, callErr)
			}
		})
	}
	if messages, err := dispatch.Handle(ctx, 1, update); err != nil || len(messages) != 0 || store.applied != 1 || verification.handled != 0 {
		t.Fatal("trusted operation not consumed", err)
	}
	if runtimeStore.queued != 0 {
		t.Fatal("operation emitted unauthorized message")
	}
	for _, namespace := range []string{"support:verify", "support:opx:test", "member:action", "s:shop", "unknown:action"} {
		candidate := rc.Update{Type: "callback_query", Private: true, ChatType: "private", ChatID: 55, UserID: 55, CallbackID: "private-click", CallbackData: namespace}
		if _, err := dispatch.Handle(ctx, 1, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if verification.handled != 2 || member.handled != 1 || shop.handled != 1 || store.applied != 1 {
		t.Fatal("callback namespaces crossed")
	}
	missing := telegrambootstrap.NewUpdateDispatcher(shop, shop, telegrambootstrap.UpdateModules{SupportCallbacks: verification})
	if _, err := missing.Handle(ctx, 1, update); err != nil || verification.handled != 2 {
		t.Fatal("missing operation handler fell into verification", err)
	}
	if _, err := missing.PrepareSupportOperation(ctx, 1, intent); !errors.Is(err, sc.ErrDenied) {
		t.Fatal("missing prepare handler allowed", err)
	}
}

// TestSupportPrepareExplicitActions keeps notes encrypted and rejects text inference.
func TestSupportPrepareExplicitActions(t *testing.T) {
	store, runtimeStore, members := &supportAssemblyStore{}, &supportAssemblyRuntime{}, &supportAssemblyMembers{}
	cipher := telegrambootstrap.NewManager(nil, "synthetic-operation-test-key")
	assembly := telegrambootstrap.NewSupportRuntime(store, runtimeStore, runtimeStore, cipher, members)
	dispatch := telegrambootstrap.NewUpdateDispatcher(nil, nil, telegrambootstrap.UpdateModules{SupportOperations: assembly.Operations})
	if assembly.Operations.Cipher != cipher {
		t.Fatal("operation cipher not wired")
	}
	ctx := rc.WithExecutionFence(context.Background(), supportAssemblyFence{})
	update := rc.Update{Type: "message", ChatType: "supergroup", ChatID: -100, UserID: 55, ThreadID: 20, IsTopicMessage: true, Text: "close"}
	for _, action := range []string{sc.OperationClose, sc.OperationBlock, sc.OperationUnblock, sc.OperationNote} {
		intent := sc.OperationIntent{Update: update, Action: action}
		if action == sc.OperationNote {
			intent.Note = "internal note"
		}
		if _, err := dispatch.PrepareSupportOperation(ctx, 1, intent); err != nil || store.operation.Action != action {
			t.Fatal(action, err)
		}
		if action == sc.OperationNote {
			plain, err := cipher.Open(store.operation.NoteCipher)
			if err != nil || plain != intent.Note || store.operation.NoteCipher == intent.Note {
				t.Fatal("note not encrypted", err)
			}
		}
	}
	if _, err := dispatch.PrepareSupportOperation(ctx, 1, sc.OperationIntent{Update: update}); !errors.Is(err, sc.ErrDenied) {
		t.Fatal("action inferred from text", err)
	}
	if _, err := dispatch.Handle(ctx, 1, update); err != nil || store.applied != 0 || runtimeStore.queued != 0 {
		t.Fatal("ordinary text executed operation", err)
	}
}

// supportMenuFixture constructs a single-connection DB and real dispatcher without network.
func supportMenuFixture(t *testing.T) (*gorm.DB, *telegrambootstrap.UpdateDispatcher, sc.Cipher, *supportAssemblyMembers, *supportAssemblyShop) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "controls.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sql.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = db.AutoMigrate(&rd.Instance{}, &rd.Delivery{}, &gd.Target{}, &gd.ModuleConfig{}, &sd.Session{}, &sd.TopicMapping{}, &sd.OperationReference{}, &sd.OperationAudit{}, &sd.OperationControl{}, &sd.SupportBlock{}, &sd.BufferedMessage{}, &sd.Challenge{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, row := range []any{
		&rd.Instance{ID: 1, TelegramID: 100, Username: "testbot", Generation: 7, Enabled: true, Mode: "internal", State: "running"},
		&gd.Target{ID: 1, Purpose: "support", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -100, OccupancySlot: "-100", Observation: gd.Observation{Generation: 7, RouteVersion: 1, BotTelegramID: 100, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, IsForum: true, CanManageTopics: true, State: "ready", ObservedAt: &now}},
		&gd.ModuleConfig{BotInstanceID: 1, Partition: "support", Enabled: true, TargetID: 1, Version: 1},
		&sd.Session{ID: 1, BotID: 1, UserID: 10, ChatID: 10, State: "open", Version: 1},
		&sd.TopicMapping{ID: 1, OperationID: 1, BotID: 1, SessionID: 1, TargetID: 1, ChatID: -100, ThreadID: 20, RouteVersion: 1, State: "open", Version: 1},
	} {
		if err = db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	queue, members := runtimestore.New(db), &supportAssemblyMembers{}
	cipher := telegrambootstrap.NewManager(queue, "synthetic-control-key")
	store := supportstore.New(db, cipher)
	assembly := telegrambootstrap.NewSupportRuntime(store, queue, queue, cipher, members)
	shop := &supportAssemblyShop{}
	dispatch := telegrambootstrap.NewUpdateDispatcher(shop, shop, telegrambootstrap.UpdateModules{SupportOperations: assembly.Operations, GroupMessages: assembly.Replies, Targets: map[rc.TargetType]rc.TargetAuthorizer{rc.TargetSupportGroup: telegrambootstrap.SupportGroupAuthorizer{Ready: store, Controls: assembly.Operations}}})
	return db, dispatch, cipher, members, shop
}

// TestSupportMenuDurableDispatchAndAudit covers real queue, final guard and atomic CAS audit.
func TestSupportMenuDurableDispatchAndAudit(t *testing.T) {
	db, dispatch, cipher, members, shop := supportMenuFixture(t)
	var err error
	ctx, cancel := context.WithTimeout(rc.WithExecutionFence(context.Background(), supportAssemblyFence{}), 5*time.Second)
	defer cancel()
	update := rc.Update{ID: 1, Type: "message", MessageID: 20, ChatType: "supergroup", ChatID: -100, ThreadID: 20, UserID: 55, IsTopicMessage: true, Text: "/support@testbot"}
	for _, action := range []string{sc.OperationBlock, sc.OperationUnblock, sc.OperationNote, sc.OperationClose} {
		update.ID++
		update.Text = "/support@testbot"
		if action == sc.OperationNote {
			update.Text = "/support note secret internal note"
			update.Reply = &rc.ReplyReference{ChatID: -100, MessageID: 77}
		}
		if messages, err := dispatch.Handle(ctx, 1, update); err != nil || len(messages) != 0 {
			t.Fatal("menu ingress", err)
		}
		if _, err := dispatch.Handle(ctx, 1, update); err != nil {
			t.Fatal("inbox replay", err)
		}
		var delivery rd.Delivery
		if err = db.Order("id DESC").First(&delivery).Error; err != nil {
			t.Fatal(err)
		}
		plain, err := cipher.Open(delivery.PayloadCipher)
		if err != nil {
			t.Fatal(err)
		}
		var envelope rc.Message
		if err = json.Unmarshal([]byte(plain), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Purpose != sc.OperationControlPurpose || strings.Contains(plain, "secret internal note") || delivery.TelegramUserID != update.UserID {
			t.Fatal("unsafe control payload")
		}
		if err = dispatch.AuthorizeTarget(ctx, 1, envelope); err != nil {
			t.Fatal("final guard", err)
		}
		changed := envelope
		changed.Text = "injected"
		if err = dispatch.AuthorizeTarget(ctx, 1, changed); err == nil {
			t.Fatal("tampered control")
		}
		members.removed = true
		if err = dispatch.AuthorizeTarget(ctx, 1, envelope); err == nil {
			t.Fatal("departed member menu")
		}
		members.removed = false
		var operation sd.OperationReference
		if err = db.Where("action = ?", action).Order("id DESC").First(&operation).Error; err != nil {
			t.Fatal(err)
		}
		click := update
		click.Type, click.CallbackID, click.CallbackData = "callback_query", "click", sc.OperationCallbackPrefix+operation.Ref
		feedback := ""
		callbackCtx := rc.WithCallbackResponder(ctx, func(text string) { feedback = text })
		click.UserID++
		if _, err = dispatch.Handle(callbackCtx, 1, click); err != nil || feedback == "" {
			t.Fatal("missing safe denial feedback", err)
		}
		deniedFeedback := feedback
		var count int64
		if err = db.Model(&sd.OperationAudit{}).Where("reference_id = ?", operation.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("cross actor audit", err)
		}
		click.UserID--
		for range 2 {
			if messages, err := dispatch.Handle(callbackCtx, 1, click); err != nil || len(messages) != 0 || feedback == deniedFeedback {
				t.Fatal("callback success feedback", err)
			}
		}
		if err = db.Model(&sd.OperationAudit{}).Where("reference_id = ?", operation.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("CAS audit", count, err)
		}
		if err = dispatch.AuthorizeTarget(ctx, 1, envelope); err == nil {
			t.Fatal("stale session menu")
		}
	}
	var count int64
	if err = db.Model(&rd.Delivery{}).Count(&count).Error; err != nil || count != 4 || shop.handled != 0 {
		t.Fatal("duplicate menu or shop leak", count, err)
	}
}

// TestSupportMenuFinalGuardRejectsStaleBindings covers persisted control expiry and drift.
func TestSupportMenuFinalGuardRejectsStaleBindings(t *testing.T) {
	for _, scenario := range []string{"expiry", "generation", "route", "topic", "nofence"} {
		t.Run(scenario, func(t *testing.T) {
			db, dispatch, cipher, _, _ := supportMenuFixture(t)
			ctx := rc.WithExecutionFence(context.Background(), supportAssemblyFence{})
			update := rc.Update{ID: 1, MessageID: 1, Type: "message", ChatType: "supergroup", ChatID: -100, ThreadID: 20, UserID: 55, IsTopicMessage: true, Text: "/support"}
			if _, err := dispatch.Handle(ctx, 1, update); err != nil {
				t.Fatal(err)
			}
			var delivery rd.Delivery
			if err := db.First(&delivery).Error; err != nil {
				t.Fatal(err)
			}
			plain, err := cipher.Open(delivery.PayloadCipher)
			if err != nil {
				t.Fatal(err)
			}
			var envelope rc.Message
			if err = json.Unmarshal([]byte(plain), &envelope); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "expiry":
				err = db.Model(&sd.OperationControl{}).Where("bot_id = 1").Update("expires_at", time.Now().Add(-time.Minute)).Error
			case "generation":
				err = db.Model(&rd.Instance{}).Where("id = 1").Update("generation", 8).Error
			case "route":
				err = db.Model(&gd.Target{}).Where("id = 1").Update("route_version", 2).Error
			case "topic":
				err = db.Model(&sd.TopicMapping{}).Where("id = 1").Update("state", "deleted").Error
			case "nofence":
				ctx = context.Background()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = dispatch.AuthorizeTarget(ctx, 1, envelope); err == nil {
				t.Fatal("stale control authorized")
			}
		})
	}
}

// TestSupportRuntimeAssemblyFailsClosed checks real constructors with mock ports.
func TestSupportRuntimeAssemblyFailsClosed(t *testing.T) {
	store, runtimeStore, members := &supportAssemblyStore{}, &supportAssemblyRuntime{}, &supportAssemblyMembers{}
	assembly := telegrambootstrap.NewSupportRuntime(store, runtimeStore, runtimeStore, nil, members)
	if _, ok := assembly.Ready.Builder.(supportapp.TransferReadyBuilder); !ok {
		t.Fatal("wrong ready encoder")
	}
	if assembly.Replies.Store != store || assembly.Replies.Replies != store || assembly.Replies.Members != members || assembly.Replies.Queue == nil || assembly.Replies.Now == nil || assembly.Topics.Platform == nil {
		t.Fatal("missing support dependency")
	}
	ctx := rc.WithExecutionFence(context.Background(), supportAssemblyFence{})
	if err := assembly.Reconcile(context.Background(), 1); !errors.Is(err, rc.ErrStale) || store.readyScans != 0 {
		t.Fatal("unfenced recovery", err)
	}
	if err := assembly.Reconcile(ctx, 1); err != nil || store.readyScans != 1 || store.replyScans != 1 || store.topics != 1 || store.claims != 1 {
		t.Fatal("workers not reached", err, store)
	}
	service := supportapp.New(store, nil, nil, runtimeStore)
	if enabled, err := service.ReadyEnabled(ctx, 1); enabled || err != nil || service.DeliveryReady != nil {
		t.Fatal("production ingress enabled", err)
	}
	shop := &supportAssemblyShop{}
	dispatch := telegrambootstrap.NewUpdateDispatcher(shop, shop, telegrambootstrap.UpdateModules{
		Support: service, SupportCallbacks: service, GroupMessages: assembly.Replies,
		Targets: map[rc.TargetType]rc.TargetAuthorizer{rc.TargetSupportPrivate: telegrambootstrap.SupportPrivateAuthorizer{Notices: shop, Replies: assembly.Replies}},
	})
	group := rc.Update{Type: "message", ChatType: "supergroup", ChatID: -100, UserID: 55, Text: "staff"}
	if _, err := dispatch.Handle(ctx, 1, group); err != nil || runtimeStore.queued != 0 {
		t.Fatal("ordinary group chat leaked", err)
	}
	group.ThreadID, group.IsTopicMessage = 20, true
	group.Reply = &rc.ReplyReference{ChatID: -100, MessageID: 77}
	if _, err := dispatch.Handle(ctx, 1, group); err != nil || runtimeStore.queued != 1 || members.calls != 1 {
		t.Fatal("trusted reply not routed", err)
	}
	message := rc.Message{TargetType: rc.TargetSupportPrivate, Purpose: "support_reply"}
	if err := dispatch.AuthorizeTarget(ctx, 1, message); err != nil || store.checks != 2 {
		t.Fatal("reply final guard missing", err)
	}
	members.removed = true
	if err := dispatch.AuthorizeTarget(ctx, 1, message); !errors.Is(err, sc.ErrDenied) {
		t.Fatal("removed staff authorized", err)
	}
	message.Purpose = "support_gate"
	if err := dispatch.AuthorizeTarget(ctx, 1, message); err != nil || shop.notices != 1 {
		t.Fatal("verification routed as reply", err)
	}
	message.Purpose = "unknown"
	if err := dispatch.AuthorizeTarget(ctx, 1, message); err == nil {
		t.Fatal("unknown support purpose allowed")
	}
	private := rc.Update{Type: "message", ChatType: "private", Private: true, ChatID: 55, UserID: 55, Text: "consult"}
	if _, err := dispatch.Handle(ctx, 1, private); err != nil || shop.handled != 1 {
		t.Fatal("closed support changed shop fallback", err)
	}
	private.Type, private.CallbackID, private.CallbackData = "callback_query", "callback", "support:verify"
	if _, err := dispatch.Handle(ctx, 1, private); err != nil || shop.handled != 1 || runtimeStore.queued != 1 {
		t.Fatal("verification callback changed routing", err)
	}
}
