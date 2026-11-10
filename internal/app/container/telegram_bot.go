package container

import (
	"context"
	"errors"
	"fmt"
	telegrambootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	botbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegrambotrecovery"
	"github.com/XManCodeing/XMall/internal/bootstrap/telegrambroadcast"
	groupbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegramgrouprecovery"
	"github.com/XManCodeing/XMall/internal/bootstrap/telegrammembership"
	membershipaccess "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess"
	"github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershipaccess/proactive"
	membershipbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershiprecovery"
	supportbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegramsupportrecovery"
	auditlogapp "github.com/XManCodeing/XMall/internal/modules/auditlog/application"
	fulfillmentcontract "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fulfillmentstore "github.com/XManCodeing/XMall/internal/modules/fulfillment/infrastructure/gormstore"
	broadcastapp "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	eventstore "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/infrastructure/gormstore"
	groupsapp "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	groupsapi "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/botapi"
	groupsstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	membershipapp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	membershipcontract "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	membershipapi "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/botapi"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	membershiprecovery "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	recoveryapp "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	botreplacement "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	recoverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	recoverystore "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	recoveryhttp "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/transport/http"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimeapi "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	supportapp "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	supportcontract "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	supportstore "github.com/XManCodeing/XMall/internal/modules/telegram/support/infrastructure/gormstore"
	uploadlocal "github.com/XManCodeing/XMall/internal/modules/upload/infrastructure/localstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"gorm.io/gorm"
	"log/slog"
	"time"
)

// AuthorizeSupportTopicDeletion checks the real backend actor and exact POST
// grant. Wildcard/read/content/downstream permissions do not authorize this fact.
func (container *Container) AuthorizeSupportTopicDeletion(ctx context.Context, actor, bot uint) error {
	if bot == 0 {
		return supportcontract.ErrDenied
	}
	return container.authorizeSupportPermission(ctx, actor, supportcontract.AdminTopicDeletionPath, "POST", true)
}

// authorizeSupportAdmin resolves an actual backend admin and rechecks permission.
// Sensitive content requires the exact GET route template, never a wildcard grant.
func (c *Container) authorizeSupportAdmin(ctx context.Context, actor uint, path string, exact bool) error {
	return c.authorizeSupportPermission(ctx, actor, path, "GET", exact)
}

// authorizeSupportPermission checks a real backend administrator and exact method/path grants.
func (c *Container) authorizeSupportPermission(ctx context.Context, actor uint, path, method string, exact bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if actor == 0 || c.AdminStore == nil || c.AuthzService == nil {
		return supportcontract.ErrDenied
	}
	admin, err := c.AdminStore.GetByID(actor)
	if err != nil || admin == nil || admin.ID != actor || admin.DeletedAt != nil {
		return supportcontract.ErrDenied
	}
	if admin.IsSuper {
		return nil
	}
	if exact {
		policies, err := c.AuthzService.GetAdminPolicies(actor)
		if err != nil {
			return err
		}
		for _, policy := range policies {
			if policy.Object == path && policy.Action == method {
				return nil
			}
		}
		return supportcontract.ErrDenied
	}
	allowed, err := c.AuthzService.EnforceAdmin(actor, path, method)
	if err != nil {
		return err
	}
	if !allowed {
		return supportcontract.ErrDenied
	}
	return nil
}

// authorizeSupportSettings treats bot policy as system configuration, independent of support content.
// It authorizes the exact method then checks bot existence without requiring a running bot.
func (c *Container) authorizeSupportSettings(ctx context.Context, actor, bot uint, method string) error {
	if bot == 0 || (method != "GET" && method != "PUT") {
		return supportcontract.ErrDenied
	}
	if err := c.authorizeSupportPermission(ctx, actor, supportcontract.AdminSettingsPath, method, true); err != nil {
		return err
	}
	if c.TelegramBotStore == nil {
		return supportcontract.ErrAdminUnavailable
	}
	instance, err := c.TelegramBotStore.Get(ctx, bot)
	if errors.Is(err, runtimecontract.ErrNotFound) {
		return supportcontract.ErrAdminNotFound
	}
	if err != nil {
		return err
	}
	if instance == nil || instance.ID != bot {
		return supportcontract.ErrAdminNotFound
	}
	if instance.RecoveryTaskID != 0 {
		return runtimecontract.ErrRecoveryBlocked
	}
	return nil
}

