package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	producthttp "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/http"
	ordertransport "github.com/XManCodeing/XMall/internal/modules/order/transport/http"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	store "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"go/ast"
	"go/parser"
	"go/token"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/app/container"
	"github.com/XManCodeing/XMall/internal/app/httpserver/middleware"
	"github.com/XManCodeing/XMall/internal/authz"
	"github.com/XManCodeing/XMall/internal/config"
	affiliatetransport "github.com/XManCodeing/XMall/internal/modules/affiliate/transport/http"
	categorystore "github.com/XManCodeing/XMall/internal/modules/catalog/category/infrastructure/gormstore"
	downstreamstore "github.com/XManCodeing/XMall/internal/modules/downstreamcallback/infrastructure/gormstore"
	admindomain "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	paymentstore "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gormstore"
	paymenttransport "github.com/XManCodeing/XMall/internal/modules/payment/transport/http"
	reconciliationtransport "github.com/XManCodeing/XMall/internal/modules/reconciliation/transport/http"
	resellertransport "github.com/XManCodeing/XMall/internal/modules/reseller/transport/http/admin"
	siteconnectionstore "github.com/XManCodeing/XMall/internal/modules/siteconnection/infrastructure/gormstore"
	groupsapp "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	groupshttp "github.com/XManCodeing/XMall/internal/modules/telegram/groups/transport/http"
	recoverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	recoverydomain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	recoveryhttp "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/transport/http"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	supporthttp "github.com/XManCodeing/XMall/internal/modules/telegram/support/transport/http"
	wallettransport "github.com/XManCodeing/XMall/internal/modules/wallet/transport/http"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"

	"github.com/casbin/casbin/v3/util"
)

// supportManagementRBAC registers the real management route inventory while
// the middleware test terminates before invoking any business operation.
type supportManagementRBAC struct {
	recoverycontract.SupportManagement
}

// recoveryCipher is a test-only envelope codec; no production secrets are read.
type recoveryCipher struct{}

func (recoveryCipher) Seal(value string) (string, error) { return value, nil }
func (recoveryCipher) Open(value string) (string, error) { return value, nil }

// recoveryAuthority models an independent grant, not the metadata GET permission.
type recoveryAuthority struct{ denied *bool }

func (authority recoveryAuthority) AuthorizeSupportResolution(_ context.Context, actor, bot uint) error {
	if *authority.denied || actor != 9 || bot != 1 {
		return c.ErrDenied
	}
	return nil
}

// recoveryRoute uses the supplied transaction to detect global-connection escapes.
type recoveryRoute struct {
	tx      *gorm.DB
	version *uint64
}

func (route recoveryRoute) CheckSupportRoute(ctx context.Context, bot, target uint, binding c.DeliveryBinding, _ time.Time) error {
	var count int64
	if err := route.tx.WithContext(ctx).Table("telegram_bot_instances").Where("id = ? AND state = ?", bot, "running").Count(&count).Error; err != nil {
		return err
	}
	if count != 1 || target != 1 || binding.ChatID != -100 || binding.RouteVersion != *route.version {
		return c.ErrDenied
	}
	return nil
}

// recoveryRuntime mirrors the runtime contract without importing its domain package.
type recoveryRuntime struct {
	ID             uint `gorm:"primaryKey"`
	RecoveryTaskID uint `gorm:"not null;default:0"`
	Generation     uint64
	Enabled        bool
	Mode, State    string
}

func (recoveryRuntime) TableName() string { return "telegram_bot_instances" }

// recoveryProof mirrors only the persisted runtime proof columns consumed by support.
type recoveryProof struct {
	ID                                                         uint `gorm:"primaryKey"`
	BotID                                                      uint
	Key, Status, TargetType, Purpose, Operation, PayloadCipher string
	ChatID, TelegramUserID, MessageID                          int64
	MessageIDs                                                 []int64 `gorm:"serializer:json;type:text"`
	PayloadHeld                                                bool
}

func (recoveryProof) TableName() string { return "telegram_bot_deliveries" }

