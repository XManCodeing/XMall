package gormstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/crypto"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	ud "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// accessHarness 为测试提供单连接数据库、可信绑定和平台调用计数，不创建联系人表。
type accessHarness struct {
	store         *s.Store
	db            *gorm.DB
	service       *a.AccessService
	now           time.Time
	route         c.AccessRoute
	identity      c.AccessIdentity
	deny          bool
	routeErr      error
	authErr       error
	calls         int
	approvals     int
	declines      int
	outcome       string
	fence         bool
	afterCall     func()
	memberStatus  string
	memberOutcome string
	onMember      func(context.Context)
}

// accessSetup 构建隔离 t 数据库和历史来源，所有连接固定 MaxOpenConns=1。
func accessSetup(t *testing.T) (*accessHarness, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:access-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	models := append(s.AccessModels(), s.LedgerModels()...)
	models = append(models, &ud.User{}, &ei.Identity{}, &gd.Target{}, &od.Order{}, &od.OrderItem{}, &d.OrderItemSnapshot{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	harness := &accessHarness{store: s.New(db), db: db, now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), route: c.AccessRoute{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, Generation: 3}, identity: c.AccessIdentity{AccountID: 7, TelegramID: 9007199254740993, BotID: 9}, outcome: c.AccessSucceeded, fence: true}
	if err := db.Create(&gd.Target{ID: harness.route.TargetID, Purpose: "membership", BotInstanceID: harness.route.BotID, TelegramChatID: harness.route.ChatID, RouteVersion: harness.route.RouteVersion, OccupancySlot: "access:1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ud.User{ID: 7, Email: "access@example.invalid", Status: constants.UserStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&ei.Identity{UserID: 7, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: fmt.Sprint(harness.identity.TelegramID)}).Error; err != nil {
		t.Fatal(err)
	}
	source := d.LedgerSource{OrderItemID: 1, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: harness.identity.TelegramID, AccountID: 7, OrderID: 1, PaidAt: harness.now.Add(-time.Hour), Kind: d.TermMonths, Months: 1, CreatedAt: harness.now}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	root := d.LedgerProjection{TargetID: 1, BeneficiaryTelegramID: harness.identity.TelegramID, Version: 1, Active: true, Kind: d.TermMonths, CalculatedAt: harness.now}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	harness.service, err = a.NewAccess(harness.store, harness, harness, harness, harness)
	if err != nil {
		t.Fatal(err)
	}
	harness.service.Now = func() time.Time { return harness.now }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return harness, r.WithExecutionFence(ctx, harness)
}

// MembershipRoute 返回测试当前路由，无网络调用。
func (h *accessHarness) MembershipRoute(context.Context, uint) (c.AccessRoute, error) {
	return h.route, h.routeErr
}

// AuthorizeMembership 模拟已绑定网站身份，不依赖任何联系人。
func (h *accessHarness) AuthorizeMembership(_ context.Context, identity c.AccessIdentity, _ uint) error {
	if h.authErr != nil {
		return h.authErr
	}
	if h.deny || identity != h.identity {
		return c.ErrUntrustedIdentity
	}
	return nil
}

// Seal 使用已有 crypto 实现加密测试秘密。
func (h *accessHarness) Seal(value string) (string, error) {
	return crypto.Encrypt(crypto.DeriveKey("access-test-key"), value)
}

// Open 使用已有 crypto 解密测试秘密。
func (h *accessHarness) Open(value string) (string, error) {
	return crypto.Decrypt(crypto.DeriveKey("access-test-key"), value)
}

// Check 模拟当前受管运行代次，不同代次拒绝。
func (h *accessHarness) Check(_ context.Context, bot uint, generation uint64) error {
	if !h.fence || bot != h.route.BotID || generation != h.route.Generation {
		return r.ErrStale
	}
	return nil
}

// CreateMembershipInvite 返回结构化结果，并记录真实平台调用次数。
func (h *accessHarness) CreateMembershipInvite(_ context.Context, input c.AccessCreateInvite) c.AccessInviteResult {
	h.calls++
	if h.afterCall != nil {
		h.afterCall()
	}
	return c.AccessInviteResult{AccessAPIResult: c.AccessAPIResult{Outcome: h.outcome}, Link: "https://t.me/+abcdefghijk"}
}

// ApproveMembershipJoin 记录批准次数，不创建成员观测。
func (h *accessHarness) ApproveMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	h.approvals++
	if h.afterCall != nil {
		h.afterCall()
	}
	return c.AccessAPIResult{Outcome: h.outcome}
}

// DeclineMembershipJoin 记录拒绝次数，不影响已批准结果。
func (h *accessHarness) DeclineMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	h.declines++
	if h.afterCall != nil {
		h.afterCall()
	}
	return c.AccessAPIResult{Outcome: h.outcome}
}

