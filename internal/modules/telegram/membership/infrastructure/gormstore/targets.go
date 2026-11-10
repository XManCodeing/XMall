package gormstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/constants"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	ga "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	gs "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TargetNames reads names for ids on the current connection without requiring bot/group admin permissions.
// It returns only existing targets; callers retain IDs as a fallback for deleted or unnamed groups.
func (store *Store) TargetNames(ctx context.Context, ids []uint) (map[uint]string, error) {
	names := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	var targets []gd.Target
	if err := store.db.WithContext(ctx).Select("id", "name").Where("id IN ?", ids).Find(&targets).Error; err != nil {
		return nil, err
	}
	for _, target := range targets {
		names[target.ID] = target.Name
	}
	return names, nil
}

var shareBotUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

// ProductShare resolves config's complete target set on one database connection.
// active and config are the already-read persisted product projection. Invalid sale,
// binding or bot state yields a stable reason; database failures remain errors.
func (store *Store) ProductShare(ctx context.Context, config *d.ProductConfig, active bool) (c.ProductShare, error) {
	out := c.ProductShare{UnavailableReason: c.ShareUnavailable}
	if config == nil || config.ProductID == 0 || !active || !config.Enabled {
		return out, nil
	}
	out.UnavailableReason = c.ShareMissingBinding
	if len(config.Targets) == 0 {
		return out, nil
	}
	err := store.transaction(ctx, func(tx *gorm.DB) error {
		ids := make([]uint, 0, len(config.Targets))
		seen := make(map[uint]bool, len(config.Targets))
		for _, target := range config.Targets {
			if target.TargetID == 0 || seen[target.TargetID] {
				return nil
			}
			seen[target.TargetID] = true
			ids = append(ids, target.TargetID)
		}
		var targets []gd.Target
		if err := tx.Select("id", "bot_instance_id", "purpose", "enabled", "telegram_chat_id").Where("id IN ?", ids).Find(&targets).Error; err != nil {
			return err
		}
		if len(targets) != len(ids) {
			return nil
		}
		var botID uint
		for _, target := range targets {
			if target.BotInstanceID == 0 || target.Purpose != "membership" || !target.Enabled || target.TelegramChatID >= 0 {
				return nil
			}
			if botID != 0 && target.BotInstanceID != botID {
				out.UnavailableReason = c.ShareMixedBots
				return nil
			}
			botID = target.BotInstanceID
		}
		out.UnavailableReason = c.ShareBotUnavailable
		var bot rd.Instance
		err := tx.Select("id", "telegram_id", "username", "enabled", "mode", "recovery_task_id").First(&bot, botID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// Reuse the runtime's managed-shop eligibility, but deliberately omit
		// heartbeat/state/generation and group observation expiry: links are permanent.
		if !bot.Enabled || bot.Mode != "internal" || bot.RecoveryTaskID != 0 || bot.TelegramID <= 0 || !shareBotUsername.MatchString(bot.Username) || !strings.HasSuffix(strings.ToLower(bot.Username), "bot") {
			return nil
		}
		out.UnavailableReason = ""
		var mapping d.ProductShareCode
		err = tx.First(&mapping, "product_id = ?", config.ProductID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out.URL = fmt.Sprintf("https://t.me/%s?start=%s", bot.Username, mapping.Code)
		return nil
	})
	return out, err
}

var _ c.ShareReader = (*Store)(nil)

// newShareCode returns twelve unbiased ASCII letters/digits from reader, or its entropy error.
func newShareCode(reader io.Reader) (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	code := make([]byte, 0, 12)
	for len(code) < 12 {
		var buffer [24]byte
		if _, err := io.ReadFull(reader, buffer[:]); err != nil {
			return "", fmt.Errorf("membership share entropy: %w", err)
		}
		for _, value := range buffer {
			if value < 248 && len(code) < 12 {
				code = append(code, alphabet[int(value)%len(alphabet)])
			}
		}
	}
	return string(code), nil
}

// ensureShareCode uses tx exclusively; conflict suppression keeps PostgreSQL transactions usable.
// Product primary key serializes competing creators; code conflicts retry at most eight times.
func ensureShareCode(tx *gorm.DB, productID uint) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var existing d.ProductShareCode
		err := tx.First(&existing, "product_id = ?", productID).Error
		if err == nil {
			return existing.Code, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
		code, err := newShareCode(rand.Reader)
		if err != nil {
			return "", err
		}
		mapping := d.ProductShareCode{ProductID: productID, Code: code}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mapping)
		if inserted.Error != nil {
			return "", inserted.Error
		}
		if inserted.RowsAffected == 1 {
			return code, nil
		}
	}
	return "", fmt.Errorf("membership share code collision retry limit reached")
}