// recoveryFixture assembles real HTTP/service/store using SQLite MaxOpenConns=1.
func recoveryFixture(t *testing.T) (*gorm.DB, *gin.Engine, *bool, *uint64) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	if err := db.AutoMigrate(&recoveryRuntime{}, &recoveryProof{}, &d.Session{}, &d.BufferedMessage{}, &d.ReadyDispatch{}, &d.TopicMapping{}, &d.TopicOperation{}, &d.ResolutionAudit{}, &d.ReplyMapping{}, &d.Album{}, &d.AlbumItem{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&recoveryRuntime{ID: 1, Generation: 1, Enabled: true, Mode: "internal", State: "running"}).Error; err != nil {
		t.Fatal(err)
	}
	denied, version := false, uint64(1)
	repo := store.New(db, recoveryCipher{})
	repo.WithRouteReader(func(tx *gorm.DB) c.RouteReader { return recoveryRoute{tx, &version} })
	query := store.NewAdmin(db)
	service := a.NewAdmin(query, recoveryCipher{}, func(context.Context, uint, string, bool) error { return nil }, nil).
		WithRecovery(query, &a.Resolver{Topics: store.AdminTopicResolver{Store: repo}, Dispatches: repo, Authorizer: recoveryAuthority{&denied}}, func(ctx context.Context, bot uint) (uint64, error) {
			var row recoveryRuntime
			err := db.WithContext(ctx).Where("id = ?", bot).Take(&row).Error
			return row.Generation, err
		})
	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(9)); ctx.Next() })
	supporthttp.RegisterRoutes(group, supporthttp.New(service))
	return db, router, &denied, &version
}

// recoveryRequest checks no-store/status with a deadline guarding against SQLite deadlocks.
func recoveryRequest(t *testing.T, router *gin.Engine, method, path, body string, status int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := httptest.NewRequest(method, "/admin/telegram-bot/support/"+path, strings.NewReader(body)).WithContext(ctx)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != status || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s: %d %s", path, result.Code, result.Body.String())
	}
	return result.Body.String()
}

