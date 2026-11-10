package gormstore

import (
	"context"
	"errors"
	"gorm.io/gorm/clause"
	"log/slog"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	eis "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/infrastructure/gormstore"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// JoinAbuses returns bounded confirmed alien-application evidence using existing rows.
func (store *Store) JoinAbuses(ctx context.Context, bot, after uint, limit int, at time.Time) (out []c.JoinAbuse, err error) {
	if bot == 0 || limit < 1 || limit > 100 || at.IsZero() {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var joins []d.AccessJoin
		query := tx.Where("bot_id = ? AND id > ? AND decision = ? AND state = ? AND received_at >= ? AND received_at <= ?", bot, after, "decline", "declined", at.Add(-24*time.Hour), at)
		if err := query.Order("id").Limit(limit).Find(&joins).Error; err != nil {
			return err
		}
		for _, join := range joins {
			row, readErr := joinAbuseEvidence(tx, join.ID, at)
			if errors.Is(readErr, c.ErrUntrustedIdentity) || errors.Is(readErr, c.ErrConflict) || errors.Is(readErr, gorm.ErrRecordNotFound) {
				out = append(out, c.JoinAbuse{ID: join.ID, BotID: bot}) // Scan-only tombstone preserves progress.
				continue
			}
			if readErr != nil {
				return readErr
			}
			out = append(out, *row)
		}
		return nil
	})
	return out, err
}

// JoinAbuse revalidates one immutable join using only the transaction connection.
func (store *Store) JoinAbuse(ctx context.Context, id uint, at time.Time) (out *c.JoinAbuse, err error) {
	if id == 0 || at.IsZero() {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		out, err = joinAbuseEvidence(tx, id, at)
		return err
	})
	return out, result(err)
}

// joinAbuseEvidence proves an alien applicant, exact original invite and acknowledged
// refusal. Expired rights, retired invitations, rebinding and unknown sends fail closed.
func joinAbuseEvidence(tx *gorm.DB, id uint, at time.Time) (*c.JoinAbuse, error) {
	var join d.AccessJoin
	if err := tx.First(&join, id).Error; err != nil {
		return nil, err
	}
	if join.Decision != "decline" || join.State != "declined" || join.ReceivedAt.After(at) || join.ReceivedAt.Before(at.Add(-24*time.Hour)) {
		return nil, c.ErrUntrustedIdentity
	}
	var invite d.AccessInvite
	if err := tx.First(&invite, join.InviteID).Error; err != nil {
		return nil, err
	}
	if err := projectInviteRetirement(tx, &invite); err != nil {
		return nil, err
	}
	if invite.TelegramID <= 0 || invite.TelegramID == join.TelegramID || invite.AccountID == 0 || invite.BotID != join.BotID || invite.ChatID != join.ChatID || invite.LinkHash == "" || invite.LinkHash != join.LinkHash || invite.State != "active" || !d.IsInviteUsableAt(invite, at) {
		return nil, c.ErrUntrustedIdentity
	}
	var operation d.AccessOperation
	if err := tx.Where("join_id = ? AND invite_id = ? AND kind = ? AND state = ?", join.ID, invite.ID, "decline", c.AccessSucceeded).First(&operation).Error; err != nil {
		return nil, err
	}
	if operation.AccountID != invite.AccountID || operation.TelegramID != join.TelegramID || operation.BotID != invite.BotID || operation.ChatID != invite.ChatID || operation.TargetID != invite.TargetID || operation.RouteVersion != invite.RouteVersion || operation.BindingVersion != invite.BindingVersion || operation.Generation == 0 {
		return nil, c.ErrUntrustedIdentity
	}
	var receipt d.AccessResult
	if err := tx.Where("operation_id = ? AND lease_version = ? AND generation = ? AND outcome = ?", operation.ID, operation.Version-1, operation.Generation, c.AccessSucceeded).First(&receipt).Error; err != nil {
		return nil, err
	}
	identity := c.AccessIdentity{AccountID: invite.AccountID, TelegramID: invite.TelegramID, BotID: invite.BotID}
	for _, observedAt := range []time.Time{at, join.ReceivedAt, receipt.ReceivedAt} {
		eligible, err := accessEligibility(tx, identity, invite.TargetID, observedAt)
		if err != nil {
			return nil, err
		}
		if !eligible.Active || eligible.BindingVersion == 0 || eligible.BindingVersion != invite.BindingVersion || !d.IsInviteUsableAt(invite, observedAt) {
			return nil, c.ErrUntrustedIdentity
		}
	}
	return &c.JoinAbuse{ID: join.ID, InviteID: invite.ID, AccountID: invite.AccountID, BotID: invite.BotID, TargetID: invite.TargetID, TelegramID: invite.TelegramID, ChatID: invite.ChatID, BindingVersion: uint64(invite.BindingVersion), RouteVersion: invite.RouteVersion, Generation: operation.Generation, Window: join.ReceivedAt.UTC().Truncate(time.Hour)}, nil
}