// AuthorizeSupportResolution independently checks exact POST permission for a backend actor/bot.
// Telegram membership and GET/content grants confer no adjudication authority.
func (c *Container) AuthorizeSupportResolution(ctx context.Context, actor, bot uint) error {
	if bot == 0 {
		return supportcontract.ErrDenied
	}
	return c.authorizeSupportPermission(ctx, actor, supportcontract.AdminResolutionPath, "POST", true)
}

// supportAdminGeneration reads trusted runtime state; the store repeats its fence in the CAS transaction.
func (c *Container) supportAdminGeneration(ctx context.Context, bot uint) (uint64, error) {
	if c.TelegramBotStore == nil {
		return 0, supportcontract.ErrAdminUnavailable
	}
	instance, err := c.TelegramBotStore.Get(ctx, bot)
	if errors.Is(err, runtimecontract.ErrNotFound) {
		return 0, supportcontract.ErrAdminNotFound
	}
	if err != nil {
		return 0, err
	}
	if instance == nil || instance.ID != bot || !instance.Enabled || instance.Mode != "internal" || instance.State != "running" || instance.Generation == 0 {
		return 0, supportcontract.ErrConflict
	}
	return instance.Generation, nil
}

// auditSupportContent reuses the existing durable authz audit log; no new schema is needed.
// Only backend/local IDs and service-generated outcomes are accepted, never message content.
func (c *Container) auditSupportContent(ctx context.Context, actor, session, message uint, outcome string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.AuthzAuditService == nil || c.AuthzAuditLogRepo == nil {
		return supportcontract.ErrAdminUnavailable
	}
	return c.AuthzAuditService.Record(auditlogapp.AuthzRecord{
		OperatorAdminID: actor, Action: "support.content." + outcome,
		Object: fmt.Sprintf("/admin/telegram-bot/support/sessions/%d/messages/%d/content", session, message), Method: "GET",
	})
}

// initMembershipRecovery 组装无网络的付款恢复与幂等账本；不依赖 Bot/Redis 是否启用。
// 游标只优化周期扫描，持久事件唯一键及租约负责崩溃恢复；重启从零扫描不会重复授予。
func (c *Container) initMembershipRecovery() {
	store := membershipstore.New(gormdb.DB).WithSystemFulfillment(func(tx *gorm.DB) fulfillmentcontract.MembershipSystemStore {
		return fulfillmentstore.New(tx)
	})
	worker, constructionErr := membershipapp.NewPaidWorker(store, store, time.Now, 30*time.Second)
	recoverRefunds := c.membershipRefundRecovery(gormdb.DB)
	var after uint
	var nextHistory time.Time
	c.MembershipRecovery = telegrammembership.NewRecoveryService(func(ctx context.Context) error {
		if constructionErr != nil {
			return constructionErr
		}
		var consumeErr error
		if _, consumeErr = worker.RunOnce(ctx, 100); ctx.Err() != nil {
			return errors.Join(consumeErr, ctx.Err())
		}
		var recoveryErr error
		if !time.Now().Before(nextHistory) {
			next, scanned, err := worker.RecoverOnce(ctx, after, 100)
			recoveryErr = err
			if scanned > 0 {
				after = next
			} else if err == nil {
				after = 0
			}
			nextHistory = time.Now().Add(time.Minute)
		}
		return errors.Join(recoveryErr, consumeErr, recoverRefunds(ctx))
	}, 2*time.Second)
}

