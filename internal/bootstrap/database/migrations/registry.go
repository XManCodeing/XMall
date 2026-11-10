package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/constants"
	affiliatedomain "github.com/XManCodeing/XMall/internal/modules/affiliate/domain"
	apicredentialdomain "github.com/XManCodeing/XMall/internal/modules/apicredential/domain"
	auditlogdomain "github.com/XManCodeing/XMall/internal/modules/auditlog/domain"
	cardsecretdomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	cartdomain "github.com/XManCodeing/XMall/internal/modules/cart/domain"
	categorydomain "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	channelclientdomain "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	contentdomain "github.com/XManCodeing/XMall/internal/modules/content/domain"
	coupondomain "github.com/XManCodeing/XMall/internal/modules/coupon/domain"
	downstreamcallbackdomain "github.com/XManCodeing/XMall/internal/modules/downstreamcallback/domain"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	giftcarddomain "github.com/XManCodeing/XMall/internal/modules/giftcard/domain"
	admindomain "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	emailverificationdomain "github.com/XManCodeing/XMall/internal/modules/identity/emailverification/domain"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	invitecodedomain "github.com/XManCodeing/XMall/internal/modules/identity/invitecode/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	memberleveldomain "github.com/XManCodeing/XMall/internal/modules/memberlevel/domain"
	notificationdomain "github.com/XManCodeing/XMall/internal/modules/notification/domain"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderriskcontract "github.com/XManCodeing/XMall/internal/modules/orderrisk/contract"
	orderriskdomain "github.com/XManCodeing/XMall/internal/modules/orderrisk/domain"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	promotiondomain "github.com/XManCodeing/XMall/internal/modules/promotion/domain"
	reconciliationdomain "github.com/XManCodeing/XMall/internal/modules/reconciliation/domain"
	resellerstore "github.com/XManCodeing/XMall/internal/modules/reseller/infrastructure/gormstore"
	settingsstore "github.com/XManCodeing/XMall/internal/modules/settings/infrastructure/gormstore"
	siteconnectiondomain "github.com/XManCodeing/XMall/internal/modules/siteconnection/domain"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	deliverycontract "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	groupsstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershiprouterecovery "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	recoverydomain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	supportdomain "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
)

// migrateTelegramMembershipInviteLifecycle 追加长期期限和独立清理账本，保留所有历史证据。
// db 是迁移事务连接；历史邀请默认 limited，不延长截止、不推断回收成功，支持幂等重跑。
func migrateTelegramMembershipInviteLifecycle(db *gorm.DB) error {
	if err := migrateModelsWithProgress(db, &membershipdomain.AccessInvite{}, &membershipdomain.AccessOperation{}, &membershipdomain.InviteCleanupCycle{}, &membershipdomain.InviteCleanupItem{}); err != nil {
		return err
	}
	return migrateMembershipRefundRequests(db)
}

// migrateMembershipRefundRequests adds the durable money/termination command without touching legacy refunds.
// db is the migration connection; returns additive DDL errors without replaying refunds.
func migrateMembershipRefundRequests(db *gorm.DB) error {
	return db.AutoMigrate(&orderdomain.MembershipRefundRequest{})
}

// schemaVersion tracks schema/data contracts, including explicitly recorded store safety upgrades,
// not runner implementation changes. For vN: update the current-model baseline when
// needed AND append the targeted vN step; behavior-only steps must preserve all data.
const schemaVersion int64 = 48
const migrationLockName = "dujiao_next_schema_migration"
const migrationAdvisoryKey int64 = 0x64756a69616f

// migrationStep upgrades version-1 to version. Steps must be ordered, contiguous,
// and retry-safe: MySQL DDL cannot be rolled back if a step or ledger write fails.
type migrationStep struct {
	version int64
	name    string
	run     func(*gorm.DB) error
}