var _ c.JoinAbuseRepository = (*Store)(nil)

// CheckPayment 从实际父子订单项读取历史快照；order 必须由同一事务加载。
// now 为可信时钟；返回会员标记及身份、数量、路由、能力或永久覆盖错误。
func (checkout *checkout) CheckPayment(ctx context.Context, order *od.Order, now time.Time) (bool, error) {
	if order == nil || now.IsZero() {
		return false, c.ErrInvalid
	}
	tx := checkout.store.db.WithContext(ctx)
	var snapshots []d.OrderItemSnapshot
	var collect func(*od.Order) error
	collect = func(current *od.Order) error {
		for _, item := range current.Items {
			snapshot, err := checkout.store.Snapshot(ctx, item.ID)
			if errors.Is(err, c.ErrNotFound) {
				// 当前会员商品不可借缺失快照降级为普通订单。
				var count int64
				if err := tx.Model(&d.ProductConfig{}).Where("product_id = ?", item.ProductID).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return c.ErrInvalid
				}
				continue
			}
			if err != nil {
				return err
			}
			if current.UserID != order.UserID || snapshot.AccountID != order.UserID {
				return c.ErrUntrustedIdentity
			}
			if item.OrderID != current.ID || item.ProductID != snapshot.ProductID || item.SKUID != snapshot.SKUID || item.Quantity != 1 || snapshot.Quantity != 1 {
				return c.ErrInvalid
			}
			snapshots = append(snapshots, *snapshot)
		}
		for index := range current.Children {
			child := &current.Children[index]
			if child.ParentID == nil || *child.ParentID != current.ID {
				return c.ErrInvalid
			}
			if err := collect(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(order); err != nil {
		return len(snapshots) > 0, err
	}
	if len(snapshots) == 0 {
		return false, nil
	}
	// 复用 Checkout 的绑定锁与永久资格端口，但绝不调用 Prepare 重建配置。
	plan := &c.CheckoutPlan{AccountID: order.UserID, Targets: make(map[uint][]d.SnapshotTarget)}
	for _, snapshot := range snapshots {
		if err := checkout.validatePaymentSnapshot(tx, snapshot, now); err != nil {
			return true, err
		}
		binding := ei.TelegramBinding{ID: uint(snapshot.IdentityVersion), TelegramID: snapshot.BeneficiaryTelegramID}
		if plan.Binding.ID != 0 && plan.Binding != binding {
			return true, c.ErrUntrustedIdentity
		}
		plan.Binding = binding
		if _, duplicate := plan.Targets[snapshot.ProductID]; duplicate {
			return true, c.ErrInvalid
		}
		plan.Lines = append(plan.Lines, c.SnapshotInput{ProductID: snapshot.ProductID})
		plan.Targets[snapshot.ProductID] = snapshot.Targets
	}
	return true, checkout.checkOverlap(ctx, plan)
}

// CheckReceipt 检查已收款订单的会员快照完整性，不复核当前身份或群能力。
// order 来自支付事务；普通订单后转会员但无历史快照必须进入收款异常而非发货。
func (checkout *checkout) CheckReceipt(ctx context.Context, order *od.Order) error {
	if order == nil {
		return c.ErrInvalid
	}
	for _, item := range order.Items {
		var count int64
		if err := checkout.store.db.WithContext(ctx).Model(&d.ProductConfig{}).Where("product_id = ?", item.ProductID).Count(&count).Error; err != nil {
			return err
		}
		snapshot, err := checkout.store.Snapshot(ctx, item.ID)
		if errors.Is(err, c.ErrNotFound) && count == 0 {
			continue
		}
		if errors.Is(err, c.ErrNotFound) {
			return c.ErrSalesClosed
		}
		if err != nil {
			return err
		}
		if snapshot.AccountID != order.UserID || snapshot.ProductID != item.ProductID || snapshot.SKUID != item.SKUID || snapshot.Quantity != 1 || item.Quantity != 1 {
			return c.ErrSalesClosed
		}
	}
	for index := range order.Children {
		if err := checkout.CheckReceipt(ctx, &order.Children[index]); err != nil {
			return err
		}
	}
	return nil
}

// validatePaymentSnapshot 复核不可变期限、绑定代次及原目标路由的新鲜能力。
// tx 是支付事务；历史期限不重建，当前商品和配置必须仍允许主动付款。
func (checkout *checkout) validatePaymentSnapshot(tx *gorm.DB, snapshot d.OrderItemSnapshot, now time.Time) error {
	if _, err := c.IdentityFromVerifiedBinding(snapshot.AccountID, snapshot.BeneficiaryTelegramID, snapshot.IdentityVersion); err != nil {
		return err
	}
	seconds, err := d.DurationSeconds(snapshot.Kind, snapshot.Months)
	if err != nil || seconds != snapshot.DurationSeconds || snapshot.ConfigVersion == 0 {
		return c.ErrInvalid
	}
	if err := eis.New(tx).LockTelegramBinding(snapshot.AccountID, ei.TelegramBinding{ID: uint(snapshot.IdentityVersion), TelegramID: snapshot.BeneficiaryTelegramID}); err != nil {
		return err
	}
	if err := checkSaleProduct(tx, snapshot.ProductID); err != nil {
		return result(err)
	}
	config, err := readConfig(tx, snapshot.ProductID)
	if err != nil {
		return result(err)
	}
	if !config.Enabled {
		return c.ErrSalesClosed
	}
	ids := make([]uint, 0, len(snapshot.Targets))
	seen := make(map[uint]bool)
	for _, target := range snapshot.Targets {
		if target.TargetID == 0 || seen[target.TargetID] {
			return c.ErrInvalid
		}
		seen[target.TargetID] = true
		ids = append(ids, target.TargetID)
	}
	targets, err := checkTargets(tx, ids, now)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(targets, snapshot.Targets) {
		return c.ErrCapability
	}
	return nil
}

// Snapshot 按订单项 id 读取历史快照；ctx 控制取消，读取不依赖现有商品或群配置。
func (store *Store) Snapshot(ctx context.Context, id uint) (*d.OrderItemSnapshot, error) {
	if id == 0 {
		return nil, c.ErrInvalid
	}
	var snapshot d.OrderItemSnapshot
	if err := store.db.WithContext(ctx).First(&snapshot, "order_item_id = ?", id).Error; err != nil {
		return nil, result(err)
	}
	return &snapshot, nil
}

// InsertSnapshot 在绑定的订单事务内从当前定义构造不可变快照，不接受 HTTP 身份字段。
// input/identity 仅为内部引用；本方法仍在同一事务复核真实绑定、订单项及父子单归属。
// 此方法不授权收款；永久重叠由 checkout 窄端口控制，公共销售门控保持关闭。
func (store *Store) InsertSnapshot(ctx context.Context, input c.SnapshotInput, identity c.TrustedIdentity, now time.Time) (snapshot *d.OrderItemSnapshot, err error) {
	if !store.boundTransaction {
		return nil, c.ErrTransactionRequired
	}
	account, telegram, version, err := identity.Values()
	if err != nil {
		return nil, err
	}
	if input.OrderItemID == 0 || input.ProductID == 0 || input.SKUID == 0 || input.ConfigVersion == 0 || input.Quantity != 1 || now.IsZero() {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		if err := eis.New(tx).LockTelegramBinding(account, ei.TelegramBinding{ID: uint(version), TelegramID: telegram}); err != nil {
			return err
		}
		if err := validateSnapshotItem(tx, input, account); err != nil {
			return err
		}
		if err := checkProduct(tx, input.ProductID); err != nil {
			return err
		}
		config, err := readConfig(tx, input.ProductID)
		if err != nil {
			return err
		}
		if config.Version != input.ConfigVersion {
			return c.ErrConflict
		}
		term, err := snapshotTerm(config, input.SKUID)
		if err != nil {
			return err
		}
		if err := checkSKUs(tx, input.ProductID, []d.SKUTerm{term}); err != nil {
			return err
		}
		ids := make([]uint, 0, len(config.Targets))
		for _, target := range config.Targets {
			ids = append(ids, target.TargetID)
		}
		targets, err := checkTargets(tx, ids, now.UTC())
		if err != nil {
			return err
		}
		seconds, err := d.DurationSeconds(term.Kind, term.Months)
		if err != nil {
			return err
		}
		snapshot = &d.OrderItemSnapshot{OrderItemID: input.OrderItemID, ProductID: input.ProductID, SKUID: input.SKUID,
			AccountID: account, BeneficiaryTelegramID: telegram, IdentityVersion: version, ConfigVersion: config.Version,
			Kind: term.Kind, Months: term.Months, DurationSeconds: seconds, Quantity: 1, Targets: targets, CreatedAt: now.UTC()}
		return tx.Create(snapshot).Error
	})
	if err != nil {
		return nil, err
	}
	// 外部事务可能仍会回滚，因此日志明确记录 staged 而非 committed。
	slog.InfoContext(ctx, "membership snapshot staged in caller transaction", "order_item_id", input.OrderItemID, "config_version", input.ConfigVersion)
	return snapshot, nil
}

// snapshotTerm 从 config 查找原 SKU id 的固定期限；返回零值和错误表示不可快照。
func snapshotTerm(config *d.ProductConfig, id uint) (d.SKUTerm, error) {
	if config.Quantity != 1 {
		return d.SKUTerm{}, c.ErrInvalid
	}
	for _, term := range config.Terms {
		if term.SKUID == id {
			return term, nil
		}
	}
	return d.SKUTerm{}, c.ErrNotFound
}

// validateSnapshotItem 核验已持久化 item 的订单、受益账号、SKU 和数量，禁止跨单快照。
// tx 必须属于外部订单事务；返回关联不符或数据库错误。
func validateSnapshotItem(tx *gorm.DB, input c.SnapshotInput, account uint) error {
	var item od.OrderItem
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("deleted_at IS NULL").First(&item, input.OrderItemID).Error; err != nil {
		return err
	}
	if input.OrderID == 0 || item.OrderID != input.OrderID || item.ProductID != input.ProductID || item.SKUID != input.SKUID || item.Quantity != 1 {
		return c.ErrInvalid
	}
	var order od.Order
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", item.OrderID).First(&order).Error; err != nil {
		return err
	}
	if order.UserID != account {
		return c.ErrUntrustedIdentity
	}
	if order.ParentID != nil {
		var parent od.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", *order.ParentID).First(&parent).Error; err != nil {
			return err
		}
		if parent.UserID != account || parent.ParentID != nil {
			return c.ErrInvalid
		}
	}
	return nil
}

