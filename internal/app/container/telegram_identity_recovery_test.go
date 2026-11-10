package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	accessbootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess"
	membershipbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershiprecovery"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershipapi "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/botapi"
	mr "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	rh "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/transport/http"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/authz"
	admin "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// groupAssemblyProbe replaces only platform I/O while checking single-connection availability.
type groupAssemblyProbe struct {
	db       *gorm.DB
	identity app.GroupIdentity
}

// Instance uses the actual persisted runtime identity for bot and returns its running generation.
func (probe groupAssemblyProbe) Instance(ctx context.Context, bot uint) (gc.Identity, error) {
	return probe.identity.Instance(ctx, bot)
}

// Check returns synthetic permissions after querying SQLite outside any transaction.
func (probe groupAssemblyProbe) Check(ctx context.Context, bot uint, chat int64) gc.ProbeResult {
	var count int64
	if err := probe.db.WithContext(ctx).Model(&rd.Task{}).Count(&count).Error; err != nil {
		return gc.ProbeResult{ErrorClass: "database"}
	}
	return gc.ProbeResult{Observation: gd.Observation{Generation: 7, BotTelegramID: 100, ChatType: "supergroup", IsMember: true, MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true}}
}

// groupAssembly returns production container wiring over the existing MaxOpenConns=1 fixture.
// Only platform checks are synthetic; membership recovery and action RBAC use real stores.
func groupAssembly(t *testing.T) (*Container, *membershipAccessFixture, *rd.Task) {
	t.Helper()
	fixture := newMembershipAccessFixture(t, false)
	if err := fixture.db.Model(&md.LedgerSource{}).Where("id = 1").Update("order_id", 1).Error; err != nil {
		t.Fatal(err)
	}
	models := append(mr.Models(), &rd.Task{}, &rd.Step{}, &rd.ScopeSlot{}, &gd.Operation{}, &gd.ModuleConfig{}, &gd.RecoveryFence{})
	if err := fixture.db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	auth, err := authz.NewService(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	identities := supportAdminIdentityStore{admins: map[uint]*admin.Admin{7: {ID: 7}}}
	container := &Container{AdminStore: identities, AuthzService: auth}
	if err := auth.SetAdminRoles(7, []string{"system_admin"}); err != nil {
		t.Fatal(err)
	}
	previous := gormdb.DB
	gormdb.DB = fixture.db
	defer func() { gormdb.DB = previous }()
	container.initGroupRecovery(rs.New(fixture.db))
	if container.TelegramGroupRecovery.Membership != nil {
		t.Fatal("read-only scanner exposed as executable membership recovery")
	}
	installGroupMembership(t, container, fixture)
	gormdb.DB = previous
	executor := container.TelegramGroupRecovery
	if executor == nil || executor.Groups != container.TelegramGroups || executor.Store != container.TelegramRecovery.Store || executor.Identity != container.TelegramGroups.Probe || any(executor.Routes) != any(container.TelegramGroups.Store) {
		t.Fatal("production group dependencies not shared")
	}
	container.TelegramGroups.Probe = groupAssemblyProbe{db: fixture.db, identity: executor.Identity}
	task, err := container.TelegramRecovery.PreviewGroup(fixture.ctx, 7, app.GroupPreflightInput{ReplacementInput: gc.ReplacementInput{TargetID: 2, ExpectedVersion: 1, ExpectedRouteVersion: 1, NewBotInstanceID: 1, NewTelegramChatID: "-2001"}, RequestKey: "assembly", Reason: "synthetic replacement"})
	if err != nil {
		t.Fatal(err)
	}
	return container, fixture, task
}

// TestBotPreparationExactRBAC uses real Casbin policies and the production permission adapter.
// A similarly shaped grant cannot authorize preparation; revocation applies immediately.
func TestBotPreparationExactRBAC(t *testing.T) {
	container, fixture, _ := groupAssembly(t)
	if container.TelegramRecovery.Bots == nil {
		t.Fatal("bot preparation not wired")
	}
	if err := container.authorizeGroupRecovery(fixture.ctx, 7, "bot-preflight"); err != nil {
		t.Fatal(err)
	}
	if err := container.AuthzService.SetAdminRoles(7, []string{"readonly_auditor"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"bot-preflight", "confirm", "resume"} {
		if err := container.authorizeGroupRecovery(fixture.ctx, 7, action); err == nil {
			t.Fatalf("revoked %s allowed", action)
		}
	}
	response := groupHTTP(container, http.MethodPost, "/telegram-bot/recovery/bot-preflight", `{"source_bot_id":"1","destination_bot_id":"2","request_key":"revoked","reason":"test"}`)
	if response.Code != http.StatusForbidden {
		t.Fatal(response.Code, response.Body.String())
	}
}

// groupHTTP executes production handler construction with a trusted actor and synthetic HTTP request.
func groupHTTP(container *Container, method, path, body string) *httptest.ResponseRecorder {
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	rh.RegisterRoutes(router, container.TelegramRecoveryHandler())
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	return result
}

// TestGroupRecoveryActualAssembly verifies actual confirm/resume and purpose-aware HTTP availability.
func TestGroupRecoveryActualAssembly(t *testing.T) {
	for _, scenario := range []string{"complete", "unproven", "remaining", "unknown", "support", "stale", "revoked", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			container, fixture, task := groupAssembly(t)
			platform := groupMembershipPlatform(t, container)
			api := platform.API.(*groupMembershipAPI)
			if scenario == "unproven" {
				api.status = "left"
			}
			if scenario == "support" {
				if err := fixture.db.Model(&gd.Target{}).Where("id = 2").Update("purpose", "support").Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing" {
				container.TelegramGroupRecovery.Membership = nil
			}
			path := fmt.Sprintf("/telegram-bot/recovery/tasks/%d", task.ID)
			response := groupHTTP(container, http.MethodGet, path, "")
			var detail struct {
				Data struct {
					Available bool `json:"execution_available"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Data.Available != (scenario != "support" && scenario != "missing") {
				t.Fatal(response.Body.String())
			}
			confirm := groupHTTP(container, http.MethodPost, path+"/confirm", fmt.Sprintf(`{"preflight_ref":%q,"expected_version":"%d"}`, task.PreflightRef, task.Version))
			if scenario == "support" || scenario == "missing" {
				if confirm.Code == 200 {
					t.Fatal("unsupported confirmation accepted")
				}
				verifyGroupOutcome(t, container, fixture, task.ID, scenario)
				return
			}
			if confirm.Code != 200 {
				t.Fatal(confirm.Body.String())
			}
			saved, err := container.TelegramRecovery.Store.Get(fixture.ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			version := saved.Task.Version
			if scenario == "stale" {
				version++
			}
			if scenario == "revoked" {
				if err := container.AuthzService.SetAdminRoles(7, []string{"readonly_auditor"}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "remaining" || scenario == "unknown" {
				state := "active"
				if scenario == "unknown" {
					state = "unknown"
				}
				cipher, err := platform.Cipher.Seal(membershipAssemblyLink)
				if err != nil {
					t.Fatal(err)
				}
				api.revokeErr = fmt.Errorf("synthetic revoke outcome unknown")
				if err := fixture.db.Create(&md.AccessInvite{TargetID: 2, BotID: 1, ChatID: -1001, RouteVersion: 1, State: state, LinkCipher: cipher, ExpiresAt: fixture.now.Add(time.Hour)}).Error; err != nil {
					t.Fatal(err)
				}
			}
			result := groupHTTP(container, http.MethodPost, path+"/resume", fmt.Sprintf(`{"expected_version":"%d"}`, version))
			if (result.Code == 200) != (scenario == "complete" || scenario == "unproven" || scenario == "remaining") {
				t.Fatal(result.Body.String())
			}
			verifyGroupPlatformOutcome(t, fixture, api, scenario)
			verifyGroupOutcome(t, container, fixture, task.ID, scenario)
		})
	}
}

// verifyGroupOutcome asserts durable fence and membership receipt state for the supplied scenario.
func verifyGroupOutcome(t *testing.T, container *Container, fixture *membershipAccessFixture, taskID uint, scenario string) {
	t.Helper()
	target, err := container.TelegramGroups.Detail(fixture.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	task, err := container.TelegramRecovery.Store.Get(fixture.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "support" || scenario == "missing" || scenario == "stale" || scenario == "revoked" {
		state := rd.Fencing
		if scenario == "support" || scenario == "missing" {
			state = rd.AwaitingConfirmation
		}
		if target.RecoveryTaskID != 0 || target.TelegramChatID != -1001 || task.Task.State != state {
			t.Fatal(target, task)
		}
		return
	}
	var run mr.Run
	if err := fixture.db.First(&run, "task_id = ?", taskID).Error; err != nil {
		t.Fatal(err)
	}
	if scenario == "complete" || scenario == "unproven" || scenario == "remaining" {
		if task.Task.State != rd.Completed || target.RecoveryTaskID != 0 || run.Verified || run.CompletionReceipt != fmt.Sprintf("membership-entry:%d:2:2", taskID) || target.TelegramChatID != -2001 || target.RouteVersion != 2 {
			t.Fatal(task, target, run)
		}
	} else {
		if task.Task.State == rd.Completed || target.RecoveryTaskID != taskID || run.Verified || (run.Remaining == 0 && run.Unknown == 0) {
			t.Fatal(task, target, run)
		}
	}
}

// TestGroupRecoveryExactRBAC rejects wildcard, read-only, missing and revoked administrators.
func TestGroupRecoveryExactRBAC(t *testing.T) {
	container, fixture, task := groupAssembly(t)
	if err := container.AuthzService.GrantRolePolicy("wildcard", "/admin/*", "*"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"system_admin", "support", "readonly_auditor", "wildcard"} {
		if err := container.AuthzService.SetAdminRoles(7, []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"confirm", "resume"} {
			err := container.authorizeGroupRecovery(fixture.ctx, 7, action)
			if (err == nil) != (role == "system_admin") {
				t.Fatalf("role=%s action=%s error=%v", role, action, err)
			}
		}
	}
	if _, err := container.TelegramGroupRecovery.Confirm(fixture.ctx, task.ID, rc.Confirmation{ActorID: 777}); err == nil {
		t.Fatal("missing admin accepted")
	}
}

// TestIdentityRecoveryExactRBAC verifies production grants, wildcard denial and revoked permissions.
func TestIdentityRecoveryExactRBAC(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
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
	auth, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	if err := auth.GrantRolePolicy("wildcard", "/admin/*", "*"); err != nil {
		t.Fatal(err)
	}
	identities := supportAdminIdentityStore{admins: map[uint]*admin.Admin{}}
	container := &Container{AdminStore: identities, AuthzService: auth}
	for index, role := range []string{"system_admin", "support", "readonly_auditor", "wildcard"} {
		actor := uint(index + 1)
		identities.admins[actor] = &admin.Admin{ID: actor}
		if err := auth.SetAdminRoles(actor, []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"identity-preflight", "identity-confirm", "identity-resume", "identity-cancel"} {
			err := container.authorizeIdentityRecovery(context.Background(), actor, action)
			if (err == nil) != (role == "system_admin") {
				t.Fatalf("%s %s %v", role, action, err)
			}
		}
	}
	if err := auth.SetAdminRoles(1, []string{"readonly_auditor"}); err != nil {
		t.Fatal(err)
	}
	if err := container.authorizeIdentityRecovery(context.Background(), 1, "identity-resume"); err == nil {
		t.Fatal("revoked grant accepted")
	}
	if err := container.authorizeIdentityRecovery(context.Background(), 777, "identity-confirm"); err == nil {
		t.Fatal("Telegram identity accepted as admin")
	}
}

// groupMembershipAPI replaces only Telegram API calls; all authorization, encryption,
// route checks and persistence remain production code over one SQLite connection.
type groupMembershipAPI struct {
	t                         *testing.T
	db                        *gorm.DB
	status                    string
	queries, creates, revokes int
	revokeErr                 error
}

// check validates ctx/chat and proves no SQL transaction spans the external call.
// It fails the owning test rather than accepting an unexpected platform request.
func (api *groupMembershipAPI) check(ctx context.Context, chat int64) {
	api.t.Helper()
	if chat != -1001 && chat != -2001 {
		api.t.Fatalf("unexpected recovery chat: %d", chat)
	}
	if err := runtime.CheckExecutionFence(ctx, 1, 7); err != nil {
		api.t.Fatalf("missing real recovery execution fence: %v", err)
	}
	var count int64
	if err := api.db.WithContext(ctx).Model(&rd.Task{}).Count(&count).Error; err != nil {
		api.t.Fatalf("platform call held sole SQLite connection: %v", err)
	}
}

// GetChatMember returns the configured destination observation for the exact trusted
// beneficiary; no persisted membership or recovery receipt is manufactured by the test.
func (api *groupMembershipAPI) GetChatMember(ctx context.Context, _ string, chat, user int64) (runtime.ChatMember, error) {
	api.check(ctx, chat)
	if chat != -2001 || user != membershipAssemblyTelegramID {
		api.t.Fatal("query escaped destination/beneficiary scope")
	}
	api.queries++
	return runtime.ChatMember{User: runtime.UserReference{ID: user}, Status: api.status, IsMember: api.status == "member"}, nil
}

// CreateChatInviteLink returns a bounded approval-only link for request, without
// claiming either delivery or admission; ctx is checked against the production fence.
func (api *groupMembershipAPI) CreateChatInviteLink(ctx context.Context, _ string, request runtime.CreateChatInviteLinkRequest) (runtime.InviteLink, error) {
	api.check(ctx, request.ChatID)
	api.creates++
	return runtime.InviteLink{InviteLink: membershipAssemblyLink, Creator: runtime.UserReference{ID: 100}, CreatesJoinRequest: true, ExpireDate: request.ExpireDate}, nil
}

// RevokeChatInviteLink returns a matching revoked link or configured failure for
// the old chat; only production code may persist the resulting disposition.
func (api *groupMembershipAPI) RevokeChatInviteLink(ctx context.Context, _ string, chat int64, link string) (runtime.InviteLink, error) {
	api.check(ctx, chat)
	api.revokes++
	return runtime.InviteLink{InviteLink: link, Creator: runtime.UserReference{ID: 100}, IsRevoked: true}, api.revokeErr
}

// ApproveChatJoinRequest rejects unexpected writes because these fixtures supply
// no trusted join request; ctx/token/chat/user never authorize a fabricated approval.
func (api *groupMembershipAPI) ApproveChatJoinRequest(context.Context, string, int64, int64) error {
	api.t.Error("unexpected approval without trusted join")
	return errors.New("unexpected approval")
}

// DeclineChatJoinRequest rejects unexpected declines; no fixture supplies a join.
func (api *groupMembershipAPI) DeclineChatJoinRequest(context.Context, string, int64, int64) error {
	api.t.Error("unexpected decline without trusted join")
	return errors.New("unexpected decline")
}

// BanChatMember rejects unexpected removals; no fixture contains an old member.
func (api *groupMembershipAPI) BanChatMember(context.Context, string, int64, int64) error {
	api.t.Error("unexpected member removal")
	return errors.New("unexpected removal")
}

// UnbanChatMember rejects unexpected unbans; no fixture owns a prior ban.
func (api *groupMembershipAPI) UnbanChatMember(context.Context, string, int64, int64) error {
	api.t.Error("unexpected member unban")
	return errors.New("unexpected unban")
}

// installGroupMembership calls actual production assembly with real cipher,
// authorizer and runtime store, attaching its returned bridge to both entrypoints.
// It returns only the fake Telegram API, never a substitute recovery executor.
func installGroupMembership(t *testing.T, container *Container, fixture *membershipAccessFixture) *groupMembershipAPI {
	t.Helper()
	api := &groupMembershipAPI{t: t, db: fixture.db, status: "member"}
	platform := &membershipapi.Access{Store: rs.New(fixture.db), Cipher: fixture.service.Cipher, API: api, Query: api, Lifecycle: api}
	cipher, err := platform.Cipher.Seal("synthetic-group-recovery-credential")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&runtimedomain.Instance{}).Where("id = 1").Update("token_cipher", cipher).Error; err != nil {
		t.Fatal(err)
	}
	recovery := container.initMembershipGroupRecovery(platform, fixture.service.Authorizer)
	fixture.website.Recovery, fixture.bridge.Recovery = recovery, recovery
	assertMembershipRecoveryPorts(t, container, fixture.bridge, fixture.website)
	worker, ok := container.TelegramGroupRecovery.Membership.(*mr.GroupRecovery)
	if !ok || worker.Store == nil || worker.Authorizer != fixture.service.Authorizer || worker.Authority == nil {
		t.Fatal("production executable membership recovery not installed")
	}
	assertGroupLedgerUnchanged(t, fixture)
	return api
}

// assertMembershipRecoveryPorts checks website/runtime share the same concrete
// bridge, which must retain the production executor and actual parent repository.
func assertMembershipRecoveryPorts(t *testing.T, container *Container, bridge *accessbootstrap.Runtime, website *accessbootstrap.Website) {
	t.Helper()
	recovery, ok := website.Recovery.(*accessbootstrap.Recovery)
	if !ok || recovery == nil || bridge.Recovery != website.Recovery || recovery.Groups == nil || recovery.Parents != container.TelegramRecovery.Store || recovery.Worker != container.TelegramGroupRecovery.Membership {
		t.Fatal("website/runtime recovery ports do not share production worker/parent")
	}
	modules, ok := container.TelegramRecovery.Bots.Modules.(botRecoveryModules)
	if !ok || modules.support == nil {
		t.Fatal("independent bot recovery modules not installed")
	}
	membershipModule, ok := modules.membership.(*membershipbridge.Modules)
	if !ok || recovery.BotWorker == nil || membershipModule.Recovery != recovery.BotWorker {
		t.Fatal("website/runtime and bot continuation do not share admission worker")
	}
	if recovery.BotWorker.Admission == nil || recovery.BotWorker.Authority == nil || recovery.BotWorker.Authorizer == nil || recovery.BotWorker.Sponsor == nil {
		t.Fatal("cross-bot worker missing task-bound admission authority")
	}
}

// groupMembershipPlatform returns the actual executor's platform for configuring
// API observations only; t rejects fallback to the legacy evidence scanner.
func groupMembershipPlatform(t *testing.T, container *Container) *membershipapi.Access {
	t.Helper()
	worker, ok := container.TelegramGroupRecovery.Membership.(*mr.GroupRecovery)
	if !ok {
		t.Fatal("membership recovery is not the production executor")
	}
	factory, ok := worker.Platforms.(*membershipapi.RecoveryFactory)
	if !ok {
		t.Fatal("membership recovery bypassed production platform factory")
	}
	return factory.Access
}

// assertGroupLedgerUnchanged snapshots historical finance/entitlement rows and
// compares complete serialized records at cleanup, including their timestamps.
func assertGroupLedgerUnchanged(t *testing.T, fixture *membershipAccessFixture) {
	t.Helper()
	snapshot := func() string {
		var sources []md.LedgerSource
		var projections []md.LedgerProjection
		var revocations []md.LedgerRevocation
		var items []md.OrderItemSnapshot
		for _, rows := range []any{&sources, &projections, &revocations, &items} {
			if err := fixture.db.Find(rows).Error; err != nil {
				t.Fatal(err)
			}
		}
		encoded, err := json.Marshal([]any{sources, projections, revocations, items})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	before := snapshot()
	t.Cleanup(func() {
		if after := snapshot(); after != before {
			t.Error("group recovery mutated historical ledger/projection/revocation/snapshot")
		}
	})
}

// TestMembershipGroupRecoveryAssemblyFailClosed invokes production construction
// with each missing dependency. No scanner, confirmation or platform I/O may survive.
func TestMembershipGroupRecoveryAssemblyFailClosed(t *testing.T) {
	for _, missing := range []string{"database", "platform", "cipher", "api", "query", "lifecycle", "identity", "parent_store", "parent", "group"} {
		t.Run(missing, func(t *testing.T) {
			container, fixture, task := groupAssembly(t)
			platform := groupMembershipPlatform(t, container)
			api := platform.API.(*groupMembershipAPI)
			var identity membership.AccessAuthorizer = fixture.service.Authorizer
			container.TelegramGroupRecovery.Membership = nil
			previous := gormdb.DB
			gormdb.DB = fixture.db
			defer func() { gormdb.DB = previous }()
			switch missing {
			case "database":
				gormdb.DB = nil
			case "platform":
				platform = nil
			case "cipher":
				platform.Cipher = nil
			case "api":
				platform.API = nil
			case "query":
				platform.Query = nil
			case "lifecycle":
				platform.Lifecycle = nil
			case "identity":
				identity = nil
			case "parent_store":
				container.TelegramRecovery.Store = nil
			case "parent":
				container.TelegramRecovery = nil
			case "group":
				container.TelegramGroupRecovery = nil
			}
			if recovery := container.initMembershipGroupRecovery(platform, identity); recovery != nil {
				t.Fatal("missing dependency exposed website/inbox recovery")
			}
			if container.TelegramGroupRecovery != nil && container.TelegramGroupRecovery.Membership != nil {
				t.Fatal("missing dependency installed executable recovery")
			}
			var saved rd.Task
			if err := fixture.db.First(&saved, task.ID).Error; err != nil || saved.State != rd.AwaitingConfirmation || saved.Version != task.Version {
				t.Fatalf("failed assembly mutated parent: %+v %v", saved, err)
			}
			if api.queries != 0 || api.creates != 0 || api.revokes != 0 {
				t.Fatal("assembly performed platform I/O")
			}
			countMembershipRows(t, fixture.db, &mr.Run{}, 0)
		})
	}
}

// verifyGroupPlatformOutcome verifies entry replacement never generates bulk
// invites, queries admission, or mutates old membership. All old records survive.
func verifyGroupPlatformOutcome(t *testing.T, fixture *membershipAccessFixture, api *groupMembershipAPI, scenario string) {
	t.Helper()
	if api.queries != 0 || api.creates != 0 || api.revokes != 0 {
		t.Fatal("entry switch performed membership platform I/O")
	}
	var count int64
	if err := fixture.db.Model(&md.AccessMember{}).Where("target_id = ? AND chat_id = ? AND route_version = ?", 2, -2001, 2).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("entry switch fabricated destination admission", count, err)
	}
	if err := fixture.db.Model(&md.AccessInvite{}).Where("chat_id = ? AND route_version = ?", -2001, 2).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("entry switch generated destination invite", count, err)
	}
	if scenario == "remaining" || scenario == "unknown" {
		var invite md.AccessInvite
		if err := fixture.db.Where("chat_id = ? AND route_version = ?", -1001, 1).First(&invite).Error; err != nil {
			t.Fatal(err)
		}
		expected := "active"
		if scenario == "unknown" {
			expected = "unknown"
		}
		if invite.State != expected || invite.LinkCipher == "" {
			t.Fatal("old invitation evidence changed", invite.ID, invite.State)
		}
	}
}