// The baseline is the CURRENT model/data snapshot, not a frozen v1 snapshot.
// An unversioned DB runs it once and records schemaVersion, skipping historical
// steps which could conflict with current models. A versioned DB ONLY runs steps.
var schemaSteps = []migrationStep{
	{version: 2, name: "telegram runtime", run: migrateTelegramRuntime},
	{version: 3, name: "telegram identity and business delivery", run: migrateTelegramBusinessDelivery},
	{version: 4, name: "telegram explicit cutover ownership", run: migrateTelegramRuntime},
	{version: 5, name: "persistent telegram channel ownership", run: retireTelegramOwnership},
	{version: 6, name: "retire telegram channel ownership", run: retireTelegramOwnership},
	{version: 7, name: "telegram panel lifecycle", run: migrateTelegramRuntime},
	{version: 8, name: "telegram product photo cache", run: migrateTelegramMediaCache},
	{version: 9, name: "telegram bot profile fields", run: migrateTelegramRuntime},
	{version: 10, name: "telegram target and payload retention", run: migrateTelegramPayloadRetention},
	{version: 11, name: "telegram group routes and capability checks", run: migrateTelegramGroups},
	{version: 12, name: "telegram support ingress", run: migrateTelegramSupport},
	{version: 13, name: "telegram support topic delivery", run: migrateTelegramSupportDelivery},
	{version: 14, name: "telegram membership product and order snapshots", run: migrateTelegramMembershipCheckout},
	{version: 15, name: "telegram support topic retry and replacement slots", run: migrateTelegramTopicRecovery},
	{version: 16, name: "telegram membership entitlement ledger and reliable fulfillment", run: migrateTelegramMembershipLedger},
	{version: 17, name: "telegram membership invitations approvals and known members", run: migrateTelegramMembershipAccess},
	{version: 18, name: "telegram membership expiry lifecycle and scan checkpoints", run: migrateTelegramMembershipLifecycle},
	{version: 19, name: "telegram recovery tasks and route fences", run: migrateTelegramRecovery},
	{version: 20, name: "telegram beneficiary mappings and support disaster checkpoints", run: migrateTelegramRecoveryMappings},
	{version: 21, name: "telegram runtime recovery fence and generation exit evidence", run: migrateTelegramRuntimeRecovery},
	{version: 22, name: "telegram per-target recovery and support restoration", run: migrateTelegramRecoveryContinuation},
	{version: 23, name: "telegram support downstream restoration liabilities", run: migrateTelegramSupportDownstreamLiabilities},
	{version: 24, name: "payment manual confirmation audit and receipt evidence", run: migratePaymentManualConfirmation},
	{version: 25, name: "order timeout cancellation evidence", run: migrateOrderCancellationEvidence},
	{version: 26, name: "telegram guest panel cleanup and support blocking safety", run: migrateTelegramGuestSafety},
	{version: 27, name: "telegram long-term invitations and independent cleanup", run: migrateTelegramMembershipInviteLifecycle},
	{version: 28, name: "membership refund recovery requests", run: migrateMembershipRefundRequests},
	{version: 29, name: "membership proactive delivery responsibilities", run: migrateMembershipProactiveDelivery},
	{version: 30, name: "membership proactive scan checkpoints", run: migrateMembershipProactiveCursor},
	{version: 31, name: "membership order evidence and invitation abuse notification safety", run: migrateMembershipOperationsSafety},
	{version: 32, name: "membership and recovery authoritative numbered reads", run: migrateMembershipNumberedReads},
	{version: 33, name: "fixed east-eight business calendar reads", run: migrateBusinessCalendarReads},
	{version: 34, name: "membership lifecycle recovery renewal checks and filtered reads", run: migrateMembershipLifecycleRecovery},
	{version: 35, name: "membership order-scoped status and access task order reads", run: migrateMembershipOrderScopedReads},
	{version: 36, name: "support ingress readiness and recovery checks", run: migrateSupportIngressChecks},
	{version: 37, name: "support direct topic reply routing", run: migrateSupportTopicReplies},
	{version: 38, name: "support daily captcha and inquiry controls", run: migrateSupportDailyCaptcha},
	{version: 39, name: "support verified captcha cleanup and missing topic recovery", run: migrateSupportDeliveryRecovery},
	{version: 40, name: "membership product share projection", run: migrateMembershipProductShare},
	{version: 41, name: "bot-bound encrypted membership product shares", run: migrateEncryptedMembershipProductShare},
	{version: 42, name: "persistent membership product short codes", run: migrateMembershipShareCodes},
	{version: 43, name: "membership admin target name projection", run: migrateMembershipTargetNames},
	{version: 44, name: "membership refund execution bindings", run: migrateMembershipRefundExecutions},
	{version: 45, name: "membership and support store safety corrections", run: migrateStoreSafetyCorrections},
	{version: 46, name: "audited payment runtime and dialect safety contracts", run: migrateAuditSafetyContracts},
	{version: 47, name: "membership refund retry leases and backoff", run: migrateMembershipRefundRequests},
	{version: 48, name: "membership proactive idle scan safety", run: migrateAuditSafetyContracts},
}

// migrateAuditSafetyContracts records audited store behavior without rewriting history.
// The connection is unused: existing columns hold recovery markers and no DDL is needed.
// Returning nil lets the runner record the retry-safe version marker alone.
func migrateAuditSafetyContracts(_ *gorm.DB) error { return nil }

// migrateStoreSafetyCorrections records membership evidence/lifecycle and support
// routing/retention fixes. The connection is intentionally unused: existing
// columns cover these behavior changes, and no DDL or historical rewrites run.
// Returns nil so the runner alone records the contiguous, retry-safe v45 marker.
func migrateStoreSafetyCorrections(_ *gorm.DB) error { return nil }

// migrateMembershipRefundExecutions adds nullable bindings without manufacturing historical links.
// db is the migration transaction; legacy audit rows retain execution_version zero.
func migrateMembershipRefundExecutions(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.RefundDisposition{})
}

// migrateMembershipTargetNames records a read-only projection change; no schema/data rewrites.
func migrateMembershipTargetNames(_ *gorm.DB) error { return nil }

// migrateMembershipShareCodes creates the unique mapping without rewriting historical products.
func migrateMembershipShareCodes(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.ProductShareCode{})
}

// migrateEncryptedMembershipProductShare records encoder injection into the read projection.
// The transaction is unused; this behavior-only step preserves all schema and historical data.
func migrateEncryptedMembershipProductShare(_ *gorm.DB) error { return nil }