// checkout 始终使用订单 tx；overlap 尚未装配时不允许产生可收款的会员订单。
type checkout struct {
	store   *Store
	overlap c.PermanentOverlapGuard
}

// CheckoutInTransaction 为已打开的 tx 构造共享预检与快照端口；不打开独立连接。
// 缺失 overlap 返回的端口对会员 fail-closed，生产用 CheckoutWithLedgerInTransaction。
func CheckoutInTransaction(tx *gorm.DB, overlap c.PermanentOverlapGuard) (c.Checkout, error) {
	store, err := InTransaction(tx)
	if err != nil {
		return nil, err
	}
	return &checkout{store: store, overlap: overlap}, nil
}

// Prepare 从可信数据库解析账号绑定和商品配置；lines 只能包含商品/SKU/数量引用。
// 重复商品行（包括不同 SKU）不能绕过每单一份，空结果表示全部为普通商品。
func (checkout *checkout) Prepare(ctx context.Context, account uint, lines []c.CheckoutLine, now time.Time) (*c.CheckoutPlan, error) {
	tx := checkout.store.db.WithContext(ctx)
	plan := &c.CheckoutPlan{AccountID: account, Targets: make(map[uint][]d.SnapshotTarget)}
	lines = append([]c.CheckoutLine(nil), lines...)
	sort.Slice(lines, func(left, right int) bool { return lines[left].ProductID < lines[right].ProductID })
	seen := make(map[uint]bool)
	// 先只识别会员商品，再按身份→账号→商品→配置→SKU→群的顺序加锁。
	for _, line := range lines {
		var root d.ProductConfig
		err := tx.First(&root, "product_id = ?", line.ProductID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if seen[line.ProductID] || line.Quantity != 1 || line.SKUID == 0 {
			return nil, c.ErrInvalid
		}
		seen[line.ProductID] = true
		plan.Lines = append(plan.Lines, c.SnapshotInput{ProductID: line.ProductID, SKUID: line.SKUID, Quantity: 1, ConfigVersion: root.Version})
	}
	if len(plan.Lines) == 0 {
		return nil, nil
	}
	identity, err := eis.New(tx).GetByUserProvider(account, constants.UserOAuthProviderTelegram)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, c.ErrUntrustedIdentity
	}
	telegram, err := strconv.ParseInt(identity.ProviderUserID, 10, 64)
	if err != nil || telegram <= 0 {
		return nil, c.ErrUntrustedIdentity
	}
	plan.Binding = ei.TelegramBinding{ID: identity.ID, TelegramID: telegram}
	if err := eis.New(tx).LockTelegramBinding(account, plan.Binding); err != nil {
		return nil, err
	}
	for _, line := range plan.Lines {
		targets, err := checkout.validateLine(tx, line, now)
		if err != nil {
			return nil, err
		}
		plan.Targets[line.ProductID] = targets
	}
	if err := checkout.checkOverlap(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// validateLine 在 tx 内锁定并复核配置版本、真实 SKU 归属和当前目标能力，返回目标值。
func (checkout *checkout) validateLine(tx *gorm.DB, line c.SnapshotInput, now time.Time) ([]d.SnapshotTarget, error) {
	if err := checkProduct(tx, line.ProductID); err != nil {
		return nil, err
	}
	config, err := readConfig(tx, line.ProductID)
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, c.ErrSalesClosed
	}
	if err := checkSaleProduct(tx, line.ProductID); err != nil {
		return nil, err
	}
	if config.Version != line.ConfigVersion {
		return nil, c.ErrConflict
	}
	term, err := snapshotTerm(config, line.SKUID)
	if err != nil {
		return nil, err
	}
	if _, err := d.DurationSeconds(term.Kind, term.Months); err != nil {
		return nil, err
	}
	if err := checkSKUs(tx, line.ProductID, []d.SKUTerm{term}); err != nil {
		return nil, err
	}
	var sku product.ProductSKU
	if err := tx.Where("id = ? AND product_id = ? AND deleted_at IS NULL AND is_active = ?", line.SKUID, line.ProductID, true).First(&sku).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(config.Targets))
	for _, target := range config.Targets {
		ids = append(ids, target.TargetID)
	}
	return checkTargets(tx, ids, now)
}