// initMembershipAccess constructs B6a without making network calls or enabling sales.
// All execution remains inside the leased runtime; HTTP/Bot readers share account and entitlement checks.
func (c *Container) initMembershipAccess(runtimeStore runtimecontract.Store) (*membershipaccess.Runtime, *membershipaccess.Website) {
	store := membershipstore.New(gormdb.DB)
	identity := membershipaccess.Identity{Bindings: c.ExternalIdentityStore, Users: c.UserStore}
	routes := membershipaccess.Routes{Lookup: c.TelegramGroups}
	platform := &membershipapi.Access{Store: runtimeStore, Cipher: c.TelegramBotManager, API: runtimeapi.Adapter{}, Query: runtimeapi.Adapter{}, Lifecycle: runtimeapi.Adapter{}, Routes: routes}
	recoveryAccess := c.initMembershipGroupRecovery(platform, identity)
	// Dependencies are concrete and non-nil here; retain fail-closed ports if construction ever fails.
	service, err := membershipapp.NewAccess(store, routes, identity, c.TelegramBotManager, platform)
	website := &membershipaccess.Website{Identity: identity, Reader: store, Invites: store, Groups: groupsstore.New(gormdb.DB), Recovery: recoveryAccess}
	bridge := &membershipaccess.Runtime{Groups: groupsstore.New(gormdb.DB), Routes: routes, Bots: runtimeStore, Recovery: recoveryAccess}
	lifecycle, lifecycleErr := membershipapp.NewLifecycle(store, routes, platform)
	if lifecycleErr != nil {
		slog.Error("membership lifecycle assembly failed")
		return bridge, website
	}
	bridge.Lifecycle = lifecycle
	bridge.Migrations = &membershipaccess.IdentityMigrations{Store: store, Routes: routes, Platform: platform}
	bridge.Reminders = &membershipaccess.Reminders{Store: store, Queue: runtimeStore, Routes: routes, Cipher: c.TelegramBotManager}
	bridge.JoinAbuse = &membershipaccess.JoinAbuseNotifications{Store: store, Queue: runtimeStore, Routes: routes, Cipher: c.TelegramBotManager}
	c.MembershipLifecycle = lifecycle
	c.MembershipRefundAdmin = store
	if err != nil {
		slog.Error("membership access assembly failed")
		return bridge, website
	}
	service.Deliveries, _ = runtimeStore.(runtimecontract.InviteDeliveryReader)
	c.MembershipAdminAccess = service
	website.Access = service
	bridge.Service = service
	bridge.Proactive = &proactive.Service{Store: store, Access: service, Queue: runtimeStore, Routes: routes, Cipher: c.TelegramBotManager}
	return bridge, website
}

// initMembershipGroupRecovery installs the task-authorized executor using platform
// and identity, returning the shared website/inbox bridge or nil on assembly failure.
// Construction performs no external I/O and never substitutes an inventory scanner.
func (c *Container) initMembershipGroupRecovery(platform *membershipapi.Access, identity membershipcontract.AccessAuthorizer) membershipaccess.RecoveryAccess {
	if c.TelegramGroupRecovery == nil || c.TelegramRecovery == nil {
		slog.Error("telegram membership group recovery parent unavailable")
		return nil
	}
	store, err := membershiprecovery.New(gormdb.DB)
	if err != nil {
		slog.Error("telegram membership group recovery store unavailable", "error", err)
		return nil
	}
	recoverer, err := membershipbridge.NewGroup(store, platform, c.TelegramRecovery.Store, c.authorizeGroupRecovery, identity)
	if err != nil {
		slog.Error("telegram membership group recovery assembly failed", "error", err)
		return nil
	}
	botRecoverer := c.initBotRecoveryModules(platform, identity)
	c.TelegramGroupRecovery.Membership = recoverer
	return &membershipaccess.Recovery{Groups: groupsstore.New(gormdb.DB), Parents: c.TelegramRecovery.Store, Worker: recoverer, BotWorker: botRecoverer}
}

// botRecoveryModules combines independent business proofs without declaring completion.
// The parent validates all receipts and counters before releasing either runtime fence.
type botRecoveryModules struct {
	membership botreplacement.Modules
	support    botreplacement.Modules
}

// ContinueBot passes the same immutable request to both modules in sequence.
// It returns no usable proofs on error; missing dependencies fail closed.
func (modules botRecoveryModules) ContinueBot(ctx context.Context, request botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error) {
	if modules.membership == nil || modules.support == nil {
		return nil, botreplacement.ErrContinuationRequired
	}
	var proofs []botreplacement.ModuleProof
	for _, module := range []botreplacement.Modules{modules.membership, modules.support} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := module.ContinueBot(ctx, request)
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, result...)
	}
	return proofs, nil
}