// RevokeMembershipInvite 返回测试撤销结果。
func (h *accessHarness) RevokeMembershipInvite(context.Context, c.AccessRoute, string) c.AccessAPIResult {
	return c.AccessAPIResult{Outcome: h.outcome}
}

// GetMembershipMember 返回定点成员事实，不能确认邀请操作。
func (h *accessHarness) GetMembershipMember(ctx context.Context, _ c.AccessJoinAction) c.AccessMemberResult {
	if h.onMember != nil {
		h.onMember(ctx)
	}
	status, outcome := h.memberStatus, h.memberOutcome
	if status == "" {
		status = "member"
	}
	if outcome == "" {
		outcome = c.AccessSucceeded
	}
	return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: outcome}, Status: status, IsMember: status == "member"}
}

// accessActive 创建并执行真实持久工作，返回授权入口及秘密供申请测试。
func accessActive(t *testing.T, h *accessHarness, ctx context.Context) *c.AccessEntry {
	t.Helper()
	entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	if entry.InviteState != "pending" {
		t.Fatal(entry)
	}
	if count, err := h.service.RunAccess(ctx, 9, 10); err != nil || count != 1 {
		t.Fatalf("run %d %v", count, err)
	}
	entry, err = h.service.RequestMembershipInvite(ctx, h.identity, 1)
	if err != nil || entry.InviteState != "active" || entry.Link == "" {
		t.Fatalf("entry %#v %v", entry, err)
	}
	return entry
}

// TestAccessLifecycleNoContact 覆盖单连接外部调用、密文、同人审批、转发拒绝和未知入口持久待办。
func TestAccessLifecycleNoContact(t *testing.T) {
	h, ctx := accessSetup(t)
	h.afterCall = func() {
		var count int64
		if err := h.db.Model(&d.AccessInvite{}).Count(&count).Error; err != nil {
			t.Error(err)
		}
	}
	entry := accessActive(t, h, ctx)
	row, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil {
		t.Fatal(err)
	}
	if row.LinkCipher == entry.Link || strings.Contains(row.LinkCipher, "t.me") || len(row.LinkHash) != 64 {
		t.Fatal("secret not protected")
	}
	input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
	if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	if h.approvals != 1 {
		t.Fatal("approval not idempotent")
	}
	var members int64
	if err := h.db.Model(&d.AccessMember{}).Count(&members).Error; err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Fatal("approval invented membership")
	}
	input.UpdateID = 2
	input.TelegramID = 88
	if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	if h.declines != 1 || h.approvals != 1 {
		t.Fatal("forwarded invite approved")
	}
	input.UpdateID = 3
	input.InviteLink = "https://t.me/+unknownxxx"
	if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
		t.Fatal(err)
	}
	var join d.AccessJoin
	if err := h.db.First(&join, "update_id = ?", 3).Error; err != nil {
		t.Fatal(err)
	}
	if join.State != "needs_review" {
		t.Fatal(join)
	}
	list, err := h.service.ListMemberships(ctx, h.identity, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), entry.Link) || !strings.Contains(string(raw), `"telegram_id":"9007199254740993"`) {
		t.Fatal(string(raw))
	}
}