// checkOverlap 调用同事务账本窄端口；未装配时明确拒绝，而非假装无永久资格。
func (checkout *checkout) checkOverlap(ctx context.Context, plan *c.CheckoutPlan) error {
	if checkout.overlap == nil {
		return c.ErrOverlapUnavailable
	}
	for _, line := range plan.Lines {
		if err := checkout.overlap.CheckPermanentOverlap(ctx, plan.AccountID, plan.Binding.TelegramID, plan.Targets[line.ProductID]); err != nil {
			return err
		}
	}
	return nil
}

// Revalidate 在订单写入前重新验证预检计划；身份/版本/路由变化一律回滚。
func (checkout *checkout) Revalidate(ctx context.Context, plan *c.CheckoutPlan, now time.Time) error {
	if plan == nil {
		return nil
	}
	tx := checkout.store.db.WithContext(ctx)
	if err := eis.New(tx).LockTelegramBinding(plan.AccountID, plan.Binding); err != nil {
		return err
	}
	for _, line := range plan.Lines {
		targets, err := checkout.validateLine(tx, line, now)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(targets, plan.Targets[line.ProductID]) {
			return c.ErrConflict
		}
	}
	return checkout.checkOverlap(ctx, plan)
}

// SaveItems 为真实持久化订单项逐一写不可变快照，禁止遗漏、重复或替换会员 SKU。
// items 包括全部子单项，不为无实际项的父单产生第二份快照。
func (checkout *checkout) SaveItems(ctx context.Context, plan *c.CheckoutPlan, items []od.OrderItem, now time.Time) error {
	if plan == nil {
		return nil
	}
	if err := checkout.Revalidate(ctx, plan, now); err != nil {
		return err
	}
	identity, err := c.IdentityFromVerifiedBinding(plan.AccountID, plan.Binding.TelegramID, uint64(plan.Binding.ID))
	if err != nil {
		return err
	}
	matched := make(map[uint]bool)
	for _, item := range items {
		for _, line := range plan.Lines {
			if line.ProductID != item.ProductID {
				continue
			}
			if matched[line.ProductID] || line.SKUID != item.SKUID || item.Quantity != 1 {
				return c.ErrInvalid
			}
			matched[line.ProductID] = true
			line.OrderID = item.OrderID
			line.OrderItemID = item.ID
			if _, err := checkout.store.InsertSnapshot(ctx, line, identity, now); err != nil {
				return err
			}
		}
	}
	if len(matched) != len(plan.Lines) {
		return c.ErrInvalid
	}
	return nil
}