// migrateMembershipProductShare records the read-only share projection contract.
// The transaction is unused: no columns, data rewrites or Telegram calls are needed.
func migrateMembershipProductShare(_ *gorm.DB) error { return nil }

// migrateSupportDeliveryRecovery records callback cleanup proof and topic recovery behavior.
// The transaction is unused; no DDL or historical message replay is performed.
func migrateSupportDeliveryRecovery(_ *gorm.DB) error { return nil }

// migrateSupportDailyCaptcha adds verification and identity-control metadata.
// db is the migration transaction; existing consultation payloads and receipts stay intact.
func migrateSupportDailyCaptcha(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &supportdomain.Session{}, &supportdomain.Challenge{}, &supportdomain.BufferedMessage{}, &supportdomain.OperationControl{}, &supportdomain.OperationReference{})
}

// migrateSupportTopicReplies records topic-based reply authorization changes.
// The transaction is unused: existing mappings and consultation history stay intact.
func migrateSupportTopicReplies(_ *gorm.DB) error { return nil }

// migrateSupportIngressChecks records the support store's readiness and recovery checks.
// The transaction is unused; this behavior-only version changes no tables or historical data.
func migrateSupportIngressChecks(_ *gorm.DB) error { return nil }

// migrateMembershipOrderScopedReads records order-scoped status/filter and task provenance read contracts.
// The transaction connection is unused; nil records this behavior-only upgrade without DDL or history rewrites.
func migrateMembershipOrderScopedReads(_ *gorm.DB) error { return nil }

// migrateMembershipLifecycleRecovery records safer recovery and filtered read contracts without DDL.
// The transaction connection is unused; nil records this behavior-only upgrade without rewriting history.
func migrateMembershipLifecycleRecovery(_ *gorm.DB) error { return nil }

// migrateBusinessCalendarReads records the UTC+08 calendar read contract without rewriting timestamps.
// The transaction connection is unused; returns nil to persist this behavior-only upgrade atomically.
func migrateBusinessCalendarReads(_ *gorm.DB) error { return nil }

// migrateMembershipNumberedReads records the count/offset read contract without changing schema or data.
// The transaction connection is unused; returns nil so the runner records this behavior-only upgrade atomically.
func migrateMembershipNumberedReads(_ *gorm.DB) error { return nil }

// migrateMembershipOperationsSafety records the behavior contract without altering historical data.
// db is intentionally unused: order projections and abuse notifications reuse existing evidence.
// Returns nil; the migration runner records the version atomically and retries safely.
func migrateMembershipOperationsSafety(_ *gorm.DB) error { return nil }

// migrateMembershipProactiveDelivery adds durable grant delivery tasks without sending notifications.
// db is the migration connection; returns retryable DDL errors and preserves all prior delivery evidence.
func migrateMembershipProactiveDelivery(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.ProactiveDelivery{})
}

// migrateMembershipProactiveCursor adds resumable scan checkpoints for databases already at v29.
// db owns the migration transaction; returns DDL errors without changing delivery facts.
func migrateMembershipProactiveCursor(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.ProactiveCursor{})
}

// migrateTelegramGuestSafety records the store safety contract without DDL or backfill.
// Panel cleanup authorization and support blocking are enforced by the new store
// code against existing columns; historical panels, sessions and blocks must not
// be guessed, retired or rewritten during upgrade. The current baseline already
// has the required models, so fresh databases need no additional baseline work.
// db is intentionally unused: this retry-safe marker issues no database/platform
// calls and always returns nil. The runner alone records version 26 in its ledger.
func migrateTelegramGuestSafety(_ *gorm.DB) error {
	return nil
}

// migrateOrderCancellationEvidence adds durable provenance without guessing historical cancellations.
// db is the migration connection; returns the additive migration error.
func migrateOrderCancellationEvidence(db *gorm.DB) error {
	if !db.Migrator().HasTable(&orderdomain.Order{}) {
		// Partial module databases must not acquire unrelated baseline tables.
		return nil
	}
	if db.Migrator().HasColumn(&orderdomain.Order{}, "CancellationReason") {
		return nil
	}
	return db.Migrator().AddColumn(&orderdomain.Order{}, "CancellationReason")
}

// Additive only: leave legacy/unknown targets and all encrypted payloads intact.
// Empty targets retain legacy shop authorization semantics at the runtime boundary.
func migrateTelegramSupport(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &supportdomain.Session{}, &supportdomain.Challenge{}, &supportdomain.BufferedMessage{})
}

// Additive B3b upgrade preserves ingress payloads and runtime delivery tombstones.
func migrateTelegramSupportDelivery(db *gorm.DB) error {
	// Earlier B3b schemas used one receipt per mapping. Albums legitimately
	// share a delivery, so remove the old unique index before AutoMigrate.
	model := &supportdomain.ReplyMapping{}
	if db.Migrator().HasTable(model) {
		indexes, err := db.Migrator().GetIndexes(model)
		if err != nil {
			return err
		}
		for _, index := range indexes {
			unique, known := index.Unique()
			columns := index.Columns()
			if known && unique && len(columns) == 1 && columns[0] == "delivery_id" {
				if err := db.Migrator().DropIndex(model, index.Name()); err != nil {
					return err
				}
			}
		}
	}
	return migrateModelsWithProgress(db,
		&supportdomain.Session{}, &supportdomain.BufferedMessage{}, &runtimedomain.Delivery{},
		&supportdomain.TopicOperation{}, &supportdomain.TopicMapping{},
		&supportdomain.ReadyDispatch{}, &supportdomain.ReplyMapping{}, &supportdomain.ResolutionAudit{},
		&supportdomain.Album{}, &supportdomain.AlbumItem{},
		&supportdomain.Downstream{}, &supportdomain.DownstreamAlbum{}, &supportdomain.SupportBlock{},
		&supportdomain.OperationReference{}, &supportdomain.OperationAudit{}, &supportdomain.OperationControl{}, &supportdomain.BotPolicy{},
	)
}