// EnsureProductShare locks the current product/config and creates a code only for eligible links.
// All nested reads use the same transaction connection, including SQLite's single connection.
func (store *Store) EnsureProductShare(ctx context.Context, productID uint) (out c.ProductShare, err error) {
	if productID == 0 {
		return out, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var product p.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&product, productID).Error; err != nil {
			return err
		}
		config, err := readConfig(tx, productID)
		if err != nil {
			return err
		}
		bound := New(tx)
		out, err = bound.ProductShare(ctx, config, product.IsActive)
		if err != nil || out.UnavailableReason != "" || out.URL != "" {
			return err
		}
		if _, err = ensureShareCode(tx, productID); err != nil {
			return err
		}
		out, err = bound.ProductShare(ctx, config, product.IsActive)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "membership share creation failed", "product_id", productID)
		return c.ProductShare{}, err
	}
	return out, nil
}

// ResolveProductShare validates the exact public code and returns its ID without any write.
func (store *Store) ResolveProductShare(ctx context.Context, code string) (uint, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9]{12}$`).MatchString(code) {
		return 0, c.ErrNotFound
	}
	var mapping d.ProductShareCode
	if err := store.db.WithContext(ctx).First(&mapping, "code = ?", code).Error; err != nil {
		return 0, result(err)
	}
	return mapping.ProductID, nil
}

// userAccessPurchase 为真实订单项快照及订单付款时间的只读联合结果。
type userAccessPurchase struct {
	d.OrderItemSnapshot
	PaidAt time.Time
}

// userAccessHistory 隔离同目标的历史身份；不同身份绝不混合期限。
type userAccessHistory struct {
	telegramID int64
	paidAt     time.Time
	itemID     uint
	purchases  []userAccessPurchase
	sources    []d.LedgerSource
}

// ListUserAccess 使用认证 accountID、排他 afterTargetID、1..100 limit 和可信 now，
// 在同一事务连接中返回无秘密的账户历史分页；不调用平台、不创建账号、不修改投影。
func (store *Store) ListUserAccess(ctx context.Context, accountID, afterTargetID uint, limit int, now time.Time) ([]c.UserAccessGroup, error) {
	if accountID == 0 || limit < 1 || limit > 100 || now.IsZero() {
		return nil, c.ErrInvalid
	}
	out := make([]c.UserAccessGroup, 0, limit)
	err := store.transaction(ctx, func(tx *gorm.DB) error {
		telegramID, err := userAccessBinding(tx, accountID)
		if err != nil {
			return err
		}
		history, err := userAccessHistories(tx, accountID, afterTargetID, now)
		if err != nil {
			return err
		}
		targets := make([]uint, 0, len(history))
		for targetID := range history {
			targets = append(targets, targetID)
		}
		sort.Slice(targets, func(left, right int) bool { return targets[left] < targets[right] })
		if len(targets) > limit {
			targets = targets[:limit]
		}
		for _, targetID := range targets {
			item, err := userAccessItem(tx, accountID, telegramID, targetID, history[targetID], now)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "membership user access projection failed", "account_id", accountID)
		return nil, err
	}
	return out, nil
}

// userAccessBinding 只从 tx 读取账号有效性与原身份表，返回可用绑定或零值。
// 账号无效拒绝查询；解绑、格式异常或重复身份仅禁止操作，不隐藏购买历史。
// userAccessBinding 返回当前有效 Telegram ID；所有读取复用传入 tx。
func userAccessBinding(tx *gorm.DB, accountID uint) (int64, error) {
	telegramID, _, err := userAccessBindingSnapshot(tx, accountID)
	return telegramID, err
}

// userAccessBindingSnapshot 在 tx 内校验账号和唯一绑定，返回身份及绑定行版本。
// 复用绑定行 ID 作为代次识别同 Telegram ID 解绑重绑，不混用其他连接。
func userAccessBindingSnapshot(tx *gorm.DB, accountID uint) (int64, uint, error) {
	var account struct{ Status string }
	err := tx.Table("users").Select("status").Where("id = ? AND deleted_at IS NULL", accountID).Take(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, c.ErrUntrustedIdentity
	}
	if err != nil {
		return 0, 0, err
	}
	if strings.ToLower(strings.TrimSpace(account.Status)) != constants.UserStatusActive {
		return 0, 0, c.ErrUntrustedIdentity
	}
	var identities []ei.Identity
	if err := tx.Select("id", "user_id", "provider_user_id").Where("user_id = ? AND provider = ?", accountID, constants.UserOAuthProviderTelegram).Limit(2).Find(&identities).Error; err != nil {
		return 0, 0, err
	}
	if len(identities) != 1 {
		return 0, 0, nil
	}
	telegramID, err := strconv.ParseInt(identities[0].ProviderUserID, 10, 64)
	if err != nil || telegramID <= 0 || strconv.FormatInt(telegramID, 10) != identities[0].ProviderUserID {
		return 0, 0, nil
	}
	return telegramID, identities[0].ID, nil
}

// userAccessHistories 按 accountID 的实际订单付款快照和账本来源构建 target 分组。
// after 排除已读目标，now 排除未来付款；同目标选最近付款身份，与当前绑定无关。
func userAccessHistories(tx *gorm.DB, accountID, after uint, now time.Time) (map[uint]*userAccessHistory, error) {
	var purchases []userAccessPurchase
	err := tx.Table("telegram_membership_order_item_snapshots AS s").Select("s.*, o.paid_at").
		Joins("JOIN order_items AS i ON i.id = s.order_item_id").Joins("JOIN orders AS o ON o.id = i.order_id").
		Where("s.account_id = ? AND o.user_id = ?", accountID, accountID).
		Where("i.deleted_at IS NULL AND o.deleted_at IS NULL AND o.paid_at IS NOT NULL AND o.paid_at <= ?", now).
		Where("o.canceled_at IS NULL AND o.status NOT IN ?", []string{constants.OrderStatusPendingPayment, constants.OrderStatusCanceled}).
		Where("i.product_id = s.product_id AND i.sku_id = s.sku_id AND i.quantity = 1 AND s.quantity = 1").Find(&purchases).Error
	if err != nil {
		return nil, err
	}
	var sources []d.LedgerSource
	if err := tx.Where("account_id = ? AND target_id > ?", accountID, after).Order("id").Find(&sources).Error; err != nil {
		return nil, err
	}
	history := make(map[uint]*userAccessHistory)
	for _, purchase := range purchases {
		for _, target := range purchase.Targets {
			if target.TargetID > after && purchase.BeneficiaryTelegramID > 0 {
				group := userAccessGroup(history, target.TargetID, purchase.BeneficiaryTelegramID, purchase.OrderItemID, purchase.PaidAt)
				group.purchases = append(group.purchases, purchase)
			}
		}
	}
	for _, source := range sources {
		if source.BeneficiaryTelegramID <= 0 {
			return nil, c.ErrInvalid
		}
		group := userAccessGroup(history, source.TargetID, source.BeneficiaryTelegramID, source.OrderItemID, source.PaidAt)
		group.sources = append(group.sources, source)
	}
	return history, nil
}

// userAccessGroup 以付款时刻、订单项 ID 确定 target 的展示身份，返回累计事实容器。
func userAccessGroup(history map[uint]*userAccessHistory, target uint, telegram int64, item uint, paid time.Time) *userAccessHistory {
	group := history[target]
	if group == nil {
		group = &userAccessHistory{}
		history[target] = group
	}
	if group.telegramID == 0 || paid.After(group.paidAt) || (paid.Equal(group.paidAt) && item > group.itemID) {
		group.telegramID, group.paidAt, group.itemID = telegram, paid, item
	}
	return group
}

// userAccessItem 从 tx 的独立历史 group 重算 target 资格并附加安全当前状态。
// binding 只决定操作能力，不决定是否展示；accountID 仅用于邀请归属核验。
func userAccessItem(tx *gorm.DB, accountID uint, binding int64, targetID uint, group *userAccessHistory, now time.Time) (c.UserAccessGroup, error) {
	mapping, err := im.Mapping(tx, group.telegramID, targetID)
	if err != nil {
		return c.UserAccessGroup{}, err
	}
	if mapping != nil {
		if mapping.AccountID != accountID {
			return c.UserAccessGroup{}, c.ErrUntrustedIdentity
		}
		// Normalize copies for display calculation only; stored provenance is never changed.
		group = &userAccessHistory{telegramID: mapping.NewTelegramID, purchases: append([]userAccessPurchase(nil), group.purchases...), sources: append([]d.LedgerSource(nil), group.sources...)}
		for index := range group.sources {
			if group.sources[index].BeneficiaryTelegramID == mapping.OldTelegramID {
				group.sources[index].BeneficiaryTelegramID = mapping.NewTelegramID
			}
		}
		for index := range group.purchases {
			if group.purchases[index].BeneficiaryTelegramID == mapping.OldTelegramID {
				group.purchases[index].BeneficiaryTelegramID = mapping.NewTelegramID
			}
		}
	}
	item := c.UserAccessGroup{TargetID: targetID, BeneficiaryTelegramID: group.telegramID, State: "processing"}
	var target gd.Target
	err = tx.First(&target, targetID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return item, err
	}
	item.Title = target.Name
	// A logical label is not the replacement chat's title. Only a matching-route
	// observation can describe the current physical chat; stale observations are ignored.
	if target.Observation.RouteVersion == target.RouteVersion && strings.TrimSpace(target.Observation.Title) != "" {
		item.Title = target.Observation.Title
	}
	calculation, sourceIDs, pending, revoked, err := userAccessCalculation(tx, targetID, group, now)
	if err != nil {
		return item, err
	}
	item.Permanent = calculation.Effective.Kind == d.TermPermanent
	item.ExpiresAt = calculation.Effective.ExpiresAt
	if !item.Permanent && item.ExpiresAt == nil {
		item.ExpiresAt = calculation.LimitedExpiresAt
	}
	if calculation.Effective.Active {
		item.State = "available"
	}
	if len(sourceIDs) > 0 {
		if err := userAccessCurrentState(tx, accountID, target, sourceIDs, now, &item); err != nil {
			return item, err
		}
	}
	if !calculation.Effective.Active {
		item.CanRequest = false
		switch {
		case pending:
			item.State = "processing"
		case revoked:
			item.State = "revoked"
		case len(sourceIDs) > 0:
			item.State = "expired"
		}
		return item, nil
	}
	if binding != group.telegramID {
		item.State, item.CanRequest = "identity_mismatch", false
		return item, nil
	}
	service := ga.New(nil, nil)
	service.Now = func() time.Time { return now }
	if !validTarget(target, service, now) {
		item.State, item.CanRequest = "unavailable", false
	}
	return item, nil
}

// userAccessCalculation 仅重算 group 选定身份及 target 的真实账本来源，返回资格、
// 来源 ID、未处理付款和全部已撤标记；墓碑同样覆盖尚未生成账本的付款快照。
func userAccessCalculation(tx *gorm.DB, target uint, group *userAccessHistory, now time.Time) (d.EntitlementCalculation, []uint, bool, bool, error) {
	scope := d.EntitlementScope{BeneficiaryTelegramID: group.telegramID, TargetID: target}
	_, revocations, _, err := im.Sources(tx, group.telegramID, target)
	if err != nil {
		return d.EntitlementCalculation{}, nil, false, false, err
	}
	tombstones := make([]d.SourceKey, 0, len(revocations))
	revoked := make(map[uint]bool)
	for _, row := range revocations {
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID})
		revoked[row.OrderItemID] = true
	}
	sources := []d.EntitlementSource{}
	sourceIDs := []uint{}
	processed := make(map[uint]bool)
	allRevoked, hasFact, pending := true, false, false
	for _, row := range group.sources {
		if row.BeneficiaryTelegramID != group.telegramID {
			continue
		}
		sources = append(sources, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
		sourceIDs = append(sourceIDs, row.ID)
		processed[row.OrderItemID], hasFact = true, true
		allRevoked = allRevoked && revoked[row.OrderItemID]
	}
	for _, purchase := range group.purchases {
		if purchase.BeneficiaryTelegramID != group.telegramID {
			continue
		}
		hasFact = true
		allRevoked = allRevoked && revoked[purchase.OrderItemID]
		pending = pending || (!processed[purchase.OrderItemID] && !revoked[purchase.OrderItemID])
	}
	calculation, err := d.RecalculateEntitlement(scope, sources, tombstones, now)
	return calculation, sourceIDs, pending, hasFact && allRevoked, err
}

// userAccessCurrentState 将当前 target 物理路由的可信成员/邀请事实写入 item。
// sourceIDs 限定当前账号与受益人来源；不读取链接、哈希或密文，旧路由不能冒充当前状态。
func userAccessCurrentState(tx *gorm.DB, accountID uint, target gd.Target, sourceIDs []uint, now time.Time, item *c.UserAccessGroup) error {
	item.CanRequest = true
	if err := userAccessOperationState(tx, accountID, target, item); err != nil {
		return err
	}
	if item.State == "unknown" || item.State == "recovering" {
		return nil
	}
	var member d.AccessMember
	err := tx.Where("target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND (source_id IN ? OR status = 'kicked')", item.TargetID, item.BeneficiaryTelegramID, target.BotInstanceID, target.TelegramChatID, target.RouteVersion, sourceIDs).
		Where("observation_source IN ? AND observed_at <= ?", []string{"chat_member", "query"}, now).Take(&member).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		item.MemberStatus = member.Status
		if member.Status == "kicked" {
			item.State, item.CanRequest = "unavailable", false
			return nil
		}
		if member.Status == "member" || member.Status == "administrator" || member.Status == "creator" || (member.Status == "restricted" && member.IsMember) {
			item.State, item.CanRequest = "member", false
			return nil
		}
	}
	var invite d.AccessInvite
	err = tx.Select("id", "state", "expires_at").Where("account_id = ? AND target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND source_id IN ?", accountID, item.TargetID, item.BeneficiaryTelegramID, target.BotInstanceID, target.TelegramChatID, target.RouteVersion, sourceIDs).Order("id DESC").Take(&invite).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch invite.State {
	case "unknown":
		item.State, item.CanRequest = "unknown", false
	case "creating", "pending":
		item.State, item.CanRequest = "recovering", false
	case "failed":
		item.State = "recovering"
	case "active", "expired", "revoked":
		// 邀请过期/撤销不是权益过期/撤权，有效资格可重新申请。
	default:
		item.State, item.CanRequest = "unknown", false
	}
	return nil
}

// userAccessOperationState projects approval/revocation anomalies before reusable invitation availability.
// Scope includes authenticated account and exact current physical route; no invitation secrets are selected.
func userAccessOperationState(tx *gorm.DB, account uint, target gd.Target, item *c.UserAccessGroup) error {
	var operation d.AccessOperation
	err := tx.Select("state").Where("account_id = ? AND target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND kind IN ? AND state IN ?", account, item.TargetID, item.BeneficiaryTelegramID, target.BotInstanceID, target.TelegramChatID, target.RouteVersion, []string{"approve", "revoke", "reconcile"}, []string{"unknown", "needs_review", "failed", "retryable", "pending", "creating"}).Where("COALESCE(review_reason, '') <> ?", "expired_entry_rebuilt").Order("CASE WHEN state IN ('unknown', 'needs_review') THEN 0 ELSE 1 END, id DESC").Take(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	item.State, item.CanRequest = "recovering", false
	if operation.State == "unknown" || operation.State == "needs_review" {
		item.State = "unknown"
	}
	return nil
}

// OrderMembershipTargets reads immutable targets within one owned parent/child order scope.
// Current binding and snapshot beneficiary must match; no invitation or work is created.
func (store *Store) OrderMembershipTargets(ctx context.Context, account uint, beneficiary int64, order uint) ([]uint, error) {
	history, err := store.OrderMembershipHistory(ctx, account, beneficiary, order)
	if err != nil {
		return nil, err
	}
	targets := make([]uint, 0, len(history))
	for _, target := range history {
		targets = append(targets, target.TargetID)
	}
	return targets, nil
}

var _ c.OrderAccessReader = (*Store)(nil)
var _ c.UserAccessReader = (*Store)(nil)

// ProactiveModels returns models for the central migration registry; no migration runs implicitly.
func ProactiveModels() []any { return []any{&d.ProactiveDelivery{}, &d.ProactiveCursor{}} }

// ScanProactive consumes new grants every tick and repairs one historical page per minute.
// Both persisted checkpoints advance past broken sources; history wraps and retries them later.
func (store *Store) ScanProactive(ctx context.Context, bot uint, limit int, now time.Time) (int, error) {
	if bot == 0 || limit < 1 || limit > 100 || now.IsZero() {
		return 0, c.ErrInvalid
	}
	var checkpoint d.ProactiveCursor
	// Start the live lane before recent grants so a payment immediately before
	// startup is not buried in history. Older rows belong to minute-paced repair.
	if err := store.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.First(&checkpoint, "bot_id = ?", bot).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var latest uint
		if err := tx.Model(&d.LedgerSource{}).Where("created_at < ?", now.Add(-time.Minute)).Select("COALESCE(MAX(id), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		seed := d.ProactiveCursor{BotID: bot, FastAfter: latest}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		return tx.First(&checkpoint, "bot_id = ?", bot).Error
	}); err != nil {
		return 0, err
	}
	count, fastErr := store.scanProactiveLane(ctx, bot, limit, now, checkpoint, false)
	if now.Before(checkpoint.HistoryAt.Add(time.Minute)) {
		return count, fastErr
	}
	historical, historyErr := store.scanProactiveLane(ctx, bot, limit, now, checkpoint, true)
	return count + historical, errors.Join(fastErr, historyErr)
}

// scanProactiveLane reads at most limit sources and commits each derived responsibility separately.
// history controls a separate checkpoint and cadence; checkpoint CAS prevents stale scan regression.
func (store *Store) scanProactiveLane(ctx context.Context, bot uint, limit int, now time.Time, checkpoint d.ProactiveCursor, history bool) (int, error) {
	after, column := checkpoint.FastAfter, "fast_after"
	if history {
		after, column = checkpoint.HistoryAfter, "history_after"
	}
	var sources []d.LedgerSource
	query := store.db.WithContext(ctx).Table("telegram_membership_ledger_sources AS s").Select("s.*").
		Joins("JOIN telegram_membership_ledger_outbox AS o ON o.order_item_id = s.order_item_id AND o.target_id = s.target_id AND o.operation = 'grant'").
		Joins("JOIN telegram_group_targets AS t ON t.id = s.target_id").
		Where("s.id > ? AND s.operation = ? AND t.bot_instance_id = ? AND NOT EXISTS (SELECT 1 FROM telegram_membership_proactive_deliveries p WHERE p.source_id = s.id)", after, "grant", bot)
	if err := query.Order("s.id ASC").Limit(limit).Find(&sources).Error; err != nil {
		return 0, err
	}
	// An empty live page has no checkpoint transition to guard. MySQL reports
	// zero changed rows for SET fast_after = fast_after; that is not a CAS loss.
	// History must still persist its cadence/wrap, and real advances keep CAS.
	if !history && len(sources) == 0 {
		return 0, nil
	}
	count, next := 0, after
	var failures []error
	for _, source := range sources {
		next = source.ID
		if err := store.transaction(ctx, func(tx *gorm.DB) error { return deriveProactive(tx, source, bot, now) }); err != nil {
			slog.WarnContext(ctx, "membership_proactive_source_scan_failed", "source_id", source.ID, "bot_id", bot)
			failures = append(failures, err)
			continue
		}
		count++
	}
	updates := map[string]any{column: next}
	cas := store.db.WithContext(ctx).Model(&d.ProactiveCursor{}).Where("bot_id = ? AND "+column+" = ?", bot, after)
	if history {
		if len(sources) < limit {
			updates[column] = uint(0)
		}
		updates["history_at"] = now
		cas = cas.Where("history_at = ?", checkpoint.HistoryAt)
	}
	result := cas.Updates(updates)
	if result.Error != nil {
		failures = append(failures, result.Error)
	} else if result.RowsAffected != 1 {
		failures = append(failures, c.ErrConflict)
	}
	return count, errors.Join(failures...)
}

// deriveProactive copies the exact grant relationship and marks obsolete history without sending.
// tx is the only connection used; invalid identity is terminal, dependency failure rolls back.
func deriveProactive(tx *gorm.DB, source d.LedgerSource, bot uint, now time.Time) error {
	var outbox d.LedgerOutbox
	if err := tx.First(&outbox, "order_item_id = ? AND target_id = ? AND operation = ?", source.OrderItemID, source.TargetID, "grant").Error; err != nil {
		return err
	}
	if outbox.BeneficiaryTelegramID != source.BeneficiaryTelegramID {
		return c.ErrConflict
	}
	var target gd.Target
	if err := tx.First(&target, "id = ? AND bot_instance_id = ?", source.TargetID, bot).Error; err != nil {
		return err
	}
	row := d.ProactiveDelivery{SourceID: source.ID, OutboxID: outbox.ID, AccountID: source.AccountID, TelegramID: source.BeneficiaryTelegramID, TargetID: source.TargetID, BotID: bot, ChatID: target.TelegramChatID, RouteVersion: target.RouteVersion, State: "preparing", InviteState: "not_requested", AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	// Route replacement is never a new purchase notification. Historical grants
	// without an exact current-route purchase snapshot remain user-initiated only.
	if target.RouteVersion > 1 {
		var snapshot d.OrderItemSnapshot
		read := tx.Where("order_item_id = ? AND account_id = ?", source.OrderItemID, source.AccountID).Find(&snapshot)
		if read.Error != nil {
			return read.Error
		}
		currentPurchase := false
		for _, purchased := range snapshot.Targets {
			currentPurchase = currentPurchase || (purchased.TargetID == target.ID && purchased.BotInstanceID == target.BotInstanceID && purchased.TelegramChatID == target.TelegramChatID && purchased.RouteVersion == target.RouteVersion)
		}
		if !currentPurchase {
			row.State = "superseded"
		}
	}
	eligible, err := accessEligibility(tx, c.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: bot}, row.TargetID, now)
	if err != nil && !errors.Is(err, c.ErrUntrustedIdentity) {
		return err
	}
	if err != nil || !eligible.Active {
		row.State = "superseded"
	}
	row.BindingVersion = eligible.BindingVersion
	var revoked int64
	if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ? AND target_id = ?", source.OrderItemID, source.TargetID).Count(&revoked).Error; err != nil {
		return err
	}
	if revoked != 0 {
		row.State = "superseded"
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}}, DoNothing: true}).Create(&row).Error
}

// ClaimProactive leases local orchestration work; an expired lease only repeats idempotent queue insertion.
// owner must be fresh per claim; platform operations remain owned by existing access/runtime workers.
func (store *Store) ClaimProactive(ctx context.Context, bot uint, owner string, now time.Time, ttl time.Duration) (out *d.ProactiveDelivery, err error) {
	if bot == 0 || owner == "" || ttl <= 0 {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var row d.ProactiveDelivery
		err := tx.Where("bot_id = ? AND state IN ? AND available_at <= ? AND (lease_until IS NULL OR lease_until <= ?)", bot, []string{"preparing", "queued"}, now, now).Order("available_at, id").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		until := now.Add(ttl)
		result := tx.Model(&d.ProactiveDelivery{}).Where("id = ? AND (lease_until IS NULL OR lease_until <= ?)", row.ID, now).Updates(map[string]any{"claim_owner": owner, "lease_until": until})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrConflict
		}
		row.ClaimOwner, row.LeaseUntil = owner, &until
		out = &row
		return nil
	})
	return out, err
}

// SaveProactive persists only orchestration facts while the exact lease is still owned.
// Immutable account/source/beneficiary/target fields cannot be updated through this API.
func (store *Store) SaveProactive(ctx context.Context, row *d.ProactiveDelivery, now time.Time) error {
	if row == nil || row.ID == 0 || row.ClaimOwner == "" {
		return c.ErrInvalid
	}
	result := store.db.WithContext(ctx).Model(&d.ProactiveDelivery{}).Where("id = ? AND claim_owner = ? AND lease_until > ?", row.ID, row.ClaimOwner, now).Updates(map[string]any{
		"invite_id": row.InviteID, "invite_state": row.InviteState, "state": row.State, "stage": row.Stage, "business_key": row.BusinessKey, "message_id": row.MessageID, "generation": row.Generation, "available_at": row.AvailableAt, "updated_at": now, "claim_owner": "", "lease_until": nil,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// Proactive returns the durable secret-free responsibility by internal ID.
func (store *Store) Proactive(ctx context.Context, id uint) (*d.ProactiveDelivery, error) {
	var row d.ProactiveDelivery
	if err := store.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, c.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// ValidateProactive rechecks immutable source ownership, revocation and current binding/entitlement.
// now is a trusted clock; grants of other sources cannot revive this revoked responsibility.
func (store *Store) ValidateProactive(ctx context.Context, row *d.ProactiveDelivery, now time.Time) error {
	return store.transaction(ctx, func(tx *gorm.DB) error {
		var source d.LedgerSource
		if err := tx.First(&source, row.SourceID).Error; err != nil {
			return err
		}
		if source.AccountID != row.AccountID || source.BeneficiaryTelegramID != row.TelegramID || source.TargetID != row.TargetID || source.Operation != "grant" {
			return c.ErrUntrustedIdentity
		}
		var outbox d.LedgerOutbox
		if err := tx.First(&outbox, row.OutboxID).Error; err != nil {
			return err
		}
		if outbox.Operation != "grant" || outbox.OrderItemID != source.OrderItemID || outbox.TargetID != source.TargetID || outbox.BeneficiaryTelegramID != source.BeneficiaryTelegramID {
			return c.ErrUntrustedIdentity
		}
		var revoked int64
		if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ? AND target_id = ?", source.OrderItemID, source.TargetID).Count(&revoked).Error; err != nil {
			return err
		}
		if revoked != 0 {
			return c.ErrUntrustedIdentity
		}
		eligible, err := accessEligibility(tx, c.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}, row.TargetID, now)
		if err != nil {
			return err
		}
		if !eligible.Active || eligible.BindingVersion == 0 || eligible.BindingVersion != row.BindingVersion {
			return c.ErrUntrustedIdentity
		}
		return nil
	})
}

// Display 在同一事务中读取 productID 配置及当前逻辑目标名称，返回无路由的白名单。
// 复用配置稳定 target_id 顺序和 groups 仓储，不探测能力、不写历史快照。
func (store *Store) Display(ctx context.Context, productID uint) (display *c.DisplayConfig, err error) {
	if productID == 0 {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		config, readErr := readConfig(tx, productID)
		if readErr != nil {
			return readErr
		}
		if len(config.Targets) == 0 {
			return c.ErrInvalid
		}
		groups := gs.New(tx)
		names := make([]string, 0, len(config.Targets))
		for _, binding := range config.Targets {
			target, targetErr := groups.Get(ctx, binding.TargetID)
			// 不传播目标 ErrNotFound，调用者只可把配置不存在视为普通商品。
			if targetErr != nil || target == nil || target.Purpose != "membership" || strings.TrimSpace(target.Name) == "" {
				return c.ErrCapability
			}
			name := target.Name
			if target.Observation.RouteVersion == target.RouteVersion && strings.TrimSpace(target.Observation.Title) != "" {
				name = target.Observation.Title
			}
			names = append(names, name)
		}
		display = &c.DisplayConfig{SalesOpen: config.Enabled, ProductID: config.ProductID, Version: config.Version, Quantity: config.Quantity, Terms: config.Terms, GroupNames: names}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return display, nil
}

var _ c.DisplayReader = (*Store)(nil)

// checkTargets 使用 tx 绑定的既有 groups 仓储及能力判定，不复制群基础设施。
// ids 须按升序且唯一；now 为可信时钟。返回购买目标值或不可用错误。
// 这里只验证持久化观测，绝不在事务内执行 Telegram 网络探测。
func checkTargets(tx *gorm.DB, ids []uint, now time.Time) ([]d.SnapshotTarget, error) {
	groups := gs.New(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
	service := ga.New(groups, nil)
	service.Now = func() time.Time { return now }
	out := make([]d.SnapshotTarget, 0, len(ids))
	var bot uint
	for _, id := range ids {
		target, err := groups.Get(tx.Statement.Context, id)
		if err != nil {
			return nil, err
		}
		if !validTarget(*target, service, now) {
			return nil, c.ErrCapability
		}
		if bot != 0 && target.BotInstanceID != bot {
			return nil, c.ErrCapability
		}
		bot = target.BotInstanceID
		out = append(out, d.SnapshotTarget{TargetID: target.ID, RouteVersion: target.RouteVersion, BotInstanceID: bot, TelegramChatID: target.TelegramChatID})
	}
	if len(out) == 0 {
		return nil, c.ErrCapability
	}
	return out, nil
}

// validTarget 复用 groups 的 Fresh/Missing 语义并检查会员专属不变量。
// target 是事务内当前值；service 不配置 Probe，因此不会执行网络操作。
func validTarget(target gd.Target, service *ga.Service, now time.Time) bool {
	view := service.View(target)
	return target.RecoveryTaskID == 0 && target.Enabled && target.Purpose == "membership" && target.BotInstanceID != 0 &&
		target.TelegramChatID < 0 && target.RouteVersion > 0 && target.Observation.IsMember &&
		target.Observation.State == "ready" && target.Observation.ObservedAt != nil &&
		!target.Observation.ObservedAt.After(now) && view.Fresh && len(view.MissingCapabilities) == 0
}

// ScanReminders reads a bounded projection page and commits its cursor with unique cycle notices.
// All-source eligibility and binding reads use tx; no contact or platform call occurs here.
func (store *Store) ScanReminders(ctx context.Context, bot uint, limit int, at time.Time) (scanned int, err error) {
	if bot == 0 || limit < 1 || limit > 100 || at.IsZero() {
		return 0, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		scanned = 0
		cursor := d.ReminderCursor{BotID: bot}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error; err != nil {
			return err
		}
		if err := dblock.Row(tx.Model(&cursor).Where("bot_id = ?", bot), "version", c.ErrNotFound); err != nil {
			return err
		}
		if err := tx.First(&cursor, "bot_id = ?", bot).Error; err != nil {
			return err
		}
		var projections []d.LedgerProjection
		query := tx.Model(&d.LedgerProjection{}).Select("telegram_membership_ledger_projections.*").Joins("JOIN telegram_group_targets AS target ON target.id = telegram_membership_ledger_projections.target_id").
			Where("target.bot_instance_id = ? AND target.purpose = ?", bot, "membership").
			Where("target_id > ? OR (target_id = ? AND beneficiary_telegram_id > ?)", cursor.TargetID, cursor.TargetID, cursor.TelegramID)
		if err := query.Order("target_id, beneficiary_telegram_id").Limit(limit).Find(&projections).Error; err != nil {
			return err
		}
		for _, projection := range projections {
			if err := enqueueReminders(tx, projection, at); err != nil {
				return err
			}
			cursor.TargetID, cursor.TelegramID = projection.TargetID, projection.BeneficiaryTelegramID
			scanned++
		}
		if len(projections) < limit {
			cursor.TargetID, cursor.TelegramID = 0, 0
		}
		return tx.Model(&d.ReminderCursor{}).Where("bot_id = ? AND version = ?", bot, cursor.Version).
			Updates(map[string]any{"target_id": cursor.TargetID, "telegram_id": cursor.TelegramID, "version": cursor.Version + 1}).Error
	})
	return scanned, err
}

// enqueueReminders snapshots all three tiers, retaining overdue work until expiry without a short scan window.
// An obsolete binding is recorded as superseded, never silently redirected to a new Telegram identity.
func enqueueReminders(tx *gorm.DB, projection d.LedgerProjection, at time.Time) error {
	eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: projection.BeneficiaryTelegramID}, projection.TargetID, at)
	if errors.Is(err, c.ErrUntrustedIdentity) {
		return nil
	}
	if err != nil {
		return err
	}
	if !eligible.Active || eligible.Kind == d.TermPermanent || eligible.ExpiresAt == nil {
		return nil
	}
	var target gd.Target
	if err := tx.First(&target, projection.TargetID).Error; err != nil {
		return err
	}
	telegram, binding, err := userAccessBindingSnapshot(tx, eligible.AccountID)
	if err != nil && !errors.Is(err, c.ErrUntrustedIdentity) {
		return err
	}
	state := "pending"
	if telegram != projection.BeneficiaryTelegramID || binding == 0 {
		state = "superseded"
	}
	for _, days := range []int{7, 3, 1} {
		expires := eligible.ExpiresAt.UTC()
		row := d.Reminder{BusinessKey: fmt.Sprintf("membership-reminder:%d:%d:%d:%d:%d", projection.TargetID, projection.BeneficiaryTelegramID, expires.UnixNano(), eligible.Version, days), BotID: target.BotInstanceID, TargetID: target.ID, AccountID: eligible.AccountID, TelegramID: projection.BeneficiaryTelegramID, ChatID: target.TelegramChatID, BindingVersion: binding, EntitlementVersion: eligible.Version, RouteVersion: target.RouteVersion, ExpiresAt: expires, Days: days, State: state, NextAttemptAt: d.ReminderAvailable(expires.Add(-time.Duration(days) * 24 * time.Hour)), CreatedAt: at, UpdatedAt: at}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// DueReminders returns bounded fair pending/queued work; queued is not a delivery receipt.
func (store *Store) DueReminders(ctx context.Context, bot uint, limit int, at time.Time) ([]d.Reminder, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	var rows []d.Reminder
	err := store.db.WithContext(ctx).Where("bot_id = ? AND state IN ? AND next_attempt_at <= ?", bot, []string{"pending", "queued"}, at).Order("next_attempt_at, id").Limit(limit).Find(&rows).Error
	return rows, err
}

// Reminder returns the immutable authorization snapshot for the runtime's pre-send guard.
func (store *Store) Reminder(ctx context.Context, id uint) (*d.Reminder, error) {
	var row d.Reminder
	err := store.db.WithContext(ctx).First(&row, id).Error
	return &row, err
}

// FinishReminder stores only classified evidence; delayed retries rotate behind other due work.
func (store *Store) FinishReminder(ctx context.Context, id uint, state string, message int64, at time.Time) error {
	switch state {
	case "pending", "queued", "sent", "failed", "unknown", "unreachable", "superseded":
	default:
		return c.ErrInvalid
	}
	if state == "sent" && message <= 0 {
		return c.ErrInvalid
	}
	return store.db.WithContext(ctx).Model(&d.Reminder{}).Where("id = ? AND state IN ?", id, []string{"pending", "queued"}).Updates(map[string]any{"state": state, "message_id": message, "next_attempt_at": at.Add(time.Minute), "updated_at": at}).Error
}