// TestAccessUnknownLeaseCAS 覆盖创建超时不可重放、租约回收 unknown 和旧 owner 完成拒绝。
func TestAccessUnknownLeaseCAS(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		h, ctx := accessSetup(t)
		h.outcome = c.AccessUnknown
		entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
			t.Fatal(err)
		}
		h.now = h.now.Add(48 * time.Hour)
		if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
			t.Fatal(err)
		}
		again, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
		if err != nil {
			t.Fatal(err)
		}
		if h.calls != 1 || again.InviteID != entry.InviteID || again.InviteState != "unknown" {
			t.Fatal("unknown replayed")
		}
	})
	t.Run("lease", func(t *testing.T) {
		h, ctx := accessSetup(t)
		entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
		if err != nil {
			t.Fatal(err)
		}
		operation, err := h.store.ClaimAccess(ctx, 9, "0123456789abcdef", h.now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.ClaimAccess(ctx, 9, "fedcba9876543210", h.now.Add(2*time.Minute), time.Minute); !errors.Is(err, c.ErrNotFound) {
			t.Fatal(err)
		}
		err = h.store.FinishAccess(ctx, operation, c.AccessAPIResult{Outcome: c.AccessSucceeded}, "cipher", strings.Repeat("a", 64), h.now.Add(2*time.Minute))
		if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
		invite, err := h.store.Invite(ctx, entry.InviteID)
		if err != nil || invite.State != "unknown" {
			t.Fatalf("%#v %v", invite, err)
		}
	})
}

// TestAccessCurrentBindingExpiryPermanent 覆盖换绑拒绝、实时到期及永久覆盖旧限时来源。
func TestAccessCurrentBindingExpiryPermanent(t *testing.T) {
	h, ctx := accessSetup(t)
	h.deny = true
	if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatal(err)
	}
	h.deny = false
	h.now = h.now.Add(31 * 24 * time.Hour)
	eligible, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
	if err != nil || eligible.Active {
		t.Fatalf("stale Active used %#v %v", eligible, err)
	}
	source := d.LedgerSource{OrderItemID: 2, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: h.identity.TelegramID, AccountID: 7, PaidAt: h.now.Add(-time.Hour), Kind: d.TermPermanent, CreatedAt: h.now}
	if err := h.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	eligible, err = h.store.Eligibility(ctx, h.identity, 1, h.now)
	if err != nil || !eligible.Active || eligible.Kind != d.TermPermanent || eligible.ExpiresAt != nil {
		t.Fatalf("permanent %#v %v", eligible, err)
	}
	accessActive(t, h, ctx)
}

// TestAccessObservationsTrustAndOrdering 覆盖系统外身份不纳管、restricted 语义及重复乱序防护。
func TestAccessObservationsTrustAndOrdering(t *testing.T) {
	h, ctx := accessSetup(t)
	input := c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: 88, Status: "member", ObservedAt: h.now, Sequence: 20, Source: "chat_member"}
	if changed, err := h.service.ObserveMembership(ctx, input); err != nil || changed {
		t.Fatalf("untrusted %t %v", changed, err)
	}
	input.TelegramID = h.identity.TelegramID
	input.Status = "restricted"
	input.IsMember = true
	if changed, err := h.service.ObserveMembership(ctx, input); err != nil || !changed {
		t.Fatalf("trusted %t %v", changed, err)
	}
	input.Status = "left"
	input.Sequence = 19
	if changed, err := h.service.ObserveMembership(ctx, input); err != nil || changed {
		t.Fatalf("stale %t %v", changed, err)
	}
	var member d.AccessMember
	if err := h.db.First(&member).Error; err != nil {
		t.Fatal(err)
	}
	if member.Status != "restricted" || !member.IsMember {
		t.Fatal(member)
	}
	input.Sequence = 21
	input.Status = "kicked"
	if changed, err := h.service.ObserveMembership(ctx, input); err != nil || !changed {
		t.Fatalf("new %t %v", changed, err)
	}
}