// initBotRecoveryModules installs task-bound modules and returns their shared membership worker.
// platform and identity supply existing dependencies; nil means fail-closed assembly failure.
// Construction does not contact Telegram, start runtime workers, or enable sales.
func (c *Container) initBotRecoveryModules(platform *membershipapi.Access, identity membershipcontract.AccessAuthorizer) *membershiprecovery.BotRecovery {
	if c.TelegramRecovery == nil || c.TelegramRecovery.Bots == nil {
		slog.Error("telegram bot recovery parent assembly unavailable")
		return nil
	}
	store, err := membershiprecovery.New(gormdb.DB)
	if err != nil {
		slog.Error("telegram bot recovery store assembly failed", "error", err)
		return nil
	}
	recoverer, err := membershipbridge.NewBotAdmission(store, platform, c.TelegramRecovery.Store, c.authorizeGroupRecovery, identity)
	if err != nil {
		slog.Error("telegram bot recovery membership assembly failed", "error", err)
		return nil
	}
	c.TelegramRecovery.Bots.Modules = botRecoveryModules{
		membership: &membershipbridge.Modules{Store: store, Recovery: recoverer},
		support:    supportbridge.New(gormdb.DB, c.TelegramBotManager, runtimeapi.Adapter{}, c.authorizeGroupRecovery),
	}
	return recoverer
}

// initSupportGroupRecovery wires the stopped-runtime support path only after the
// supervisor exists. The closure reads current fences on every authenticated resume.
func (c *Container) initSupportGroupRecovery() {
	if c.TelegramGroupRecovery == nil || c.TelegramBotRuntime == nil {
		return
	}
	adapter := supportbridge.New(gormdb.DB, c.TelegramBotManager, runtimeapi.Adapter{}, c.authorizeGroupRecovery)
	probe := &groupsapi.Adapter{Store: c.TelegramBotStore, Cipher: c.TelegramBotManager}
	c.TelegramGroupRecovery.ContinueSupport = func(ctx context.Context, request recoverycontract.GroupRecoveryRequest) error {
		if err := c.authorizeGroupRecovery(ctx, request.ActorID, "resume"); err != nil {
			return err
		}
		instance, err := c.TelegramBotStore.Get(ctx, request.BotID)
		if err != nil {
			return err
		}
		if instance.RecoveryTaskID == 0 {
			// First pass uses the ordinary read-only probe before stop-and-join.
			if _, err := c.TelegramGroups.Check(ctx, request.TargetID, request.NewRouteVersion, request.ActorID, "recovery"); err != nil {
				return err
			}
		} else {
			if err := c.TelegramBotRuntime.BlockAndDrainRecovery(ctx, runtimecontract.RecoveryFence{TaskID: request.TaskID, BotID: request.BotID, Version: instance.Version, Generation: request.Generation}); err != nil {
				return err
			}
			if err := adapter.RefreshGroup(ctx, request.ActorID, request, probe); err != nil {
				return err
			}
		}
		result, err := adapter.ContinueGroup(ctx, request.ActorID, request, c.TelegramBotRuntime)
		if err != nil {
			return err
		}
		if result.Unknown != 0 {
			return recoveryapp.ErrRecoveryReview
		}
		if !result.Verified || result.Remaining != 0 {
			return recoveryapp.ErrRecoveryPending
		}
		return nil
	}
}

// initGroupRecovery shares group service, route store and runtime identity with ordinary checks.
// store is the existing runtime store; construction never invokes Telegram or enables sales.
func (container *Container) initGroupRecovery(store runtimecontract.Store) {
	routes := groupsstore.New(gormdb.DB).WithBotRecoveryGuard(func(tx *gorm.DB, bot uint) error {
		return runtimestore.New(tx).GuardRecoveryWrite(context.Background(), bot)
	})
	identity := &groupsapi.Adapter{Store: store, Cipher: container.TelegramBotManager}
	container.TelegramGroups = groupsapp.New(routes, identity)
	container.TelegramRecovery = &recoveryapp.Service{Store: recoverystore.New(gormdb.DB), Groups: container.TelegramGroups}
	container.TelegramSupportRecovery = supportbridge.New(gormdb.DB, container.TelegramBotManager, runtimeapi.Adapter{}, container.authorizeGroupRecovery)
	container.TelegramRecovery.Bots = &botreplacement.Service{Store: container.TelegramRecovery.Store, Backend: botbridge.New(gormdb.DB), Continuation: botbridge.New(gormdb.DB), Probe: identity, Authorize: container.authorizeGroupRecovery}
	// Membership stays unavailable until the executable adapter is constructed.
	// A read-only inventory scanner must not advertise production recovery.
	container.TelegramGroupRecovery = &groupbridge.Executor{Store: container.TelegramRecovery.Store, Groups: container.TelegramGroups,
		Routes: routes, Identity: identity, Authorize: container.authorizeGroupRecovery}
}