// migrateTelegramTopicRecovery 新增退避和替代槽位，并显式替换旧唯一索引。
// db 是迁移连接；返回 DDL 或索引检测错误。重跑不会删除任何历史话题/操作。
func migrateTelegramTopicRecovery(db *gorm.DB) error {
	model := &supportdomain.TopicOperation{}
	if err := migrateModelsWithProgress(db, model); err != nil {
		return err
	}
	indexes, err := db.Migrator().GetIndexes(model)
	if err != nil {
		return err
	}
	const indexName = "idx_support_topic_slot"
	wanted := []string{"bot_id", "session_id", "target_id", "route_version", "slot_version"}
	for _, index := range indexes {
		if index.Name() != indexName {
			continue
		}
		columns := index.Columns()
		unique, known := index.Unique()
		matches := known && unique && len(columns) == len(wanted)
		for position, column := range columns {
			if position >= len(wanted) || column != wanted[position] {
				matches = false
			}
		}
		if matches {
			return nil
		}
		// 先完成新增列，再替换旧索引；MySQL DDL 中断后可由同一步重跑补齐。
		if err := db.Migrator().DropIndex(model, indexName); err != nil {
			return err
		}
		break
	}
	return db.Migrator().CreateIndex(model, indexName)
}

// migrateTelegramMembershipCheckout 仅新增会员配置与订单项快照表。
// db 为迁移连接；返回 DDL 错误。不得从当前商品猜测历史订单权益或开启销售。
func migrateTelegramMembershipCheckout(db *gorm.DB) error {
	return migrateModelsWithProgress(db,
		&membershipdomain.ProductShareCode{}, &membershipdomain.ProductConfig{}, &membershipdomain.ProductTarget{},
		&membershipdomain.SKUTerm{}, &membershipdomain.OrderItemSnapshot{},
	)
}

// migrateTelegramMembershipLedger 新增 B5 来源、投影、可靠事件及履约回执。
// db 为迁移连接；返回 DDL 错误。迁移不授予历史订单、不修改快照或开启销售。
func migrateTelegramMembershipLedger(db *gorm.DB) error {
	return migrateModelsWithProgress(db,
		&membershipdomain.LedgerSource{}, &membershipdomain.LedgerRevocation{},
		&membershipdomain.LedgerProjection{}, &membershipdomain.LedgerOutbox{},
		&membershipdomain.PaidEvent{}, &fulfillmentdomain.ItemReceipt{},
	)
}

// migrateTelegramMembershipAccess adds B6a operation history without inferring membership or generating links.
// Unreleased v17 includes binding generations; existing rows retain zero, never inferred from current bindings.
// db is the migration connection; errors remain retryable through the existing migration ledger.
func migrateTelegramMembershipAccess(db *gorm.DB) error {
	return migrateModelsWithProgress(db,
		&membershipdomain.AccessInvite{}, &membershipdomain.AccessSlot{}, &membershipdomain.AccessJoin{},
		&membershipdomain.AccessOperation{}, &membershipdomain.AccessMember{}, &membershipdomain.AccessResult{},
		&membershipdomain.InviteCleanupCycle{}, &membershipdomain.InviteCleanupItem{},
	)
}

// migrateTelegramMembershipLifecycle adds only durable expiry phases and scan cursors.
// db is the migration connection; replay preserves all historical evidence and never calls Telegram.
func migrateTelegramMembershipLifecycle(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.AccessOperation{}, &membershipdomain.LifecycleCursor{}, &membershipdomain.RefundNotice{}, &membershipdomain.RefundDisposition{}, &membershipdomain.Reminder{}, &membershipdomain.ReminderCursor{})
}

// migrateTelegramRecovery adds durable task/checkpoint storage and an inactive route fence.
// db is the migration connection; replay neither switches routes nor grants membership.
func migrateTelegramRecovery(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &recoverydomain.Task{}, &recoverydomain.ScopeSlot{}, &recoverydomain.Step{}, &groupsdomain.Target{}, &groupsdomain.RecoveryFence{})
}

// migrateTelegramRecoveryMappings adds explicit reviewed beneficiary mappings and
// local support isolation checkpoints. db is the migration connection; replay
// never changes historical orders, grants admission, or performs platform calls.
func migrateTelegramRecoveryMappings(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &membershipdomain.IdentityMigration{}, &supportdomain.DisasterCheckpoint{}, &membershiprouterecovery.Run{}, &membershiprouterecovery.Evidence{})
}