// TestAccessConcurrentSingleSlot 验证多个入口请求共享单一持久槽位和创建任务。
func TestAccessConcurrentSingleSlot(t *testing.T) {
	h, ctx := accessSetup(t)
	var workers sync.WaitGroup
	errorsFound := make(chan error, 12)
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			errorsFound <- err
		}()
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := h.db.Model(&d.AccessOperation{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(count)
	}
}

// TestAccessFenceAndPostflight 验证无受管上下文不发 API、调用后代次丢失保留 unknown。
func TestAccessFenceAndPostflight(t *testing.T) {
	t.Run("no_fence", func(t *testing.T) {
		h, ctx := accessSetup(t)
		if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.RunAccess(context.Background(), 9, 1); err != nil {
			t.Fatal(err)
		}
		if h.calls != 0 {
			t.Fatal("unsupervised call")
		}
	})
	t.Run("lost_after_call", func(t *testing.T) {
		h, ctx := accessSetup(t)
		entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
		if err != nil {
			t.Fatal(err)
		}
		h.afterCall = func() { h.fence = false }
		if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
			t.Fatal(err)
		}
		row, err := h.store.Invite(ctx, entry.InviteID)
		if err != nil || row.State != "unknown" {
			t.Fatalf("%#v %v", row, err)
		}
	})
}

// TestAccessRetryAndRouteFencing 验证明确信号才重试，路由变化或换绑阻止新的外部授权。
func TestAccessRetryAndRouteFencing(t *testing.T) {
	t.Run("retryable", func(t *testing.T) {
		h, ctx := accessSetup(t)
		h.outcome = c.AccessRetryable
		entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
			t.Fatal(err)
		}
		row, err := h.store.Invite(ctx, entry.InviteID)
		if err != nil || row.State != "failed" {
			t.Fatalf("%#v %v", row, err)
		}
		h.now = h.now.Add(2 * time.Minute)
		h.outcome = c.AccessSucceeded
		if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
			t.Fatal(err)
		}
		if h.calls != 2 {
			t.Fatal(h.calls)
		}
	})
	t.Run("route_changed", func(t *testing.T) {
		h, ctx := accessSetup(t)
		if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
			t.Fatal(err)
		}
		h.route.RouteVersion++
		if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
			t.Fatal(err)
		}
		if h.calls != 0 {
			t.Fatal("stale route invoked")
		}
	})
	t.Run("binding_changed", func(t *testing.T) {
		h, ctx := accessSetup(t)
		if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
			t.Fatal(err)
		}
		h.deny = true
		if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
			t.Fatal(err)
		}
		if h.calls != 0 {
			t.Fatal("changed binding invoked")
		}
	})
}

// TestAccessJoinWatermark 覆盖新未知链接挡旧批准/拒绝、乱序、重复及同秒排序。
func TestAccessJoinWatermark(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(fmt.Sprintf("decline_%t", decline), func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			h.deny = decline
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 20, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			old := input
			input.UpdateID++
			input.InviteLink = "https://t.me/+unknownxxx"
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			for _, replay := range []c.AccessJoinRequest{old, input, old} {
				if err := h.service.ReceiveMembershipJoin(ctx, replay); err != nil {
					t.Fatal(err)
				}
			}
			// 更晚事件时间优先于更大的旧 update 序号（长时间无更新时序号可能重置）。
			old.UpdateID = 99
			old.ReceivedAt = h.now.Add(-time.Second)
			if err := h.service.ReceiveMembershipJoin(ctx, old); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			if h.approvals != 0 || h.declines != 0 {
				t.Fatal("old action reached new unknown request")
			}
			var executable int64
			if err := h.db.Model(&d.AccessOperation{}).Where("join_id <> 0 AND state IN ?", []string{"pending", "retryable"}).Count(&executable).Error; err != nil || executable != 0 {
				t.Fatalf("old executable tasks: %d %v", executable, err)
			}
		})
	}
}