// TelegramRecoveryHandler returns the production HTTP handler with supported execution ports.
// It leaves mutation routes absent when group execution was not assembled.
func (container *Container) TelegramRecoveryHandler() *recoveryhttp.Handler {
	handler := recoveryhttp.New(container.TelegramRecovery)
	handler.WithSupportManagement(container.TelegramSupportRecovery)
	if executor := container.TelegramGroupRecovery; executor != nil {
		handler.WithExecution(recoveryhttp.Execution{Confirm: executor.Confirm, Resume: executor.Resume, Available: executor.Available})
	}
	return handler
}

// authorizeGroupRecovery requires the exact POST action grant on every confirm/resume attempt.
// action is an internal enum; arbitrary paths and missing administrators fail closed.
func (container *Container) authorizeGroupRecovery(ctx context.Context, actor uint, action string) error {
	if action == "bot-preflight" {
		return container.authorizeSupportPermission(ctx, actor, "/admin/telegram-bot/recovery/bot-preflight", "POST", true)
	}
	if action != "confirm" && action != "resume" {
		return recoverycontract.ErrInvalid
	}
	return container.authorizeSupportPermission(ctx, actor, "/admin/telegram-bot/recovery/tasks/:id/"+action, "POST", true)
}

// initTelegramBot constructs dependencies only; polling belongs to BuildRunner.
func (c *Container) initTelegramBot() {
	c.initMembershipRecovery()
	store := runtimestore.New(gormdb.DB)
	c.TelegramBotStore = store
	c.TelegramBotManager = telegrambootstrap.NewManagerWithMedia(store, c.Config.App.SecretKey, uploadlocal.DefaultRoot)
	shopDependencies := telegrambootstrap.ShopDependencies{
		ContentSettings: c.SettingService,
		Membership:      membershipstore.New(gormdb.DB),
		ProductLinks:    telegrambootstrap.MembershipProductLinks{Codes: membershipstore.New(gormdb.DB), Configs: membershipstore.New(gormdb.DB), Targets: groupsstore.New(gormdb.DB)},
		Store:           store, Secret: c.Config.App.SecretKey, Auth: c.UserAuthService, Users: c.UserStore,
		Categories: c.CategoryService, Products: c.ProductReadService, ProductRepo: c.ProductRepo,
		ProductMappings: c.ProductMappingRepo, SKUStocks: c.ProductMappingService,
		Orders: c.OrderService, Payments: c.PaymentService, Wallet: c.WalletService, Currency: c.SettingService.GetSiteCurrency,
	}
	shop := telegrambootstrap.NewShop(shopDependencies)
	c.initGroupRecovery(store)
	c.initIdentityRecovery()
	accessRuntime, accessWebsite := c.initMembershipAccess(store)
	accessRuntime.Panels = shop
	c.MembershipUserAccess = accessWebsite
	deliveryGuard, _ := accessRuntime.Service.(runtimecontract.InviteDeliveryAuthorizer)
	shop.SetMembershipAccess(&membershipaccess.Bot{Website: accessWebsite, Deliveries: store, DeliveryGuard: deliveryGuard})
	capabilityPreflight := telegrammembership.New(membershipstore.New(gormdb.DB), membershipstore.New(gormdb.DB), c.TelegramGroups, c.ProductRepo.CheckPublicSale)
	c.OrderService.SetMembershipPreflight(capabilityPreflight)
	c.PaymentService.SetMembershipPreflight(capabilityPreflight)
	c.TelegramSupportAdmin = supportapp.NewAdmin(supportstore.NewAdmin(gormdb.DB), c.TelegramBotManager, c.authorizeSupportAdmin, c.auditSupportContent)
	// Ingress and workers share the transaction-bound runtime/recovery fence.
	supportStore := supportstore.New(gormdb.DB, c.TelegramBotManager).WithReadyEncoder(supportcontract.EncodeReadyTransfer)
	supportStore.WithPolicyRecoveryGuard(func(tx *gorm.DB, bot uint) error {
		return runtimestore.New(tx).GuardRecoveryWrite(tx.Statement.Context, bot)
	})
	supportStore.WithDeliveryRecovery(func(tx *gorm.DB) runtimecontract.DeliveryRecoveryStore { return runtimestore.New(tx) })
	c.TelegramSupportAdmin.WithDeliveryRecovery(supportStore, func(ctx context.Context, actor uint, path string) error {
		return c.authorizeSupportPermission(ctx, actor, path, "POST", true)
	})
	c.TelegramSupportAdmin.WithDownstreams(supportstore.NewAdmin(gormdb.DB), &supportapp.DownstreamResolver{Store: supportStore, Authorizer: c}, c.supportAdminGeneration)
	c.TelegramSupportAdmin.WithRecovery(supportstore.NewAdmin(gormdb.DB), &supportapp.Resolver{Topics: supportstore.AdminTopicResolver{Store: supportStore}, Dispatches: supportStore, Authorizer: c}, c.supportAdminGeneration)
	c.TelegramSupportAdmin.WithSettings(supportStore, c.authorizeSupportSettings)
	support := supportapp.New(supportStore, c.TelegramBotManager, c.TelegramGroups, store).WithPolicies(supportStore)
	supportRuntime := telegrambootstrap.NewSupportRuntime(supportStore, store, store, c.TelegramBotManager, c.TelegramGroups)
	updates := telegrambootstrap.NewUpdateDispatcher(shop, shop, telegrambootstrap.UpdateModules{
		Profiles: telegrambootstrap.EventProfileSync{Auth: c.UserAuthService},
		Support:  support, SupportCallbacks: support, GroupMessages: supportRuntime.Replies,
		SupportOperations: supportRuntime.Operations,
		Targets: map[runtimecontract.TargetType]runtimecontract.TargetAuthorizer{
			runtimecontract.TargetSupportPrivate: telegrambootstrap.SupportPrivateAuthorizer{Notices: support, Replies: supportRuntime.Replies},
			runtimecontract.TargetSupportGroup:   telegrambootstrap.SupportGroupAuthorizer{Ready: supportStore, Controls: supportRuntime.Operations},
		},
		Events: map[string]telegrambootstrap.UpdateReceiver{"my_chat_member": c.TelegramGroups, "topic_service": supportRuntime,
			"chat_join_request": accessRuntime, "chat_member": accessRuntime},
		Subscriptions: []telegrambootstrap.UpdateSubscriptions{c.TelegramGroups, accessRuntime},
	})
	broadcastBridge := &telegrambroadcast.RuntimeBridge{
		Store: store, Cipher: c.TelegramBotManager, UploadRoot: uploadlocal.DefaultRoot,
		Users:    telegrambroadcast.NewUserDirectory(c.ExternalIdentityStore),
		Identity: telegrambroadcast.ShopIdentity{Shop: shop},
	}
	c.TelegramBroadcastService = broadcastapp.NewRuntimeService(c.TelegramBroadcastRepo, broadcastBridge, broadcastBridge, telegrambroadcast.NewDispatcher(c.QueueClient))
	dispatcher := telegrambootstrap.NewBusinessDispatcher(eventstore.NewBusinessStore(gormdb.DB), shopDependencies, shop, store)
	if c.Config.Queue.Enabled {
		c.TelegramBotLeaseClient = telegrambootstrap.NewLeaseClient(c.Config.Queue)
		c.TelegramBotRuntime = runtimeapp.NewRuntime(c.TelegramBotManager, runtimeapi.Leaser{Client: c.TelegramBotLeaseClient}, updates)
		// Advertise readiness only when the supervised delivery runtime is wired.
		support.DeliveryReady = supportstore.DeliveryCapability{Store: supportStore}
		c.TelegramSupportAdmin.WithReadiness(support.ReadyEnabled)
		c.TelegramRecovery.Bots.Drain = c.TelegramBotRuntime
		c.initSupportGroupRecovery()
		c.TelegramBotRuntime.Recover = func(ctx context.Context) error {
			return withRecoveryLease(ctx, runtimeapi.Leaser{Client: c.TelegramBotLeaseClient}, func(ctx context.Context) error {
				_, err := c.PaymentService.RecoverPaidFulfillment(ctx, 100)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.Join(err, c.TelegramBroadcastService.Reconcile(ctx))
			}, 8*time.Second, 3*time.Second, 25*time.Second)
		}
		c.TelegramBotRuntime.ReconcileLocal = accessRuntime.ReconcileProactive
		c.TelegramBotRuntime.Reconcile = func(ctx context.Context, botID uint) error {
			if err := store.Cleanup(ctx, botID); err != nil {
				return err
			}
			return errors.Join(dispatcher.Reconcile(ctx, botID), c.TelegramGroups.Reconcile(ctx, botID), support.Reconcile(ctx, botID), supportRuntime.Reconcile(ctx, botID), accessRuntime.Reconcile(ctx, botID))
		}
	}
}