// migrateTelegramRuntimeRecovery adds persistent stop fences and real worker-exit evidence.
// db is the migration connection; historical generations deliberately receive no fabricated proof.
// Returns the migration error without changing enabled intent, generation, or Telegram state.
func migrateTelegramRuntimeRecovery(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &runtimedomain.Instance{}, &runtimedomain.GenerationExit{}, &supportdomain.TopicOperation{})
}

// migrateTelegramRecoveryContinuation adds task-owned recovery evidence without platform calls.
// db is the migration handle; old route receipts and buffered payloads remain unchanged.
func migrateTelegramRecoveryContinuation(db *gorm.DB) error {
	if err := groupsstore.MigrateRecoveryTargets(db); err != nil {
		return err
	}
	if err := membershiprouterecovery.MigrateBotRecovery(db); err != nil {
		return err
	}
	return migrateModelsWithProgress(db, &supportdomain.RestorationCheckpoint{}, &supportdomain.RestorationEntry{}, &supportdomain.BufferedMessage{})
}

// migrateTelegramSupportDownstreamLiabilities adds explicit counted-source receipts.
// db is the migration handle; errors prevent the version ledger from advancing.
// Historical aggregate-only entries keep tracking version zero: no inferred sources or decrements.
func migrateTelegramSupportDownstreamLiabilities(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &supportdomain.RestorationCheckpoint{}, &supportdomain.RestorationEntry{}, &supportdomain.RestorationDownstreamLiability{})
}

// migratePaymentManualConfirmation 新增独立收款审计、渠道证据占用及迟到收款异常表。
// db 为持有迁移锁的连接；返回建表/索引错误。重放不修改支付原始证据，不推断历史人工确认。
func migratePaymentManualConfirmation(db *gorm.DB) error {
	return migrateModelsWithProgress(db,
		&paymentdomain.ManualConfirmation{},
		&paymentdomain.PaymentEvidenceClaim{},
		&paymentdomain.PaymentReceiptAnomaly{},
	)
}

func migrateTelegramGroups(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &groupsdomain.Target{}, &groupsdomain.Route{}, &groupsdomain.Operation{}, &groupsdomain.ModuleConfig{})
}

func migrateTelegramPayloadRetention(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &runtimedomain.Inbox{}, &runtimedomain.Delivery{})
}

// Preserve the deployed v5 ledger slot without creating new credential snapshots.
// Retain the channel schema upgrade: pre-v5 installations still need Revision for
// ordinary channel compare-and-swap writes, independently of retired ownership.
// Existing ownership records and linkage columns are left untouched.
func retireTelegramOwnership(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &channelclientdomain.Client{})
}

func migrateTelegramRuntime(db *gorm.DB) error {
	return migrateModelsWithProgress(db,
		&runtimedomain.Instance{}, &runtimedomain.Contact{}, &runtimedomain.Inbox{},
		&runtimedomain.Session{}, &runtimedomain.Delivery{},
	)
}

func migrateTelegramMediaCache(db *gorm.DB) error {
	return migrateModelsWithProgress(db, &runtimedomain.MediaCache{})
}

func migrateTelegramBusinessDelivery(db *gorm.DB) error {
	// v2 installations may predate the delivery ownership/broadcast metadata.
	// Reconcile runtime tables here too; v2 is not rerun for a versioned v2 DB.
	if err := migrateTelegramRuntime(db); err != nil {
		return err
	}
	return migrateModelsWithProgress(db,
		&userauthdomain.TelegramHistoryGrant{},
		&deliverycontract.Event{}, &deliverycontract.OrderSource{}, &deliverycontract.OrderOperation{},
		&paymentdomain.Payment{}, &broadcastdomain.Broadcast{},
	)
}

func AutoMigrate() error {
	return runSchemaMigrations(gormdb.DB, schemaVersion, migrateSchema, schemaSteps...)
}

func validateMigrationSteps(target int64, baseline func(*gorm.DB) error, steps []migrationStep) error {
	if target < 1 || baseline == nil {
		return errors.New("invalid schema migration target or baseline")
	}
	if int64(len(steps)) != target-1 {
		return fmt.Errorf("schema migration steps must cover every version 2..%d", target)
	}
	for i, step := range steps {
		if step.version != int64(i)+2 || step.name == "" || step.run == nil {
			return fmt.Errorf("invalid schema migration step %d: expected version %d with name and callback", i, i+2)
		}
	}
	return nil
}