// accessFaultStore 注入事务外读故障和最终检查前的新申请；内嵌真实仓储保留单连接约束。
type accessFaultStore struct {
	c.AccessRepository
	lookupErr   error
	eligibleErr error
	onCurrent   func()
	checks      int
}

// InviteByHash 按配置返回 lookupErr，否则委托真实仓储。
func (store *accessFaultStore) InviteByHash(ctx context.Context, bot uint, chat int64, hash string) (*d.AccessInvite, error) {
	if store.lookupErr != nil {
		return nil, store.lookupErr
	}
	return store.AccessRepository.InviteByHash(ctx, bot, chat, hash)
}

// Eligibility 按配置返回 eligibleErr，否则委托真实单连接数据库。
func (store *accessFaultStore) Eligibility(ctx context.Context, identity c.AccessIdentity, target uint, at time.Time) (c.AccessEligibility, error) {
	if store.eligibleErr != nil {
		return c.AccessEligibility{}, store.eligibleErr
	}
	return store.AccessRepository.Eligibility(ctx, identity, target, at)
}

// CurrentJoin 在第三次（紧邻平台调用）复核前注入更新，并返回真实校验结果。
func (store *accessFaultStore) CurrentJoin(ctx context.Context, operation *d.AccessOperation, at time.Time) (*d.AccessJoin, error) {
	store.checks++
	if store.checks == 3 && store.onCurrent != nil {
		store.onCurrent()
	}
	return store.AccessRepository.CurrentJoin(ctx, operation, at)
}

// TestAccessJoinFaultReplay 验证各种基础设施故障不固化拒绝，且先阻断旧任务、支持同 update 重放。
func TestAccessJoinFaultReplay(t *testing.T) {
	for _, failure := range []string{"lookup", "eligibility", "route", "authorization", "capability"} {
		t.Run(failure, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("temporary dependency failure")
			store := &accessFaultStore{AccessRepository: h.store}
			h.service.Store = store
			switch failure {
			case "lookup":
				store.lookupErr = fault
			case "eligibility":
				store.eligibleErr = fault
			case "route":
				h.routeErr = fault
			case "authorization":
				h.authErr = fault
			case "capability":
				fault = c.ErrCapability
				h.routeErr = fault
			}
			input.UpdateID++
			if err := h.service.ReceiveMembershipJoin(ctx, input); !errors.Is(err, fault) {
				t.Fatalf("fault lost: %v", err)
			}
			var join d.AccessJoin
			if err := h.db.First(&join, "update_id = ?", input.UpdateID).Error; err != nil || join.Decision != "needs_review" {
				t.Fatalf("failure persisted decline: %#v %v", join, err)
			}
			store.lookupErr, store.eligibleErr, h.routeErr, h.authErr = nil, nil, nil, nil
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil || h.approvals != 0 || h.declines != 0 {
				t.Fatalf("old task survived fault: %v", err)
			}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil || h.approvals != 1 || h.declines != 0 {
				t.Fatalf("inbox replay failed: %v", err)
			}
		})
	}
}

// TestAccessJoinFinalWatermark 验证已抢占任务在最终读取水位时也挡住新的未知申请。
func TestAccessJoinFinalWatermark(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(fmt.Sprintf("decline_%t", decline), func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			h.deny = decline
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			store := &accessFaultStore{AccessRepository: h.store}
			store.onCurrent = func() {
				input.UpdateID++
				input.InviteLink = "unknown"
				if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			h.service.Store = store
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			if store.checks != 3 || h.approvals != 0 || h.declines != 0 {
				t.Fatal("claimed old action reached platform")
			}
		})
	}
}