// TestRecoveryTopicHTTP covers current routes, historical uniqueness and one audited CAS.
func TestRecoveryTopicHTTP(t *testing.T) {
	db, router, denied, route := recoveryFixture(t)
	operation := d.TopicOperation{ID: 51, BotID: 1, SessionID: 1, TargetID: 1, RouteVersion: 1, ChatID: -100, Action: "create", State: "unknown", Version: 1, Claim: "private-claim"}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	valid := `{"expected_version":1,"decision":"succeeded","evidence":"ticket:T-1","thread_id":"42"}`
	path := "topic-operations/51/resolve"
	recoveryRequest(t, router, "GET", "topic-operations", "", 400)
	body := recoveryRequest(t, router, "GET", "topic-operations?bot_id=1&state=unknown&page_size=1", "", 200)
	if strings.Contains(body, "private-claim") || !strings.Contains(body, `"total":1`) {
		t.Fatal(body)
	}
	body = recoveryRequest(t, router, "GET", "topic-operations?bot_id=2&state=unknown", "", 200)
	if !strings.Contains(body, `"total":0`) {
		t.Fatal(body)
	}
	recoveryRequest(t, router, "GET", "topic-operations/51", "", 200)
	recoveryRequest(t, router, "GET", "upstreams/51", "", 404)
	for _, invalid := range []string{strings.Replace(valid, "ticket:T-1", "https://secret", 1), strings.Replace(valid, `,"thread_id":"42"`, "", 1), strings.TrimSuffix(valid, "}") + `,"actor_id":9}`, strings.TrimSuffix(valid, "}") + `,"generation":1}`, `{"expected_version":1,"decision":"failed","evidence":"case:1"}`, valid + `{}`} {
		recoveryRequest(t, router, "POST", path, invalid, 400)
	}
	*denied = true
	recoveryRequest(t, router, "POST", path, valid, 403)
	*denied = false
	*route = 2
	recoveryRequest(t, router, "POST", path, valid, 403)
	*route = 1
	historical := d.TopicMapping{OperationID: 99, BotID: 1, SessionID: 99, TargetID: 1, RouteVersion: 0, ChatID: -100, ThreadID: 42, State: "deleted"}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", path, valid, 409)
	valid = strings.Replace(valid, `"42"`, `"43"`, 1)
	recoveryRequest(t, router, "POST", path, valid, 200)
	recoveryRequest(t, router, "POST", path, valid, 409)
	var audits []d.ResolutionAudit
	if err := db.Find(&audits).Error; err != nil || len(audits) != 1 || audits[0].ActorID != 9 || audits[0].Resource != "topic" || audits[0].Version != 1 {
		t.Fatal(audits, err)
	}
	operation.ID, operation.SessionID = 52, 2
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", "topic-operations/52/resolve", `{"expected_version":1,"decision":"failed","evidence":"audit:A-1","no_effect":true}`, 200)
	var count int64
	if err := db.Model(&d.TopicMapping{}).Where("operation_id = ?", 52).Count(&count).Error; err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

// seedRecoveryUpstream persists an immutable envelope and an unknown runtime proof row.
func seedRecoveryUpstream(t *testing.T, db *gorm.DB) recoveryProof {
	t.Helper()
	envelope := r.Message{ChatID: -100, ThreadID: 20, RouteVersion: 1, TargetType: r.TargetSupportGroup, Purpose: "support_ready", Operation: "send", BusinessRef: c.ReadyBusinessRef(1, 41), Text: "original"}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := d.BufferedMessage{ID: 41, BotID: 1, SessionID: 1, UserID: 10, State: "unknown", TargetChatID: -100, TargetThreadID: 20, RouteVersion: 1, PayloadCipher: "private-payload"}
	dispatch := d.ReadyDispatch{ID: 91, BotID: 1, MessageID: 41, TopicID: 1, Version: 1, PayloadCipher: string(raw)}
	topic := d.TopicMapping{ID: 1, OperationID: 1, BotID: 1, SessionID: 1, ChatID: -100, ThreadID: 20, RouteVersion: 1, State: "open"}
	proof := recoveryProof{ID: 61, BotID: 1, Key: envelope.BusinessRef, ChatID: -100, TelegramUserID: 10, TargetType: string(envelope.TargetType), Purpose: envelope.Purpose, Operation: envelope.Operation, PayloadCipher: string(raw), Status: "unknown"}
	for _, row := range []any{&message, &dispatch, &topic, &proof} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return proof
}

// TestRecoveryUpstreamHTTP covers persisted proof, anti-forgery, independent IDs and audit/CAS.
func TestRecoveryUpstreamHTTP(t *testing.T) {
	for _, decision := range []string{"delivered", "failed"} {
		t.Run(decision, func(t *testing.T) {
			db, router, denied, _ := recoveryFixture(t)
			proof := seedRecoveryUpstream(t, db)
			path := "upstreams/41/resolve"
			valid := fmt.Sprintf(`{"expected_version":1,"decision":%q,"evidence":"case:C-1","delivery_id":%d}`, decision, proof.ID)
			body := recoveryRequest(t, router, "GET", "upstreams?bot_id=1&state=unknown", "", 200)
			for _, secret := range []string{"payload", "cipher", "claim", "original", "message_ids"} {
				if strings.Contains(body, secret) {
					t.Fatal(body)
				}
			}
			if !strings.Contains(body, `"total":1`) || !strings.Contains(body, `"version":1`) {
				t.Fatal(body)
			}
			recoveryRequest(t, router, "GET", "upstreams/41", "", 200)
			recoveryRequest(t, router, "GET", "upstreams/91", "", 404)
			recoveryRequest(t, router, "GET", "upstreams?bot_id=1&state=succeeded", "", 400)
			for _, injection := range []string{`,"message_ids":[77]}`, `,"message_id":77}`, `,"thread_id":"20"}`, `,"generation":1}`, `,"bot_id":1}`} {
				recoveryRequest(t, router, "POST", path, strings.TrimSuffix(valid, "}")+injection, 400)
			}
			*denied = true
			recoveryRequest(t, router, "POST", path, valid, 403)
			*denied = false
			recoveryRequest(t, router, "POST", path, valid, 403)
			status, messageID := "sent", int64(77)
			if decision == "failed" {
				status, messageID = "failed", 0
			}
			if err := db.Model(&proof).Updates(map[string]any{"status": status, "message_id": messageID}).Error; err != nil {
				t.Fatal(err)
			}
			recoveryRequest(t, router, "POST", path, valid, 200)
			recoveryRequest(t, router, "POST", path, valid, 409)
			var audits []d.ResolutionAudit
			if err := db.Find(&audits).Error; err != nil || len(audits) != 1 || audits[0].Resource != "ready" || audits[0].ActorID != 9 {
				t.Fatal(audits, err)
			}
			var stored d.BufferedMessage
			if err := db.First(&stored, 41).Error; err != nil {
				t.Fatal(err)
			}
			// Resolution records the proven terminal failure only. It must not
			// requeue runtime or disguise failed as unknown/sending.
			if stored.State != decision || stored.DeliveryID != proof.ID {
				t.Fatal(stored.State, stored.DeliveryID)
			}
			var persisted recoveryProof
			if err := db.First(&persisted, proof.ID).Error; err != nil {
				t.Fatal(err)
			}
			var proofCount int64
			if err := db.Model(&recoveryProof{}).Count(&proofCount).Error; err != nil {
				t.Fatal(err)
			}
			if proofCount != 1 || persisted.Status != status || persisted.Key != proof.Key || persisted.PayloadCipher != proof.PayloadCipher || persisted.MessageID != messageID {
				t.Fatal("resolution changed the runtime attempt", persisted, proofCount)
			}
			if decision == "failed" {
				var replies int64
				if err := db.Model(&d.ReplyMapping{}).Count(&replies).Error; err != nil || replies != 0 {
					t.Fatal("failed resolution created a reply receipt", replies, err)
				}
			}
		})
	}
}

// TestRecoveryAuditFailureRollsBack verifies proof acceptance cannot commit without its audit.
func TestRecoveryAuditFailureRollsBack(t *testing.T) {
	db, router, _, _ := recoveryFixture(t)
	proof := seedRecoveryUpstream(t, db)
	if err := db.Model(&proof).Updates(map[string]any{"status": "sent", "message_id": 77}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON telegram_support_resolution_audits BEGIN SELECT RAISE(ABORT, 'test audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	input := `{"expected_version":1,"decision":"delivered","evidence":"ticket:rollback","delivery_id":61}`
	recoveryRequest(t, router, "POST", "upstreams/41/resolve", input, 500)
	var message d.BufferedMessage
	var dispatch d.ReadyDispatch
	var replies int64
	if err := db.First(&message, 41).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&dispatch, 91).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&d.ReplyMapping{}).Count(&replies).Error; err != nil {
		t.Fatal(err)
	}
	if message.State != "unknown" || dispatch.Version != 1 || replies != 0 {
		t.Fatal(message.State, dispatch.Version, replies)
	}
	if err := db.Exec("DROP TRIGGER reject_recovery_audit").Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", "upstreams/41/resolve", input, 200)
}

// TestRecoveryForgedProofAndNoEffect rejects mismatched runtime envelopes and partial effects.
func TestRecoveryForgedProofAndNoEffect(t *testing.T) {
	db, router, _, _ := recoveryFixture(t)
	proof := seedRecoveryUpstream(t, db)
	valid := `{"expected_version":1,"decision":"failed","evidence":"audit:proof","delivery_id":61}`
	if err := db.Model(&proof).Updates(map[string]any{"status": "failed", "message_id": 77}).Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", "upstreams/41/resolve", valid, 403)
	if err := db.Model(&proof).Updates(map[string]any{"message_id": 0, "chat_id": -200}).Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", "upstreams/41/resolve", valid, 403)
	if err := db.Model(&proof).Updates(map[string]any{"chat_id": -100, "bot_id": 2}).Error; err != nil {
		t.Fatal(err)
	}
	recoveryRequest(t, router, "POST", "upstreams/41/resolve", valid, 403)
	var audits int64
	if err := db.Model(&d.ResolutionAudit{}).Count(&audits).Error; err != nil || audits != 0 {
		t.Fatal(audits, err)
	}
}

// TestSupportReadRoutesRequireBackendJWTAndRole verifies every actual support route.
func TestSupportReadRoutesRequireBackendJWTAndRole(t *testing.T) {
	repo, auth, denied, _ := setupContentRouteAccessTest(t)
	admin := &admindomain.Admin{Username: "support-reader", PasswordHash: "hash"}
	if err := repo.Create(admin); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetAdminRoles(admin.ID, []string{"support"}); err != nil {
		t.Fatal(err)
	}
	allowed := signContentAdminToken(t, admin)
	settingsAdmin := &admindomain.Admin{Username: "support-settings", PasswordHash: "hash"}
	if err := repo.Create(settingsAdmin); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetAdminRoles(settingsAdmin.ID, []string{"system_admin"}); err != nil {
		t.Fatal(err)
	}
	settingsToken := signContentAdminToken(t, settingsAdmin)
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(middleware.JWTAuthMiddleware(contentAdminJWTSecret, repo), middleware.AdminRBACMiddleware(auth))
	group.Use(func(ctx *gin.Context) { response.Success(ctx, nil); ctx.Abort() })
	supporthttp.RegisterRoutes(group, supporthttp.New(nil))
	assertSupportRouteInventory(t, router)
	assertSupportWriteRouteIsolation(t, router, repo, auth)
	for _, route := range router.Routes() {
		path := strings.ReplaceAll(route.Path, ":id", "1")
		path = strings.ReplaceAll(path, ":message_id", "2")
		path = strings.ReplaceAll(path, ":bot_id", "1")
		supportStatus, settingsStatus := response.CodeOK, response.CodeForbidden
		if strings.Contains(path, "/settings/") {
			supportStatus, settingsStatus = response.CodeForbidden, response.CodeOK
		}
		for _, scenario := range []struct {
			token  string
			status int
		}{{"", response.CodeUnauthorized}, {denied, response.CodeForbidden}, {allowed, supportStatus}, {settingsToken, settingsStatus}} {
			request := httptest.NewRequest(route.Method, path, nil)
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			request.Header.Set("X-Admin-ID", "1")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			var body struct {
				Status int `json:"status_code"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != scenario.status {
				t.Fatalf("%s: %s", path, result.Body.String())
			}
		}
	}
	assertSupportSourceInventory(t)
}

// TestAllAdminRoutesCoveredByBuiltinRoles 校验 admin 路由文件里的每条路由
// 都被 authz.BuiltinRoleSeeds() 中至少一条角色策略覆盖。
//
// 目的：避免新增 admin 接口时忘记同步 RBAC 预置角色，导致非超管角色无法通过
// 角色分配获得该权限（catalog UI 上能看到，但任何角色都拿不到）。
//
// 实现：静态扫描 routes_admin*.go、internal/modules/**/routes.go
// 以及 internal/platform/http/**/routes.go，
// 提取 admin/authorized/paymentProtected.METHOD("/path", ...) 调用，
// 与 builtin role seeds 用 keyMatch2 比对（与运行时 Casbin 模型一致）。
func TestAllAdminRoutesCoveredByBuiltinRoles(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatalf("extract admin routes: %v", err)
	}
	if len(routes) == 0 {
		t.Fatalf("no admin routes extracted; parser or source layout changed?")
	}
	t.Logf("validating RBAC coverage for %d admin routes", len(routes))

	seeds := authz.BuiltinRoleSeeds()
	if len(seeds) == 0 {
		t.Fatalf("no builtin role seeds")
	}

	type policy struct {
		object string
		action string
	}
	var policies []policy
	for _, seed := range seeds {
		for _, p := range seed.Policies {
			policies = append(policies, policy{
				object: authz.NormalizeObject(p.Object),
				action: authz.NormalizeAction(p.Action),
			})
		}
	}

	var uncovered []adminRoute
	for _, r := range routes {
		matched := false
		for _, p := range policies {
			if p.action != "*" && p.action != r.method {
				continue
			}
			if util.KeyMatch2(r.object, p.object) {
				matched = true
				break
			}
		}
		if !matched {
			uncovered = append(uncovered, r)
		}
	}

	if len(uncovered) > 0 {
		var lines []string
		for _, r := range uncovered {
			lines = append(lines, "  "+r.method+" "+r.object)
		}
		t.Fatalf("the following admin routes are not covered by any builtin role seed in authz.BuiltinRoleSeeds() — add them to the appropriate role in api/internal/authz/bootstrap.go:\n%s",
			strings.Join(lines, "\n"))
	}
}

// The production router (not a synthetic route list) is the permission catalog's
// source of truth. No database, Redis, config file or Telegram call is needed to
// register handlers; requests to retained business handlers are not executed.
func TestChannelClientAdminRoutesAndPermissionsRemoved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := SetupRouter(&config.Config{}, &container.Container{
		TelegramGroups:         &groupsapp.Service{},
		TelegramBotManager:     &runtimeapp.Manager{},
		PaymentStore:           &paymentstore.Store{},
		PaymentChannelStore:    &paymentstore.ChannelStore{},
		CategoryRepo:           categorystore.NewCategoryStore(nil),
		UserStore:              userstore.New(nil),
		SiteConnectionRepo:     siteconnectionstore.New(nil),
		DownstreamOrderRefRepo: &downstreamstore.Store{},
	})
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, "/api/v1/admin/channel-clients") || strings.HasPrefix(route.Path, "/api/v1/admin/telegram/cutover") {
			t.Errorf("retired channel client route registered: %s %s", route.Method, route.Path)
		}
	}
	catalog := buildAdminPermissionCatalog(router)
	if len(catalog) == 0 {
		t.Fatal("production permission catalog is empty")
	}
	for _, item := range catalog {
		if strings.HasPrefix(item.Object, "/admin/channel-clients") || strings.HasPrefix(item.Object, "/admin/telegram/cutover") || item.Module == "channel-clients" {
			t.Errorf("retired channel client permission remains in catalog: %+v", item)
		}
	}
	for _, seed := range authz.BuiltinRoleSeeds() {
		for _, policy := range seed.Policies {
			if strings.HasPrefix(policy.Object, "/admin/channel-clients") || strings.HasPrefix(policy.Object, "/admin/telegram/cutover") {
				t.Errorf("retired channel client policy remains: %s %+v", seed.Role, policy)
			}
		}
	}

	// Retirement must not remove native instance management,
	// broadcasts or the external channel bot API.
	for _, expected := range []adminRoute{
		{http.MethodGet, "/api/v1/admin/telegram-bot/groups"},
		{http.MethodPost, "/api/v1/admin/telegram-bot/groups"},
		{http.MethodGet, "/api/v1/admin/telegram-bot/groups/:id"},
		{http.MethodPatch, "/api/v1/admin/telegram-bot/groups/:id"},
		{http.MethodPost, "/api/v1/admin/telegram-bot/groups/:id/check"},
		{http.MethodGet, "/api/v1/admin/telegram-bot/instances/:id/modules"},
		{http.MethodPatch, "/api/v1/admin/telegram-bot/instances/:id/modules"},
		{http.MethodGet, "/api/v1/admin/telegram-bot/instances"},
		{http.MethodGet, "/api/v1/admin/telegram-bot/broadcasts"},
		{http.MethodGet, "/api/v1/channel/telegram/config"},
		{http.MethodPost, "/api/v1/channel/telegram/heartbeat"},
	} {
		found := false
		for _, route := range router.Routes() {
			if route.Method == expected.method && route.Path == expected.object {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("retained route missing: %s %s", expected.method, expected.object)
		}
	}
}

func TestAdminAdvertisingRoutesAndPoliciesRemoved(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatalf("extract admin routes: %v", err)
	}
	for _, route := range routes {
		if route.object == "/admin/ads" || strings.HasPrefix(route.object, "/admin/ads/") {
			t.Errorf("advertising route must stay removed: %s %s", route.method, route.object)
		}
	}
	for _, seed := range authz.BuiltinRoleSeeds() {
		for _, policy := range seed.Policies {
			if policy.Object == "/admin/ads" || strings.HasPrefix(policy.Object, "/admin/ads/") {
				t.Errorf("advertising policy must stay removed: %s %s", seed.Role, policy.Object)
			}
		}
	}
}

// Exercise every formerly declaration-gated route with real JWT validation and
// Casbin policies. The isolated SQLite fixture has no settings table or declaration
// record. A terminal probe replaces business execution, so write routes never
// create payments, transfer money or contact a provider.
func TestPaymentFinanceRoutesWithoutDeclarationKeepAuthenticationAndRBAC(t *testing.T) {
	adminRepo, authzService, noPermissionToken, _ := setupContentRouteAccessTest(t)
	finance := &admindomain.Admin{Username: "finance-no-declaration", PasswordHash: "hash"}
	if err := adminRepo.Create(finance); err != nil {
		t.Fatal(err)
	}
	if err := authzService.SetAdminRoles(finance.ID, []string{"finance", "integration"}); err != nil {
		t.Fatal(err)
	}
	super := &admindomain.Admin{Username: "super-no-declaration", PasswordHash: "hash", IsSuper: true}
	if err := adminRepo.Create(super); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	authorized := router.Group("/api/v1/admin")
	authorized.Use(middleware.JWTAuthMiddleware(contentAdminJWTSecret, adminRepo), middleware.AdminRBACMiddleware(authzService))
	authorized.Use(func(c *gin.Context) {
		response.Success(c, gin.H{"reached_business_handler": true})
		c.Abort()
	})
	paymenttransport.RegisterAdminRoutes(authorized, &paymenttransport.AdminHandler{})
	paymenttransport.RegisterAdminChannelRoutes(authorized, &paymenttransport.AdminChannelHandler{})
	wallettransport.RegisterAdminRoutes(authorized, &wallettransport.AdminHandler{})
	affiliatetransport.RegisterAdminFinanceRoutes(authorized, &affiliatetransport.AdminHandler{})
	resellertransport.RegisterOperationsFinanceRoutes(authorized, &resellertransport.AdminOperationsHandler{})
	resellertransport.RegisterFinanceRoutes(authorized, &resellertransport.AdminFinanceHandler{})
	reconciliationtransport.RegisterAdminRoutes(authorized, &reconciliationtransport.AdminHandler{})

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "anonymous", want: response.CodeUnauthorized},
		{name: "invalid JWT", token: "not-a-jwt", want: response.CodeUnauthorized},
		{name: "authenticated without permission", token: noPermissionToken, want: response.CodeForbidden},
		{name: "authorized without declaration", token: signContentAdminToken(t, finance), want: response.CodeOK},
		{name: "super without declaration", token: signContentAdminToken(t, super), want: response.CodeOK},
	}
	for _, route := range router.Routes() {
		for _, test := range tests {
			t.Run(route.Method+" "+route.Path+"/"+test.name, func(t *testing.T) {
				path := strings.ReplaceAll(route.Path, ":id", "42")
				request := httptest.NewRequest(route.Method, path, nil)
				if test.token != "" {
					request.Header.Set("Authorization", "Bearer "+test.token)
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				var result struct {
					StatusCode int `json:"status_code"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode response: %v (%s)", err, recorder.Body.String())
				}
				if recorder.Code != http.StatusOK || result.StatusCode != test.want {
					t.Fatalf("want business status %d, got HTTP %d: %s", test.want, recorder.Code, recorder.Body.String())
				}
				reached := strings.Contains(recorder.Body.String(), `"reached_business_handler":true`)
				if reached != (test.want == response.CodeOK) {
					t.Fatalf("unexpected business-handler access: %s", recorder.Body.String())
				}
			})
		}
	}
	t.Logf("verified %d payment/finance routes across %d authentication states", len(router.Routes()), len(tests))
}

