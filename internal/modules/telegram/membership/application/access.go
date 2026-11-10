package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// AccessService 编排 B6a，只暴露授权查询、邀请申请、可信 update 接收和逐项 worker。
// 所有端口必需，Now 仅供测试注入；生产装配后不要并发修改依赖。
type AccessService struct {
	Store      c.AccessRepository
	Routes     c.AccessRoutes
	Authorizer c.AccessAuthorizer
	Cipher     c.AccessCipher
	Platform   c.AccessPlatform
	Now        func() time.Time
	Deliveries r.InviteDeliveryReader
}

// NewAccess 使用既有仓储/群路由/身份授权/密文/runtime 平台适配器构造独立服务。
// 缺少依赖返回 ErrCapability，不开启会员销售、不注册调度或 HTTP。
func NewAccess(store c.AccessRepository, routes c.AccessRoutes, authorizer c.AccessAuthorizer, cipher c.AccessCipher, platform c.AccessPlatform) (*AccessService, error) {
	if store == nil || routes == nil || authorizer == nil || cipher == nil || platform == nil {
		return nil, c.ErrCapability
	}
	return &AccessService{Store: store, Routes: routes, Authorizer: authorizer, Cipher: cipher, Platform: platform, Now: time.Now}, nil
}

// accessNow 返回 UTC 可信时钟；Now 未设置时使用系统时间。
func (service *AccessService) accessNow() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

// authorizeAccess 校验 actor 三元组及绑定/封禁，绝不解析联系人或创建账号。
func (service *AccessService) authorizeAccess(ctx context.Context, identity c.AccessIdentity, target uint) error {
	if identity.AccountID == 0 || identity.TelegramID <= 0 || identity.BotID == 0 {
		return c.ErrUntrustedIdentity
	}
	identity.Authorization, identity.Route = nil, nil // 外部门禁不接收仓储快照。
	return service.Authorizer.AuthorizeMembership(ctx, identity, target)
}

// ListMemberships 返回分页无秘密列表；账号绑定异常拒绝查询授权，不自动迁移历史身份。
func (service *AccessService) ListMemberships(ctx context.Context, identity c.AccessIdentity, after uint, limit int) ([]c.AccessGroup, error) {
	if err := service.authorizeAccess(ctx, identity, 0); err != nil {
		return nil, err
	}
	return service.Store.ListAccess(ctx, identity, after, limit, service.accessNow())
}