// TestAccessDeclineRevalidation 验证拒绝执行前重新判断，故障重试而非拒绝，恢复合法后不拒绝。
func TestAccessDeclineRevalidation(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovered_%t", recovery), func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			h.deny = true
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			h.authErr = errors.New("temporary authorization database error")
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil || h.declines != 0 {
				t.Fatalf("fault became decline: %v", err)
			}
			var operation d.AccessOperation
			if err := h.db.First(&operation, "kind = ?", "decline").Error; err != nil || operation.State != c.AccessRetryable {
				t.Fatalf("fault not retryable: %#v %v", operation, err)
			}
			h.authErr = nil
			h.deny = !recovery
			h.now = h.now.Add(2 * time.Minute)
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			if recovery && h.declines != 0 || !recovery && h.declines != 1 {
				t.Fatal("decline was not revalidated")
			}
		})
	}
}

// TestAccessJoinDuringPlatformCall 验证无法消除的外部竞态保留 unknown，而非宣称旧批准已确认。
func TestAccessJoinDuringPlatformCall(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(fmt.Sprintf("decline_%t", decline), func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			h.deny = decline
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			h.afterCall = func() {
				input.UpdateID++
				input.InviteLink = "unknown"
				if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			var operation d.AccessOperation
			if err := h.db.First(&operation, "join_id <> 0").Error; err != nil || operation.State != c.AccessUnknown {
				t.Fatalf("external race hidden: %#v %v", operation, err)
			}
			if h.approvals+h.declines != 1 {
				t.Fatal("unexpected platform replay")
			}
		})
	}
}