// TestMembershipRoutesRequireMatchingRole 验证实际会员路由 JWT、商品/订单角色隔离。
func TestMembershipRoutesRequireMatchingRole(t *testing.T) {
	repo, auth, denied, _ := setupContentRouteAccessTest(t)
	tokens := map[string]string{}
	for _, role := range []string{"operations", "support", "system_admin"} {
		admin := &admindomain.Admin{Username: "membership-" + role, PasswordHash: "hash"}
		if err := repo.Create(admin); err != nil {
			t.Fatal(err)
		}
		if err := auth.SetAdminRoles(admin.ID, []string{role}); err != nil {
			t.Fatal(err)
		}
		tokens[role] = signContentAdminToken(t, admin)
	}
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(middleware.JWTAuthMiddleware(contentAdminJWTSecret, repo), middleware.AdminRBACMiddleware(auth))
	group.Use(func(ctx *gin.Context) { response.Success(ctx, nil); ctx.Abort() })
	producthttp.RegisterMembershipRoutes(group, &producthttp.AdminProductHandler{})
	ordertransport.RegisterMembershipRoutes(group, &ordertransport.AdminHandler{})
	for _, route := range router.Routes() {
		path := strings.ReplaceAll(route.Path, ":id", "99")
		allowedRole := "operations"
		if strings.Contains(path, "/orders") {
			allowedRole = "support"
		}
		for role, token := range tokens {
			expected := 403
			if role == allowedRole {
				expected = response.CodeOK
			}
			req := httptest.NewRequest(route.Method, path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			result := httptest.NewRecorder()
			router.ServeHTTP(result, req)
			if !strings.Contains(result.Body.String(), fmt.Sprintf(`"status_code":%d`, expected)) {
				t.Fatalf("%s %s %s: %s", role, route.Method, path, result.Body.String())
			}
		}
		for _, token := range []string{"", denied} {
			expected := 401
			if token != "" {
				expected = 403
			}
			req := httptest.NewRequest(route.Method, path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			result := httptest.NewRecorder()
			router.ServeHTTP(result, req)
			if !strings.Contains(result.Body.String(), fmt.Sprintf(`"status_code":%d`, expected)) {
				t.Fatalf("unauthorized %s %s", path, result.Body.String())
			}
		}
	}
}

func TestTelegramGroupRoutesEnforceRBAC(t *testing.T) {
	repo, svc, denied, _ := setupContentRouteAccessTest(t)
	admin := &admindomain.Admin{Username: "group-system-admin", PasswordHash: "hash"}
	if err := repo.Create(admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAdminRoles(admin.ID, []string{"system_admin"}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(middleware.JWTAuthMiddleware(contentAdminJWTSecret, repo), middleware.AdminRBACMiddleware(svc))
	group.Use(func(c *gin.Context) { response.Success(c, gin.H{"reached": true}); c.Abort() })
	groupshttp.RegisterRoutes(group, groupshttp.New(nil))
	recoveryhttp.RegisterRoutes(group, recoveryhttp.New(nil).WithExecution(recoveryhttp.Execution{
		Confirm: func(context.Context, uint, recoverycontract.Confirmation) (*recoverydomain.Task, error) {
			return nil, recoverycontract.ErrInvalid
		},
		Resume: func(context.Context, uint, uint64, uint) (*recoverydomain.Task, error) {
			return nil, recoverycontract.ErrInvalid
		},
	}).WithSupportManagement(supportManagementRBAC{}))
	if len(router.Routes()) != 20 {
		t.Fatal("missing groups routes")
	}
	for _, route := range router.Routes() {
		for _, test := range []struct {
			token string
			want  int
		}{{"", response.CodeUnauthorized}, {denied, response.CodeForbidden}, {signContentAdminToken(t, admin), response.CodeOK}} {
			request := httptest.NewRequest(route.Method, strings.ReplaceAll(route.Path, ":id", "1"), nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			var out struct {
				StatusCode int `json:"status_code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.StatusCode != test.want {
				t.Fatalf("%s %s: %s", route.Method, route.Path, rec.Body.String())
			}
		}
	}
}

func TestAdminDeclarationRoutesAndPoliciesRemoved(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if strings.HasPrefix(route.object, "/admin/compliance") {
			t.Errorf("declaration API must stay removed: %s %s", route.method, route.object)
		}
	}
	for _, seed := range authz.BuiltinRoleSeeds() {
		for _, policy := range seed.Policies {
			if strings.HasPrefix(policy.Object, "/admin/compliance") {
				t.Errorf("declaration policy must stay removed: %s %s", seed.Role, policy.Object)
			}
		}
	}
}

type adminRoute struct {
	method string
	object string // 例如 "/admin/users/:id"
}

var adminRouteMethods = map[string]struct{}{
	"GET":    {},
	"POST":   {},
	"PUT":    {},
	"PATCH":  {},
	"DELETE": {},
}

var adminRouteReceivers = map[string]struct{}{
	"admin":            {},
	"authorized":       {},
	"paymentProtected": {},
}

var publicAdminRoutes = map[string]struct{}{
	"POST /admin/login":            {},
	"POST /admin/login/verify-2fa": {},
}

// extractAdminRoutesFromSource 从应用 admin 路由、模块路由和平台 HTTP 路由文件中读取调用。
// 方法范围：GET / POST / PUT / PATCH / DELETE。HEAD/OPTIONS 不参与 RBAC。
func extractAdminRoutesFromSource() ([]adminRoute, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	routerDirectory := filepath.Dir(thisFile)

	sources, err := discoverAdminRouteSources(routerDirectory)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, os.ErrNotExist
	}

	seen := make(map[string]struct{})
	var out []adminRoute
	for _, source := range sources {
		routes, err := extractAdminRoutesFromFile(source)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			key := route.method + " " + route.object
			if _, public := publicAdminRoutes[key]; public {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, route)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].object == out[j].object {
			return out[i].method < out[j].method
		}
		return out[i].object < out[j].object
	})
	return out, nil
}

func discoverAdminRouteSources(routerDirectory string) ([]string, error) {
	adminSources, err := filepath.Glob(filepath.Join(routerDirectory, "routes_admin*.go"))
	if err != nil {
		return nil, err
	}

	sources := make([]string, 0, len(adminSources)+32)
	for _, path := range adminSources {
		if !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
	}

	for _, directory := range []string{
		filepath.Join(routerDirectory, "..", "..", "modules"),
		filepath.Join(routerDirectory, "..", "..", "platform", "http"),
	} {
		if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && entry.Name() == "routes.go" {
				sources = append(sources, path)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Broadcast keeps route registration beside its handler to stay within its
	// module file budget; it must remain included in RBAC coverage scanning.
	sources = append(sources, filepath.Join(routerDirectory, "..", "..", "modules", "telegram", "broadcast", "transport", "http", "handler.go"))
	// Support read routes also live beside their transport handler.
	sources = append(sources, filepath.Join(routerDirectory, "..", "..", "modules", "telegram", "support", "transport", "http", "handler.go"))
	sort.Strings(sources)
	return sources, nil
}

func TestExtractAdminRoutesIncludesPlatformHTTPRoutes(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatalf("extract admin routes: %v", err)
	}
	for _, route := range routes {
		if route.method == "GET" && route.object == "/admin/system/version/check" {
			return
		}
	}
	t.Fatal("platform HTTP route GET /admin/system/version/check was not discovered")
}

func TestExtractAdminRoutesIncludesBroadcastDeliveries(t *testing.T) {
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"GET /admin/telegram-bot/broadcasts/:id/deliveries":                     false,
		"POST /admin/telegram-bot/broadcasts/:id/deliveries/:delivery_id/retry": false,
	}
	for _, route := range routes {
		key := route.method + " " + route.object
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("broadcast handler route not discovered: %s", route)
		}
	}
}

func extractAdminRoutesFromFile(path string) ([]adminRoute, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return nil, err
	}

	var routes []adminRoute
	var routeErr error
	ast.Inspect(file, func(node ast.Node) bool {
		if routeErr != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := adminRouteReceivers[receiver.Name]; !ok {
			return true
		}
		if _, ok := adminRouteMethods[selector.Sel.Name]; !ok {
			return true
		}
		pathLiteral, ok := call.Args[0].(*ast.BasicLit)
		if !ok || pathLiteral.Kind != token.STRING {
			routeErr = fmt.Errorf("%s:%d: admin route path must be a string literal", path, fileSet.Position(call.Args[0].Pos()).Line)
			return false
		}
		routePath, err := strconv.Unquote(pathLiteral.Value)
		if err != nil {
			routeErr = fmt.Errorf("%s:%d: decode admin route path: %w", path, fileSet.Position(pathLiteral.Pos()).Line, err)
			return false
		}
		routes = append(routes, adminRoute{
			method: selector.Sel.Name,
			object: authz.NormalizeObject("/admin" + routePath),
		})
		return true
	})
	if routeErr != nil {
		return nil, routeErr
	}
	return routes, nil
}