// RequestMembershipInvite 复核身份、当前权益和路由后持久入队；仅 active 入口解密返回。
// 实际创建由受 runtime 执行栅栏保护的 RunAccess 执行，不在 HTTP 事务调用平台。
func (service *AccessService) RequestMembershipInvite(ctx context.Context, identity c.AccessIdentity, target uint) (*c.AccessEntry, error) {
	if err := service.authorizeAccess(ctx, identity, target); err != nil {
		return nil, err
	}
	route, err := service.Routes.MembershipRoute(ctx, target)
	if err != nil {
		return nil, err
	}
	if route.BotID != identity.BotID || route.TargetID != target {
		return nil, c.ErrCapability
	}
	identity.Route = &route
	now := service.accessNow()
	eligible, err := service.Store.Eligibility(ctx, identity, target, now)
	if err != nil {
		return nil, err
	}
	if !eligible.Active {
		return nil, c.ErrUntrustedIdentity
	}
	identity.Authorization = &eligible
	invite, err := service.Store.EnsureInvite(ctx, identity, route, now, time.Time{})
	if err != nil {
		return nil, err
	}
	out := &c.AccessEntry{AccessGroup: c.AccessGroup{TargetID: target, BotID: invite.BotID, TelegramID: identity.TelegramID, Active: eligible.Active, Kind: eligible.Kind, ExpiresAt: eligible.ExpiresAt, InviteID: invite.ID, InviteState: invite.State}}
	if invite.State != "active" {
		return out, nil
	}
	if invite.AccountID != identity.AccountID || invite.TelegramID != identity.TelegramID || !sameAccessRoute(route, invite.BotID, invite.ChatID, invite.RouteVersion) || !d.IsInviteUsableAt(*invite, service.accessNow()) {
		return nil, c.ErrUntrustedIdentity
	}
	// 查询到解密之间再次复核绑定，避免把旧绑定授权当作当前敏感授权。
	if err := service.authorizeAccess(ctx, identity, target); err != nil {
		return nil, err
	}
	latest, err := service.Routes.MembershipRoute(ctx, target)
	if err != nil {
		return nil, err
	}
	if latest != route {
		return nil, c.ErrConflict
	}
	current, err := service.Store.Eligibility(ctx, identity, target, service.accessNow())
	if err != nil {
		return nil, err
	}
	if !current.Active || current.Version != eligible.Version || invite.BindingVersion == 0 || invite.BindingVersion != current.BindingVersion {
		return nil, c.ErrUntrustedIdentity
	}
	out.Link, err = service.Cipher.Open(invite.LinkCipher)
	if err != nil {
		return nil, err
	}
	if !validAccessLink(out.Link) || accessHash(out.Link) != invite.LinkHash {
		return nil, c.ErrInvalid
	}
	if _, err := service.PrepareInviteDelivery(ctx, identity, invite.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// ReceiveMembershipJoin 接受已持久 inbox 的可信输入；未知链接进入可见待处理，禁止批准。
// 所有决策在平台调用前还会由 worker 再次复查，通知/联系人不影响准入。
func (service *AccessService) ReceiveMembershipJoin(ctx context.Context, input c.AccessJoinRequest) error {
	if input.BotID == 0 || input.UpdateID <= 0 || input.TelegramID <= 0 || input.ChatID >= 0 || input.ReceivedAt.IsZero() || len(input.InviteLink) > 2048 {
		return c.ErrInvalid
	}
	// 先提交申请水位，再查链接/授权：故障也不能让前一次申请继续执行。
	if operation, err := service.Store.EnqueueJoin(ctx, input, nil, "needs_review", service.accessNow()); err != nil {
		return err
	} else if operation != nil {
		return nil // 已持久决策的重复 update 不重做接收，worker 仍会复核当前条件。
	}
	invite, err := service.Store.InviteByHash(ctx, input.BotID, input.ChatID, accessHash(input.InviteLink))
	if errors.Is(err, c.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	decision := "approve"
	if err := service.validateJoin(ctx, &input, invite); err != nil {
		// 只有明确的身份/有效期/权益拒绝可固化；路由与基础设施故障交给 inbox 重放。
		if !errors.Is(err, c.ErrUntrustedIdentity) {
			return err
		}
		decision = "decline"
	}
	_, err = service.Store.EnqueueJoin(ctx, input, invite, decision, service.accessNow())
	return err
}

// validateJoin 对 input 的同受益身份、有效期、当前绑定、权益、路由逐一复核。
func (service *AccessService) validateJoin(ctx context.Context, input *c.AccessJoinRequest, invite *d.AccessInvite) error {
	if invite.TelegramID != input.TelegramID || invite.State != "active" || !d.IsInviteUsableAt(*invite, service.accessNow()) {
		return c.ErrUntrustedIdentity
	}
	identity := c.AccessIdentity{AccountID: invite.AccountID, TelegramID: input.TelegramID, BotID: input.BotID}
	if err := service.authorizeAccess(ctx, identity, invite.TargetID); err != nil {
		return err
	}
	route, err := service.Routes.MembershipRoute(ctx, invite.TargetID)
	if err != nil {
		return err
	}
	if !sameAccessRoute(route, invite.BotID, invite.ChatID, invite.RouteVersion) {
		// 成功取得当前路由后确认的旧入口是明确拒绝，不等同路由查询故障。
		return c.ErrUntrustedIdentity
	}
	identity.Route = &route
	identity.Authorization = input.Authorization
	eligible, err := service.Store.Eligibility(ctx, identity, invite.TargetID, service.accessNow())
	if err != nil {
		return err
	}
	if !eligible.Active || invite.BindingVersion == 0 || invite.BindingVersion != eligible.BindingVersion {
		return c.ErrUntrustedIdentity
	}
	input.Authorization = &eligible
	return nil
}

// ObserveMembership 仅消费可信成员更新/查询结果；不假定批准等同加入。
func (service *AccessService) ObserveMembership(ctx context.Context, input c.AccessObservation) (bool, error) {
	route, err := service.Routes.MembershipRoute(ctx, input.TargetID)
	if err != nil {
		return false, err
	}
	if !sameAccessRoute(route, input.BotID, input.ChatID, input.RouteVersion) {
		return false, c.ErrCapability
	}
	if input.ObservedAt.After(service.accessNow().Add(time.Minute)) {
		return false, c.ErrInvalid
	}
	return service.Store.ObserveAccess(ctx, input)
}

// sameAccessRoute 比较当前 route 与持久操作的物理目标，代次可在安全重试前重新绑定。
func sameAccessRoute(route c.AccessRoute, bot uint, chat int64, version uint64) bool {
	return route.BotID == bot && route.ChatID == chat && route.RouteVersion == version && route.Generation != 0
}

// accessHash 为高熵邀请生成固定指纹，不在日志保存明文。
func accessHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// validAccessLink 严格接受 Telegram 私密邀请入口，不允许提示页、用户信息或查询参数。
func validAccessLink(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "t.me" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	suffix := strings.TrimPrefix(parsed.Path, "/+")
	if suffix == parsed.Path {
		suffix = strings.TrimPrefix(parsed.Path, "/joinchat/")
		if suffix == parsed.Path {
			return false
		}
	}
	if len(suffix) < 8 || len(suffix) > 128 {
		return false
	}
	for _, character := range suffix {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

// checkAccessFence 复用 runtime 续租/代次合同，非内置运行时始终失败关闭。
func checkAccessFence(ctx context.Context, route c.AccessRoute) error {
	return r.CheckExecutionFence(ctx, route.BotID, route.Generation)
}

var _ c.AccessReader = (*AccessService)(nil)