// TestAccessJoinConcurrentOrdering 验证并发重复/乱序接收只执行最高水位申请。
func TestAccessJoinConcurrentOrdering(t *testing.T) {
	h, ctx := accessSetup(t)
	entry := accessActive(t, h, ctx)
	var workers sync.WaitGroup
	failures := make(chan error, 16)
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func(sequence int64) {
			defer workers.Done()
			input := c.AccessJoinRequest{BotID: 9, UpdateID: sequence, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			failures <- h.service.ReceiveMembershipJoin(ctx, input)
		}(int64(index%8 + 1))
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.service.RunAccess(ctx, 9, 100); err != nil {
		t.Fatal(err)
	}
	var join d.AccessJoin
	if err := h.db.First(&join, "update_id = ?", 8).Error; err != nil || join.State != "approved" || h.approvals != 1 {
		t.Fatalf("latest not exclusively approved: %#v %v calls=%d", join, err, h.approvals)
	}
}

// TestAccessAuthorizationSnapshot 验证真实单连接当前账号、绑定变化及同人重绑版本。
func TestAccessAuthorizationSnapshot(t *testing.T) {
	for _, change := range []string{"rebind", "replace", "disabled"} {
		t.Run(change, func(t *testing.T) {
			h, ctx := accessSetup(t)
			snapshot, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
			if err != nil || snapshot.BindingVersion == 0 {
				t.Fatalf("snapshot: %+v %v", snapshot, err)
			}
			accessChangeBinding(t, h, change)
			identity := h.identity
			identity.Authorization = &snapshot
			if _, err := h.store.EnsureInvite(ctx, identity, h.route, h.now, h.now.Add(time.Hour)); err == nil {
				t.Fatal("stale authorization enqueued invite")
			}
			historical := c.AccessIdentity{TelegramID: h.identity.TelegramID}
			if eligible, err := h.store.Eligibility(ctx, historical, 1, h.now); err != nil || !eligible.Active {
				t.Fatalf("current binding affected history: %+v %v", eligible, err)
			}
		})
	}
}

// accessChangeBinding 修改真实绑定或账号状态；失败终止 t，不依赖模拟 authorizer。
func accessChangeBinding(t *testing.T, h *accessHarness, change string) {
	t.Helper()
	var err error
	switch change {
	case "rebind":
		err = h.db.Model(&ei.Identity{}).Where("user_id = 7").Update("provider_user_id", "88").Error
	case "replace":
		if err = h.db.Where("user_id = 7").Delete(&ei.Identity{}).Error; err == nil {
			err = h.db.Create(&ei.Identity{UserID: 7, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: fmt.Sprint(h.identity.TelegramID)}).Error
		}
	case "disabled":
		err = h.db.Model(&ud.User{}).Where("id = 7").Update("status", "disabled").Error
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestAccessBindingCallBoundaries 覆盖成员网络读期间及平台调用后发生的真实绑定变化。
func TestAccessBindingCallBoundaries(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, change := range []string{"rebind", "replace", "disabled"} {
			t.Run(fmt.Sprintf("after_%t_%s", after, change), func(t *testing.T) {
				h, ctx := accessSetup(t)
				entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
				if err != nil {
					t.Fatal(err)
				}
				if after {
					h.afterCall = func() { accessChangeBinding(t, h, change) }
				} else {
					h.onMember = func(context.Context) { accessChangeBinding(t, h, change) }
				}
				if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
					t.Fatal(err)
				}
				invite, err := h.store.Invite(ctx, entry.InviteID)
				if err != nil || (!after && h.calls != 0) || (after && invite.State != "unknown") {
					t.Fatalf("unsafe call result: %+v calls=%d err=%v", invite, h.calls, err)
				}
			})
		}
	}
}

// TestAccessJoinSnapshotAndBan 验证批准入队同事务重绑检查、当前群 kicked 拒绝。
func TestAccessJoinSnapshotAndBan(t *testing.T) {
	for _, change := range []string{"rebind", "replace", "disabled", "kicked"} {
		t.Run(change, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := h.store.Eligibility(ctx, h.identity, 1, h.now)
			if err != nil {
				t.Fatal(err)
			}
			if change == "kicked" {
				_, err = h.store.ObserveAccess(ctx, c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: h.identity.TelegramID, Status: "kicked", ObservedAt: h.now, Source: "query"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.EnsureInvite(ctx, h.identity, h.route, h.now, h.now.Add(time.Hour)); !errors.Is(err, c.ErrUntrustedIdentity) {
					t.Fatalf("kicked invite accepted: %v", err)
				}
			} else {
				accessChangeBinding(t, h, change)
			}
			input := c.AccessJoinRequest{BotID: 9, ChatID: -1001, UpdateID: 1, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now, Authorization: &snapshot}
			if _, err := h.store.EnqueueJoin(ctx, input, invite, "approve", h.now); err == nil {
				t.Fatal("stale or kicked approval enqueued")
			}
		})
	}
}

// TestAccessMemberQueryBoundaries 验证 kicked/left、查询失败保守不批准、网络不占事务连接。
func TestAccessMemberQueryBoundaries(t *testing.T) {
	for _, status := range []string{"kicked", "left", "failure", "changed_after"} {
		t.Run(status, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			input := c.AccessJoinRequest{BotID: 9, ChatID: -1001, UpdateID: 1, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
			if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
				t.Fatal(err)
			}
			h.memberStatus = status
			if status == "failure" {
				h.memberOutcome = c.AccessRetryable
			}
			if status == "changed_after" {
				h.memberStatus = "left"
				h.afterCall = func() { h.memberStatus = "kicked" }
			}
			h.onMember = func(ctx context.Context) {
				var count int64
				if err := h.db.WithContext(ctx).Model(&ud.User{}).Count(&count).Error; err != nil {
					t.Fatalf("member network held transaction: %v", err)
				}
			}
			if _, err := h.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			var operation d.AccessOperation
			if err := h.db.First(&operation, "kind = 'approve'").Error; err != nil {
				t.Fatal(err)
			}
			if status == "left" && h.approvals != 1 || (status == "kicked" || status == "failure") && h.approvals != 0 || status == "changed_after" && operation.State != c.AccessUnknown {
				t.Fatalf("status=%s calls=%d result=%s", status, h.approvals, operation.State)
			}
		})
	}
}