// runSchemaMigrations preserves deployed version-1 ledgers. Completion is written
// after EACH step on the lock-owning connection. SQLite's outer transaction rolls
// back the entire attempt; MySQL/Postgres retain completed steps on later failure.
func runSchemaMigrations(db *gorm.DB, target int64, baseline func(*gorm.DB) error, steps ...migrationStep) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if err := validateMigrationSteps(target, baseline, steps); err != nil {
		return err
	}
	started := time.Now()
	log.Printf("schema migration: waiting for database lock, target=%d", target)
	db = db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Warn)})
	return withMigrationLock(db, func(locked *gorm.DB) error {
		log.Printf("schema migration: lock acquired after %s", time.Since(started))
		if err := locked.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY, completed_at VARCHAR(40) NOT NULL
		)`).Error; err != nil {
			return fmt.Errorf("create schema migration ledger: %w", err)
		}
		var versions []int64
		if err := locked.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&versions).Error; err != nil {
			return fmt.Errorf("read schema migration versions: %w", err)
		}
		var current int64
		for i, version := range versions {
			// The first row may be >1: a fresh baseline records only its target.
			if version < 1 || version > target || (i > 0 && version != current+1) {
				return fmt.Errorf("unknown or noncontiguous schema migration version %d (target %d)", version, target)
			}
			current = version
		}
		if current == target {
			log.Printf("schema migration: version=%d already complete, skipped (%s)", target, time.Since(started))
			return nil
		}
		apply := func(version int64, name string, run func(*gorm.DB) error) error {
			stepStarted := time.Now()
			log.Printf("schema migration: version=%d step=%s started", version, name)
			if err := run(locked); err != nil {
				return fmt.Errorf("schema migration version %d (%s): %w", version, name, err)
			}
			if err := validateMigrationForeignKeys(locked); err != nil {
				return err
			}
			if err := locked.Exec("INSERT INTO schema_migrations (version, completed_at) VALUES (?, ?)", version, time.Now().UTC().Format(time.RFC3339Nano)).Error; err != nil {
				return fmt.Errorf("record schema migration version %d: %w", version, err)
			}
			log.Printf("schema migration: version=%d step=%s completed (%s)", version, name, time.Since(stepStarted))
			return nil
		}
		if current == 0 {
			return apply(target, "current snapshot baseline", baseline)
		}
		for _, step := range steps {
			if step.version > current {
				if err := apply(step.version, step.name, step.run); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func validateMigrationForeignKeys(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	// Rebuilds need foreign_keys OFF before BEGIN; validate before recording each
	// completion. Never scan data on the already-complete startup fast path.
	rows, err := db.Raw("PRAGMA foreign_key_check").Rows()
	if err != nil {
		return err
	}
	invalid := rows.Next()
	checkErr := rows.Err()
	closeErr := rows.Close()
	if invalid {
		return errors.New("schema migration foreign key validation failed")
	}
	return errors.Join(checkErr, closeErr)
}

// sqliteMigrationTx makes GORM's existing internal Transaction calls use
// savepoints inside BEGIN IMMEDIATE, never BEGIN or a different pooled connection.
type sqliteMigrationTx struct{ gorm.ConnPool }

func (tx sqliteMigrationTx) Commit() error {
	_, err := tx.ExecContext(context.Background(), "COMMIT")
	return err
}
func (tx sqliteMigrationTx) Rollback() error {
	_, err := tx.ExecContext(context.Background(), "ROLLBACK")
	return err
}

func withMigrationLock(db *gorm.DB, run func(*gorm.DB) error) (err error) {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	conn, err := pool.Conn(db.Statement.Context)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Every statement, including nested transactions, uses the lock-owning
	// physical connection. Losing it cannot transparently resume on another one.
	locked := db.Session(&gorm.Session{NewDB: true, Context: db.Statement.Context})
	locked.Statement.ConnPool = conn
	lockCtx, cancel := context.WithTimeout(db.Statement.Context, 5*time.Minute)
	defer cancel()
	switch db.Dialector.Name() {
	case "mysql", "postgres":
		for {
			var acquired bool
			if db.Dialector.Name() == "mysql" {
				var result sql.NullInt64
				err = conn.QueryRowContext(lockCtx, "SELECT GET_LOCK(?, 0)", migrationLockName).Scan(&result)
				if err == nil && !result.Valid {
					err = errors.New("GET_LOCK returned NULL")
				}
				acquired = result.Valid && result.Int64 == 1
			} else {
				err = conn.QueryRowContext(lockCtx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryKey).Scan(&acquired)
			}
			if err != nil {
				// A lost response can hide a successfully acquired session lock.
				// Discard the physical connection rather than pooling an owner.
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				return fmt.Errorf("acquire schema migration lock: %w", err)
			}
			if acquired {
				break
			}
			select {
			case <-lockCtx.Done():
				return fmt.Errorf("wait for schema migration lock: %w", lockCtx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released bool
			var releaseErr error
			if db.Dialector.Name() == "mysql" {
				releaseErr = conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(&released)
			} else {
				releaseErr = conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryKey).Scan(&released)
			}
			if releaseErr == nil && !released {
				releaseErr = errors.New("schema migration lock was not owned")
			}
			if releaseErr != nil {
				// Never return a session that might still own a lock to the pool.
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				err = errors.Join(err, fmt.Errorf("release schema migration lock: %w", releaseErr))
			}
		}()
		return run(locked)
	case "sqlite":
		var foreignKeys, busyTimeout int
		if err = conn.QueryRowContext(lockCtx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			return err
		}
		if err = conn.QueryRowContext(lockCtx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, fkErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
			_, busyErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout))
			if restoreErr := errors.Join(fkErr, busyErr); restoreErr != nil {
				_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
				err = errors.Join(err, restoreErr)
			}
		}()
		if _, err = conn.ExecContext(lockCtx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		if _, err = conn.ExecContext(lockCtx, "PRAGMA busy_timeout = 300000"); err != nil {
			return err
		}
		if _, err = conn.ExecContext(lockCtx, "BEGIN IMMEDIATE"); err != nil {
			// An interrupted BEGIN can leave its outcome unknown.
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
			return fmt.Errorf("acquire sqlite migration write lock: %w", err)
		}
		tx := sqliteMigrationTx{ConnPool: conn}
		committed := false
		defer func() {
			if !committed {
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
					err = errors.Join(err, rollbackErr)
				}
			}
		}()
		locked = locked.Session(&gorm.Session{SkipDefaultTransaction: true})
		locked.Statement.ConnPool = tx
		if err = run(locked); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	default:
		return fmt.Errorf("unsupported schema migration dialect %q", db.Dialector.Name())
	}
}

// Order the entire registry before splitting calls: independently migrating the
// original list would lose GORM's cross-model dependency ordering. Models here
// have no creation-time associations; AutoMigrate still handles future ones.
func migrateModelsWithProgress(db *gorm.DB, models ...interface{}) error {
	ordered := (migrator.Migrator{Config: migrator.Config{DB: db}}).ReorderModels(models, true)
	for i, model := range ordered {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		started := time.Now()
		log.Printf("schema migration: table=%s progress=%d/%d started", stmt.Schema.Table, i+1, len(ordered))
		if err := db.AutoMigrate(model); err != nil {
			return fmt.Errorf("auto migrate table %s: %w", stmt.Schema.Table, err)
		}
		log.Printf("schema migration: table=%s progress=%d/%d completed (%s)", stmt.Schema.Table, i+1, len(ordered), time.Since(started))
	}
	return nil
}

func migrateSchema(db *gorm.DB) error {
	var err error
	db, err = gormdb.WithMigrationDatabase(db)
	if err != nil {
		return err
	}
	log.Printf("schema migration: stage=preflight started")
	if err := validateMappingExternalIDLengths(db); err != nil {
		return err
	}
	if err := validateProcurementLocalUniqueness(db); err != nil {
		return err
	}
	log.Printf("schema migration: stage=auto migrate models started")
	if err := migrateModelsWithProgress(db,
		&admindomain.Admin{},
		&userdomain.User{},
		&externalidentitydomain.Identity{},
		&userauthdomain.TelegramHistoryGrant{},
		&deliverycontract.Event{},
		&deliverycontract.OrderSource{},
		&deliverycontract.OrderOperation{},
		&invitecodedomain.InviteCode{},
		&affiliatedomain.Profile{},
		&affiliatedomain.Click{},
		&affiliatedomain.Commission{},
		&affiliatedomain.WithdrawRequest{},
		&walletdomain.Account{},
		&walletdomain.Transaction{},
		&walletdomain.RechargeOrder{},
		&auditlogdomain.UserLoginLog{},
		&auditlogdomain.AuthzAuditLog{},
		&notificationdomain.NotificationLog{},
		&auditlogdomain.AdminLoginLog{},
		&emailverificationdomain.Code{},
		&orderdomain.Order{},
		&orderdomain.OrderItem{},
		&orderdomain.OrderRefundRecord{},
		&orderdomain.MembershipRefundRequest{},
		&orderriskdomain.LockKey{},
		&cartdomain.Item{},
		&paymentdomain.PaymentChannel{},
		&paymentdomain.Payment{},
		&paymentdomain.ManualConfirmation{}, &paymentdomain.PaymentEvidenceClaim{}, &paymentdomain.PaymentReceiptAnomaly{},
		&cardsecretdomain.Secret{},
		&cardsecretdomain.Batch{},
		&giftcarddomain.GiftCard{},
		&giftcarddomain.GiftCardBatch{},
		&fulfillmentdomain.Fulfillment{},
		&coupondomain.Coupon{},
		&coupondomain.CouponUsage{},
		&promotiondomain.Promotion{},
		&categorydomain.Category{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&contentdomain.Post{},
		&contentdomain.PostProduct{},
		&contentdomain.PostCategory{},
		&contentdomain.Banner{},
		&settingsstore.SettingRecord{},
		&apicredentialdomain.ApiCredential{},
		&siteconnectiondomain.Connection{},
		&mappingdomain.Mapping{},
		&mappingdomain.SKUMapping{},
		&procurementdomain.Order{},
		&downstreamcallbackdomain.OrderRef{},
		&reconciliationdomain.Job{},
		&reconciliationdomain.Item{},
		&channelclientdomain.Client{},
		&broadcastdomain.Broadcast{},
		&runtimedomain.Instance{},
		&runtimedomain.GenerationExit{},
		&runtimedomain.Contact{},
		&runtimedomain.Inbox{},
		&runtimedomain.Session{},
		&runtimedomain.Delivery{},
		&runtimedomain.MediaCache{},
		&groupsdomain.Target{}, &groupsdomain.Route{}, &groupsdomain.Operation{}, &groupsdomain.ModuleConfig{},
		&supportdomain.Session{}, &supportdomain.Challenge{}, &supportdomain.BufferedMessage{},
		&supportdomain.TopicOperation{}, &supportdomain.TopicMapping{}, &supportdomain.ReadyDispatch{},
		&supportdomain.ReplyMapping{}, &supportdomain.ResolutionAudit{},
		&supportdomain.Album{}, &supportdomain.AlbumItem{},
		&supportdomain.Downstream{}, &supportdomain.DownstreamAlbum{}, &supportdomain.SupportBlock{},
		&supportdomain.OperationReference{}, &supportdomain.OperationAudit{}, &supportdomain.OperationControl{}, &supportdomain.BotPolicy{},
		&membershipdomain.ProductShareCode{}, &membershipdomain.ProductConfig{}, &membershipdomain.ProductTarget{},
		&membershipdomain.SKUTerm{}, &membershipdomain.OrderItemSnapshot{},
		&membershipdomain.LedgerSource{}, &membershipdomain.LedgerRevocation{},
		&membershipdomain.LedgerProjection{}, &membershipdomain.LedgerOutbox{},
		&membershipdomain.PaidEvent{}, &membershipdomain.ProactiveDelivery{}, &membershipdomain.ProactiveCursor{}, &fulfillmentdomain.ItemReceipt{},
		&membershipdomain.AccessInvite{}, &membershipdomain.AccessSlot{}, &membershipdomain.AccessJoin{},
		&membershipdomain.AccessOperation{}, &membershipdomain.AccessMember{}, &membershipdomain.AccessResult{},
		&membershipdomain.InviteCleanupCycle{}, &membershipdomain.InviteCleanupItem{},
		&membershipdomain.LifecycleCursor{}, &membershipdomain.RefundNotice{}, &membershipdomain.RefundDisposition{}, &membershipdomain.Reminder{}, &membershipdomain.ReminderCursor{},
		&recoverydomain.Task{}, &recoverydomain.ScopeSlot{}, &recoverydomain.Step{}, &groupsdomain.RecoveryFence{},
		&membershipdomain.IdentityMigration{}, &supportdomain.DisasterCheckpoint{}, &supportdomain.RestorationCheckpoint{}, &supportdomain.RestorationEntry{}, &supportdomain.RestorationDownstreamLiability{}, &membershiprouterecovery.Run{}, &membershiprouterecovery.Evidence{},
		&memberleveldomain.MemberLevel{},
		&memberleveldomain.MemberLevelPrice{},
		&contentdomain.Media{},
	); err != nil {
		return err
	}
	// 无版本存量库走 baseline，也必须替换 AutoMigrate 不会更新的旧话题索引。
	if err := migrateTelegramTopicRecovery(db); err != nil {
		return err
	}

	stages := []struct {
		name string
		run  func(...*gorm.DB) error
	}{
		{"telegram per-target recovery and support restoration", func(dbs ...*gorm.DB) error { return migrateTelegramRecoveryContinuation(dbs[0]) }},
		{"oauth identity unique index", ensureUserOAuthIdentityUserProviderUniqueIndex},
		{"pending order risk IPs", func(dbs ...*gorm.DB) error { return backfillPendingOrderRiskIPs(dbs[0]) }},
		{"reseller schema", func(dbs ...*gorm.DB) error { return resellerstore.Migrate(dbs[0]) }},
		{"cart SKU unique index", migrateCartSKUUniqueIndex},
		{"product SKU backfill", ensureProductSKUMigration},
		{"manual remaining stock", ensureManualStockRemainingMigration},
		{"category parent", ensureCategoryParentMigration},
		{"payment provider rename", ensurePaymentProviderBepusdtRenameMigration},
		{"payment channel config", ensurePaymentChannelBepusdtConfigMigration},
		{"payment fee policy", ensurePaymentFeePolicyMigration},
		{"order refund fee", ensureOrderRefundPaymentFeeMigration},
		{"order item original price", ensureOrderItemOriginalPriceMigration},
		{"cart foreign keys", ensureCartForeignKeyConstraints},
		{"procurement foreign key", ensureProcurementOrderForeignKeyConstraint},
	}
	for _, stage := range stages {
		started := time.Now()
		log.Printf("schema migration: stage=%s started", stage.name)
		if err := stage.run(db); err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
		log.Printf("schema migration: stage=%s completed (%s)", stage.name, time.Since(started))
	}
	if db.Migrator().HasColumn(&productdomain.Product{}, "price_currency") {
		if err := db.Migrator().DropColumn(&productdomain.Product{}, "price_currency"); err != nil {
			return err
		}
	}
	return nil
}

// backfillPendingOrderRiskIPs 只迁移仍可能占用库存的在途订单，避免启动时扫描全部历史订单。
func backfillPendingOrderRiskIPs(db *gorm.DB) error {
	type orderIPRow struct {
		ID       uint
		ClientIP string
	}
	var rows []orderIPRow
	if err := db.Model(&orderdomain.Order{}).
		Select("id", "client_ip").
		Where("deleted_at IS NULL AND status = ? AND (risk_ip IS NULL OR risk_ip = '') AND client_ip <> ''", constants.OrderStatusPendingPayment).
		Find(&rows).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			riskIP := orderriskcontract.NormalizeRiskIP(row.ClientIP)
			if riskIP == "" {
				continue
			}
			if err := tx.Model(&orderdomain.Order{}).
				Where("id = ? AND (risk_ip IS NULL OR risk_ip = '')", row.ID).
				UpdateColumn("risk_ip", riskIP).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