// TestAccessBindingQueryFailure 验证真实身份表读取失败时不回退分连接授权或入队。
func TestAccessBindingQueryFailure(t *testing.T) {
	h, ctx := accessSetup(t)
	if err := h.db.Migrator().DropTable(&ei.Identity{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Eligibility(ctx, h.identity, 1, h.now); err == nil {
		t.Fatal("binding query failure authorized")
	}
	if _, err := h.store.EnsureInvite(ctx, h.identity, h.route, h.now, h.now.Add(time.Hour)); err == nil {
		t.Fatal("binding query failure enqueued")
	}
}

// TestAccessPersistedBindingGeneration 验证排队后同 ID 重绑及历史零值任务失败关闭。
// create/approve 均覆盖；active 邀请不能由新绑定代次领取或再次批准入队。
func TestAccessPersistedBindingGeneration(t *testing.T) {
	for _, kind := range []string{"create", "approve"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_legacy_%t", kind, legacy), func(t *testing.T) {
				h, ctx := accessSetup(t)
				var entry *c.AccessEntry
				var err error
				if kind == "approve" {
					entry = accessActive(t, h, ctx)
					input := c.AccessJoinRequest{BotID: 9, ChatID: -1001, UpdateID: 1, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
					err = h.service.ReceiveMembershipJoin(ctx, input)
				} else {
					entry, err = h.service.RequestMembershipInvite(ctx, h.identity, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				var queued d.AccessOperation
				if err := h.db.First(&queued, "kind = ?", kind).Error; err != nil || queued.BindingVersion == 0 {
					t.Fatalf("missing persisted generation: %+v %v", queued, err)
				}
				if legacy {
					for _, model := range []any{&d.AccessInvite{}, &d.AccessOperation{}} {
						if err := h.db.Model(model).Where("id > 0").Update("binding_version", 0).Error; err != nil {
							t.Fatal(err)
						}
					}
				} else {
					accessChangeBinding(t, h, "replace")
				}
				if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); !errors.Is(err, c.ErrUntrustedIdentity) {
					t.Fatalf("old invite claimed by new/unknown generation: %v", err)
				}
				calls := h.calls
				if _, err := h.service.RunAccess(ctx, 9, 10); err != nil || h.calls != calls || h.approvals != 0 {
					t.Fatalf("stale generation reached platform: %v", err)
				}
				if err := h.db.First(&queued, queued.ID).Error; err != nil || queued.State != c.AccessFailed {
					t.Fatalf("stale task not closed: %+v %v", queued, err)
				}
				if kind == "approve" {
					invite, err := h.store.Invite(ctx, entry.InviteID)
					if err != nil {
						t.Fatal(err)
					}
					input := c.AccessJoinRequest{BotID: 9, ChatID: -1001, UpdateID: 2, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
					if _, err := h.store.EnqueueJoin(ctx, input, invite, "approve", h.now); !errors.Is(err, c.ErrUntrustedIdentity) {
						t.Fatalf("old active invite authorized: %v", err)
					}
				}
			})
		}
	}
}

// TestAccessRevokeLifecycle 验证撤销请求去重，实际成功才转 revoked，不删除历史。
func TestAccessRevokeLifecycle(t *testing.T) {
	h, ctx := accessSetup(t)
	entry := accessActive(t, h, ctx)
	if err := h.service.RevokeMembershipInvite(ctx, h.identity, entry.InviteID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.RevokeMembershipInvite(ctx, h.identity, entry.InviteID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	invite, err := h.store.Invite(ctx, entry.InviteID)
	if err != nil || invite.State != "revoked" {
		t.Fatalf("%#v %v", invite, err)
	}
}
