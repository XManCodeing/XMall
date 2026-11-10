package gormstore

import (
	"context"
	"errors"
	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	order "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"github.com/XManCodeing/XMall/internal/shared/dberror"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 只持有数据库连接；不接受 HTTP/Telegram 客户端。
type Store struct {
	db               *gorm.DB
	boundTransaction bool
}

// New 由 db 构造配置读写仓储；订单快照写入必须另用 InTransaction。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// InTransaction 接受调用方正在执行的实际 tx，返回共享事务的窄仓储。
// 不开启或提交事务；禁止传入全局连接，错误返回 ErrTransactionRequired。
func InTransaction(tx *gorm.DB) (*Store, error) {
	if tx == nil || tx.Statement == nil || tx.Config.DisableNestedTransaction {
		return nil, c.ErrTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, c.ErrTransactionRequired
	}
	return &Store{db: tx, boundTransaction: true}, nil
}

// transaction 在 ctx 下执行数据库回调 fn；绑定事务时仅使用同连接保存点。
// 保存点保证单次调用失败也完整回滚，不要求外部调用者以吞错后的半成品继续。
func (store *Store) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return result(store.db.WithContext(ctx).Transaction(fn))
}

// result 将数据库缺失/唯一冲突 err 转为稳定合同错误，其余原样返回。
func result(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || dberror.IsUniqueViolation(err) {
		return c.ErrConflict
	}
	return err
}

// Config 在单一事务内读取 productID 的整体配置；返回独立值，避免半新半旧集合。
func (store *Store) Config(ctx context.Context, productID uint) (config *d.ProductConfig, err error) {
	if productID == 0 {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		config, err = readConfig(tx, productID)
		return err
	})
	return config, err
}

// readConfig 从 tx 锁定 productID 的聚合根，按稳定 ID 顺序读取关联集合。
func readConfig(tx *gorm.DB, productID uint) (*d.ProductConfig, error) {
	var config d.ProductConfig
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&config, "product_id = ?", productID).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("product_id = ?", productID).Order("target_id").Find(&config.Targets).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("product_id = ?", productID).Order("sku_id").Find(&config.Terms).Error; err != nil {
		return nil, err
	}
	return &config, nil
}

// SaveConfig 校验 input 并使用 now 的 UTC 时点原子保存；返回新版本或完整回滚错误。
// 锁顺序为商品、配置根、升序 SKU、升序群目标；仅访问当前 tx。
func (store *Store) SaveConfig(ctx context.Context, input c.ConfigInput, now time.Time) (saved *d.ProductConfig, err error) {
	if err = validateInput(input, now); err != nil {
		return nil, err
	}
	input.TargetIDs = append([]uint(nil), input.TargetIDs...)
	input.Terms = append([]d.SKUTerm(nil), input.Terms...)
	sort.Slice(input.TargetIDs, func(left, right int) bool { return input.TargetIDs[left] < input.TargetIDs[right] })
	sort.Slice(input.Terms, func(left, right int) bool { return input.Terms[left].SKUID < input.Terms[right].SKUID })
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		if err := checkProduct(tx, input.ProductID); err != nil {
			return err
		}
		if err := checkConfigState(tx, input); err != nil {
			return err
		}
		if err := saveRoot(tx, input, now.UTC()); err != nil {
			return err
		}
		if err := checkSKUs(tx, input.ProductID, input.Terms); err != nil {
			return err
		}
		if _, err := checkTargets(tx, input.TargetIDs, now.UTC()); err != nil {
			return err
		}
		if err := replaceChildren(tx, input); err != nil {
			return err
		}
		saved, err = readConfig(tx, input.ProductID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// validateInput 对 input 的集合、固定数量、期限、版本及 now 做无副作用边界验证。
func validateInput(input c.ConfigInput, now time.Time) error {
	if input.Enabled && input.ExpectedVersion == 0 {
		return c.ErrSalesClosed
	}
	if input.ProductID == 0 || input.Quantity != 1 || len(input.TargetIDs) == 0 || len(input.Terms) == 0 || input.ExpectedVersion >= math.MaxInt64 || now.IsZero() {
		return c.ErrInvalid
	}
	targets := make(map[uint]bool, len(input.TargetIDs))
	for _, target := range input.TargetIDs {
		if target == 0 || targets[target] {
			return c.ErrInvalid
		}
		targets[target] = true
	}
	skus := make(map[uint]bool, len(input.Terms))
	for _, term := range input.Terms {
		if term.SKUID == 0 || skus[term.SKUID] || (term.ProductID != 0 && term.ProductID != input.ProductID) {
			return c.ErrInvalid
		}
		if _, err := d.DurationSeconds(term.Kind, term.Months); err != nil {
			return err
		}
		skus[term.SKUID] = true
	}
	return nil
}

// checkProduct 在 tx 锁定原商品 id，拒绝已删除或上游映射商品，不复制商品数据。
func checkProduct(tx *gorm.DB, id uint) error {
	var product p.Product
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).First(&product).Error; err != nil {
		return err
	}
	if product.IsMapped {
		return c.ErrInvalid
	}
	return nil
}

// checkSKUs 在 tx 按主键批量锁定并验证 terms 全部属于 productID，减少逐 SKU 往返。
// terms 已通过去重校验；缺失、跨商品或软删除 SKU 均返回记录不存在错误。
func checkSKUs(tx *gorm.DB, productID uint, terms []d.SKUTerm) error {
	const batchSize = 100
	for start := 0; start < len(terms); start += batchSize {
		end := min(start+batchSize, len(terms))
		ids := make([]uint, 0, end-start)
		for _, term := range terms[start:end] {
			ids = append(ids, term.SKUID)
		}
		var skus []p.ProductSKU
		if err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ? AND product_id = ? AND deleted_at IS NULL", ids, productID).
			Order("id").Find(&skus).Error; err != nil {
			return err
		}
		if len(skus) != len(ids) {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

// saveRoot 在 tx 使用 input.ExpectedVersion CAS 根记录，绝不隐式覆盖旧版本。
func saveRoot(tx *gorm.DB, input c.ConfigInput, now time.Time) error {
	if input.ExpectedVersion == 0 {
		if err := tx.Create(&d.ProductConfig{ProductID: input.ProductID, Version: 1, Quantity: 1, UpdatedAt: now}).Error; err != nil {
			return err
		}
		_, err := ensureShareCode(tx, input.ProductID)
		return err
	}
	updated := tx.Model(&d.ProductConfig{}).Where("product_id = ? AND version = ?", input.ProductID, input.ExpectedVersion).
		Updates(map[string]any{"version": input.ExpectedVersion + 1, "enabled": input.Enabled, "quantity": 1, "updated_at": now})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// replaceChildren 在已成功 CAS 的 tx 中替换 input 的当前定义；不访问订单快照。
func replaceChildren(tx *gorm.DB, input c.ConfigInput) error {
	if err := tx.Where("product_id = ?", input.ProductID).Delete(&d.ProductTarget{}).Error; err != nil {
		return err
	}
	if err := tx.Where("product_id = ?", input.ProductID).Delete(&d.SKUTerm{}).Error; err != nil {
		return err
	}
	// 同一事务中按固定顺序分批写入，降低远程数据库往返；任一批失败仍整体回滚。
	targets := make([]d.ProductTarget, 0, len(input.TargetIDs))
	for _, id := range input.TargetIDs {
		targets = append(targets, d.ProductTarget{ProductID: input.ProductID, TargetID: id})
	}
	if err := tx.CreateInBatches(&targets, 100).Error; err != nil {
		return err
	}
	terms := append([]d.SKUTerm(nil), input.Terms...)
	for index := range terms {
		terms[index].ProductID = input.ProductID
	}
	return tx.CreateInBatches(&terms, 100).Error
}

// checkConfigState validates input against the locked root; edits cannot change sale state.
// tx is the product transaction. Creation is inactive; stale versions or toggles return conflict.
func checkConfigState(tx *gorm.DB, input c.ConfigInput) error {
	if input.ExpectedVersion == 0 {
		return nil
	}
	config, err := readConfig(tx, input.ProductID)
	if err != nil {
		return err
	}
	if config.Version != input.ExpectedVersion || config.Enabled != input.Enabled {
		return c.ErrConflict
	}
	if input.Enabled {
		return checkSaleProduct(tx, input.ProductID)
	}
	return nil
}

// checkSaleProduct locks id and its category in tx, rejecting inactive or missing rows.
// It returns a closed-sale error rather than allowing orphaned historical membership products.
func checkSaleProduct(tx *gorm.DB, id uint) error {
	var row p.Product
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
		return err
	}
	if !row.IsActive || row.IsMapped {
		return c.ErrSalesClosed
	}
	return checkSaleCategory(tx, row.CategoryID)
}

// checkSaleCategory locks categoryID in tx and requires an active, undeleted category.
// Invalid assignments return ErrInvalid; database failures are propagated.
func checkSaleCategory(tx *gorm.DB, categoryID uint) error {
	if categoryID == 0 {
		return c.ErrInvalid
	}
	var row category.Category
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ? AND deleted_at IS NULL", categoryID).Error; err != nil {
		return err
	}
	if !row.IsActive {
		return c.ErrInvalid
	}
	return nil
}

// validateActivation checks the entire locked config and all live SKUs before activation.
// tx is the product transaction; now is trusted UTC time. No remote operation is performed.
func validateActivation(tx *gorm.DB, config *d.ProductConfig, now time.Time) error {
	input := c.ConfigInput{ProductID: config.ProductID, ExpectedVersion: config.Version, Quantity: config.Quantity, Terms: config.Terms}
	for _, target := range config.Targets {
		input.TargetIDs = append(input.TargetIDs, target.TargetID)
	}
	if err := validateInput(input, now); err != nil {
		return err
	}
	if err := checkSKUs(tx, config.ProductID, input.Terms); err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&p.ProductSKU{}).Where("product_id = ? AND deleted_at IS NULL", config.ProductID).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(input.Terms)) {
		return c.ErrInvalid
	}
	var row p.Product
	if err := tx.First(&row, config.ProductID).Error; err != nil {
		return err
	}
	if err := checkSaleCategory(tx, row.CategoryID); err != nil {
		return err
	}
	_, err := checkTargets(tx, input.TargetIDs, now)
	return err
}

var _ c.Store = (*Store)(nil)
var _ c.SnapshotWriter = (*Store)(nil)

// SaveProductConfig 在已绑定的商品事务内解析 input 的 SKU，完整保存 productID 权益。
// 不访问网络；任一缺失、重复、不一致期限或权限错误均返回并交由商品事务回滚。
func (store *Store) SaveProductConfig(ctx context.Context, productID uint, input c.AdminConfigInput) error {
	if !store.boundTransaction {
		return c.ErrTransactionRequired
	}
	var skus []p.ProductSKU
	if err := store.db.WithContext(ctx).Where("product_id = ? AND deleted_at IS NULL", productID).Find(&skus).Error; err != nil {
		return err
	}
	if len(skus) != len(input.Terms) {
		return c.ErrInvalid
	}
	terms := make([]d.SKUTerm, 0, len(input.Terms))
	for _, term := range input.Terms {
		var matched *p.ProductSKU
		for index := range skus {
			sku := &skus[index]
			if (term.SKUID != 0 && sku.ID == term.SKUID) || (term.SKUID == 0 && sku.SKUCode == strings.TrimSpace(term.SKUCode)) {
				matched = sku
				break
			}
		}
		if matched == nil || (term.SKUCode != "" && matched.SKUCode != strings.TrimSpace(term.SKUCode)) {
			return c.ErrInvalid
		}
		terms = append(terms, d.SKUTerm{SKUID: matched.ID, Kind: term.Kind, Months: term.Months})
	}
	_, err := store.SaveConfig(ctx, c.ConfigInput{ProductID: productID, ExpectedVersion: input.ExpectedVersion, Enabled: input.Enabled, Quantity: input.Quantity, TargetIDs: input.TargetIDs, Terms: terms}, time.Now().UTC())
	if errors.Is(err, d.ErrInvalidTerm) {
		return c.ErrInvalid
	}
	return err
}

// PrepareProductOperation 在商品事务内按商品→配置→SKU加锁并验证会员版本。
// active 校验完整当前配置；下架不要求群能力新鲜，删除仍保护库存及历史快照。
func (store *Store) PrepareProductOperation(ctx context.Context, id uint, version uint64, active, deleting bool) error {
	if !store.boundTransaction {
		return c.ErrTransactionRequired
	}
	if id == 0 || version == 0 || version >= math.MaxInt64 {
		return c.ErrInvalid
	}
	tx := store.db.WithContext(ctx)
	if err := checkProduct(tx, id); err != nil {
		return result(err)
	}
	config, err := readConfig(tx, id)
	if err != nil {
		return result(err)
	}
	if config.Version != version {
		return c.ErrConflict
	}
	if active {
		if err := validateActivation(tx, config, time.Now().UTC()); err != nil {
			return result(err)
		}
	}
	if deleting {
		if err := protectProductDeletion(tx, id); err != nil {
			return err
		}
		// 清理入口不导出，只有锁内完成会员/CAS/历史保护后才能执行；外层失败仍整体回滚。
		return store.deleteProductConfig(ctx, id)
	}
	return saveRoot(tx, c.ConfigInput{ProductID: id, ExpectedVersion: version, Enabled: active}, time.Now().UTC())
}

// protectProductDeletion 检查已锁定 id 的所有实际库存与不可变购买历史。
// tx 为商品事务；无限库存标记不是实物库存，正库存、占用和已售均阻止删除。
func protectProductDeletion(tx *gorm.DB, id uint) error {
	var row p.Product
	if err := tx.First(&row, "id = ?", id).Error; err != nil {
		return err
	}
	var skus []p.ProductSKU
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("product_id = ?", id).Order("id").Find(&skus).Error; err != nil {
		return err
	}
	if row.ManualStockTotal > 0 || row.ManualStockLocked > 0 || row.ManualStockSold > 0 {
		return productcontract.ErrProductHasStock
	}
	for _, sku := range skus {
		if sku.ManualStockTotal > 0 || sku.ManualStockLocked > 0 || sku.ManualStockSold > 0 {
			return productcontract.ErrProductHasStock
		}
	}
	// 不过滤订单状态或软删除，退款/取消不抹除购买事实；快照独立保护已授予权益。
	for _, model := range []any{&order.OrderItem{}, &d.OrderItemSnapshot{}} {
		var count int64
		if err := tx.Unscoped().Model(model).Where("product_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return productcontract.ErrProductHasOrderRecord
		}
	}
	return nil
}

// deleteProductConfig 清理 id 的当前配置、群关联及期限；调用方先完成锁定/CAS与历史保护。
// 仅接受绑定事务，失败由原商品级联删除事务整体回滚；不删除任何账本、快照或访问任务。
func (store *Store) deleteProductConfig(ctx context.Context, id uint) error {
	if !store.boundTransaction {
		return c.ErrTransactionRequired
	}
	for _, model := range []any{&d.ProductTarget{}, &d.SKUTerm{}, &d.ProductConfig{}} {
		if err := store.db.WithContext(ctx).Where("product_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	return nil
}

var _ c.AdminOperations = (*Store)(nil)

var _ c.AdminLedgerReader = (*Store)(nil)

// OrderLedger reads immutable sources for authorized itemIDs, never live product or Telegram settings.
// ctx propagates cancellation; empty input returns no rows. Errors carry no database diagnostics.
func (store *Store) OrderLedger(ctx context.Context, itemIDs []uint) ([]c.AdminItemLedger, error) {
	result := make([]c.AdminItemLedger, 0, len(itemIDs))
	if len(itemIDs) == 0 {
		return result, nil
	}
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := make(map[uint]bool)
		for _, id := range itemIDs {
			if id == 0 {
				return c.ErrInvalid
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			row, err := adminItemLedger(tx, id)
			if err != nil {
				return err
			}
			if row != nil {
				result = append(result, *row)
			}
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "membership admin ledger read failed", "item_count", len(itemIDs))
		return nil, errors.New("membership ledger read failed")
	}
	return result, nil
}

// adminItemLedger returns one item's actual ledger and aggregate scopes; no snapshot means no enrichment.
// tx is the read transaction and id must already belong to an authorized order detail.
func adminItemLedger(tx *gorm.DB, id uint) (*c.AdminItemLedger, error) {
	var snapshot d.OrderItemSnapshot
	if err := tx.First(&snapshot, "order_item_id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	row := &c.AdminItemLedger{ItemID: id, State: "pending", Sources: []c.AdminLedgerSource{}, Effective: []c.AdminLedgerEffective{}}
	var sources []d.LedgerSource
	if err := tx.Where("order_item_id = ?", id).Order("id").Find(&sources).Error; err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		state, err := adminPostingState(tx, id)
		row.State = state
		return row, err
	}
	row.State = "recorded"
	allowed := make(map[uint]bool)
	for _, target := range snapshot.Targets {
		allowed[target.TargetID] = true
	}
	seen := make(map[uint]bool)
	for _, source := range sources {
		if !allowed[source.TargetID] || source.BeneficiaryTelegramID != snapshot.BeneficiaryTelegramID || source.AccountID != snapshot.AccountID {
			return nil, c.ErrInvalid
		}
		var revoked int64
		if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ? AND target_id = ?", id, source.TargetID).Count(&revoked).Error; err != nil {
			return nil, err
		}
		status := "recorded"
		if revoked > 0 {
			status = "revoked"
		}
		row.Sources = append(row.Sources, c.AdminLedgerSource{TargetID: source.TargetID, BeneficiaryTelegramID: strconv.FormatInt(source.BeneficiaryTelegramID, 10), Kind: source.Kind, Months: source.Months, PaidAt: source.PaidAt, Status: status})
		if seen[source.TargetID] {
			continue
		}
		seen[source.TargetID] = true
		effective, err := adminEffective(tx, source)
		if err != nil {
			return nil, err
		}
		if effective != nil {
			row.Effective = append(row.Effective, *effective)
		}
	}
	return row, nil
}

// adminPostingState maps the persisted payment outbox for an actual item/root to safe UI states.
// An attempt without completion is failed; no event remains pending, never fabricated as recorded.
func adminPostingState(tx *gorm.DB, itemID uint) (string, error) {
	var events []d.PaidEvent
	err := tx.Model(&d.PaidEvent{}).Joins("JOIN orders ON telegram_membership_paid_events.order_id = COALESCE(orders.parent_id, orders.id)").Joins("JOIN order_items ON order_items.order_id = orders.id").Where("order_items.id = ?", itemID).Find(&events).Error
	if err != nil {
		return "", err
	}
	for _, event := range events {
		if event.CompletedAt == nil && event.Attempts > 0 && event.ClaimOwner == "" {
			return "failed", nil
		}
	}
	return "pending", nil
}

// adminEffective reads the persisted aggregate and recovery queue for source's exact beneficiary/target.
// Permanent DTOs deliberately omit dates, even when the limited contribution has a separate expiry.
func adminEffective(tx *gorm.DB, source d.LedgerSource) (*c.AdminLedgerEffective, error) {
	var projection d.LedgerProjection
	err := tx.First(&projection, "beneficiary_telegram_id = ? AND target_id = ?", source.BeneficiaryTelegramID, source.TargetID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending int64
	if err := tx.Model(&d.LedgerOutbox{}).Where("beneficiary_telegram_id = ? AND target_id = ? AND state = ?", source.BeneficiaryTelegramID, source.TargetID, "pending").Count(&pending).Error; err != nil {
		return nil, err
	}
	result := &c.AdminLedgerEffective{TargetID: source.TargetID, BeneficiaryTelegramID: strconv.FormatInt(source.BeneficiaryTelegramID, 10), Permanent: projection.Active && projection.Kind == "permanent", PendingRestore: pending > 0}
	if !result.Permanent {
		result.ExpiresAt = projection.ExpiresAt
	}
	return result, nil
}

// CountAccessAttention counts abnormal admission operations across every bot.
// Lifecycle work belongs to the independent removal list and its attention count.
func (store *Store) CountAccessAttention(ctx context.Context) (int64, error) {
	var count int64
	err := store.db.WithContext(ctx).Model(&d.AccessOperation{}).
		Where("kind <> ? AND state IN ?", "lifecycle", []string{"failed", "unknown", "needs_review", "retryable"}).Count(&count).Error
	return count, err
}

// CountReviewAttention counts only still-unreviewed inbox applications across bots.
// Manual review preserves needs_review as evidence, so reviewed_at must also be absent.
func (store *Store) CountReviewAttention(ctx context.Context) (int64, error) {
	var count int64
	err := store.db.WithContext(ctx).Model(&d.AccessJoin{}).
		Where("state = ? AND reviewed_at IS NULL", "needs_review").Count(&count).Error
	return count, err
}

// AccessDeliveryCandidates returns a bounded secret-free page for runtime receipt lookup.
// Only successful create operations with a current active, unexpired invitation qualify;
// failed operations are already counted and retired invitations are not actionable delivery failures.
func (store *Store) AccessDeliveryCandidates(ctx context.Context, after uint, limit int) ([]c.AccessTask, error) {
	if limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	rows := []c.AccessTask{}
	err := store.db.WithContext(ctx).Table("telegram_membership_access_operations AS op").
		Select("op.id, op.bot_id, op.invite_id").
		Joins("JOIN telegram_membership_access_invites inv ON inv.id = op.invite_id AND inv.bot_id = op.bot_id").
		Joins("JOIN telegram_membership_access_slots slot ON slot.invite_id = inv.id AND slot.telegram_id = inv.telegram_id AND slot.target_id = inv.target_id").
		Where("op.id > ? AND op.kind = ? AND op.state = ? AND inv.state = ? AND (inv.term = 'long_term' OR inv.expires_at > ?)", after, "create", "succeeded", "active", time.Now().UTC()).
		Order("op.id").Limit(limit).Scan(&rows).Error
	return rows, err
}

// LifecycleAttention counts durable unresolved phases across bots without recalculating frozen
// entitlement projections or treating retained historical outcomes on succeeded tasks as failures.
func (store *Store) LifecycleAttention(ctx context.Context) (c.AttentionSummary, error) {
	var summary c.AttentionSummary
	states := []string{"failed", "retryable", "needs_review", "unknown"}
	// Cleanup liabilities remain actionable even after the independent member operation succeeded.
	// EXISTS avoids counting one lifecycle task multiple times when several historical links failed.
	cleanup := store.db.WithContext(ctx).Table("telegram_membership_invite_cleanup_items AS item").
		Joins("LEFT JOIN telegram_membership_access_operations AS cleanup_op ON cleanup_op.id = item.operation_id").
		Where("item.cycle_id = telegram_membership_access_operations.cleanup_cycle_id").
		Where("item.state IN ? OR (item.state <> ? AND cleanup_op.state IN ?)", states, "succeeded", states).Select("1")
	err := store.db.WithContext(ctx).Model(&d.AccessOperation{}).
		Where("kind = ?", "lifecycle").
		Where("(state <> ? AND (state IN ? OR lifecycle_phase IN ? OR ban_outcome IN ? OR unban_outcome IN ?)) OR EXISTS (?)", "succeeded", states, []string{"unknown_ban", "unknown_unban"}, states, states, cleanup).
		Count(&summary.AttentionCount).Error
	return summary, err
}

var _ c.AccessAttentionRepository = (*Store)(nil)
var _ c.LifecycleAttentionReader = (*Store)(nil)

// accessTaskQuery selects only public fields and joins immutable task-owned provenance.
// Missing/mismatched evidence leaves order_no empty: no current identity or latest-order lookup.
func accessTaskQuery(tx *gorm.DB) *gorm.DB {
	titles := tx.Session(&gorm.Session{NewDB: true}).Model(&groups.TargetTitle{}).
		Select("id, name, bot_instance_id, telegram_chat_id, route_version, obs_title")
	return tx.Table("telegram_membership_access_operations AS op").
		Select("op.id, op.parent_operation_id, op.target_id, op.bot_id, op.chat_id, op.telegram_id, op.route_version, op.state, op.attempt, op.next_attempt_at, op.error_class, op.review_reason, op.member_status, op.member_is_member, op.observed_at, op.entitlement_version, op.kind, op.invite_id, op.join_id, op.account_id, op.version, op.binding_version, op.last_attempt_at, op.updated_at, inv.source_id, inv.expires_at, COALESCE(purchase.order_no, '') AS order_no, CASE WHEN purchase.id IS NOT NULL AND snapshot.beneficiary_telegram_id > 0 THEN "+dblock.TextCast(tx, "snapshot.beneficiary_telegram_id")+" ELSE '' END AS buyer_telegram_id, CASE WHEN title.bot_instance_id = op.bot_id AND title.telegram_chat_id = op.chat_id AND title.route_version = op.route_version THEN COALESCE(NULLIF(title.obs_title, ''), title.name, '') ELSE COALESCE(title.name, '') END AS chat_title").
		Joins("LEFT JOIN telegram_membership_access_invites inv ON inv.id = op.invite_id AND inv.target_id = op.target_id AND inv.bot_id = op.bot_id AND inv.chat_id = op.chat_id AND inv.route_version = op.route_version AND inv.telegram_id = op.telegram_id AND inv.account_id = op.account_id").
		Joins("LEFT JOIN telegram_membership_ledger_sources source ON source.id = inv.source_id AND source.target_id = inv.target_id AND source.account_id = inv.account_id AND source.beneficiary_telegram_id = inv.telegram_id AND source.operation = 'grant'").
		Joins("LEFT JOIN telegram_membership_order_item_snapshots snapshot ON snapshot.order_item_id = source.order_item_id AND snapshot.account_id = source.account_id AND snapshot.beneficiary_telegram_id = source.beneficiary_telegram_id").
		Joins("LEFT JOIN order_items item ON item.id = snapshot.order_item_id AND item.order_id = source.order_id AND item.deleted_at IS NULL").
		Joins("LEFT JOIN orders leaf ON leaf.id = item.order_id AND leaf.user_id = source.account_id AND leaf.deleted_at IS NULL").
		Joins("LEFT JOIN orders purchase ON purchase.id = COALESCE(leaf.parent_id, leaf.id) AND purchase.id = source.payment_order_id AND purchase.user_id = leaf.user_id AND purchase.parent_id IS NULL AND purchase.deleted_at IS NULL").
		Joins("LEFT JOIN (?) title ON title.id = op.target_id", titles)
}

// ListAccessTasks reads bot-scoped operations with an exclusive cursor and bounded page size.
func (store *Store) ListAccessTasks(ctx context.Context, bot, after uint, limit int) ([]c.AccessTask, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	rows := []c.AccessTask{}
	err := accessTaskQuery(store.db.WithContext(ctx)).Where("op.bot_id = ? AND op.id > ? AND op.kind <> ?", bot, after, "lifecycle").Order("op.id").Limit(limit).Scan(&rows).Error
	for index := range rows {
		accessTaskActions(&rows[index])
	}
	return rows, err
}

// AccessTask reads one bot-scoped task for versioned recovery; absence returns ErrNotFound.
func (store *Store) AccessTask(ctx context.Context, bot, id uint) (*c.AccessTask, error) {
	var row c.AccessTask
	query := accessTaskQuery(store.db.WithContext(ctx)).Where("op.bot_id = ? AND op.id = ?", bot, id).Scan(&row)
	if query.Error != nil {
		return nil, query.Error
	}
	if row.ID == 0 {
		return nil, c.ErrNotFound
	}
	accessTaskActions(&row)
	return &row, nil
}

// accessTaskActions exposes candidate actions; writes revalidate all current business prerequisites.
func accessTaskActions(row *c.AccessTask) {
	row.Actions = []string{}
	if row.Kind == "lifecycle" {
		row.BlockedReason = "manual_only"
		return
	}
	if row.State == "failed" || row.State == "retryable" {
		row.Actions = append(row.Actions, "retry")
	}
	if (row.Kind == "create" || row.Kind == "approve") && (row.State == "failed" || row.State == "retryable") {
		row.Actions = append(row.Actions, "refresh_member")
	}
	if row.Kind == "approve" && row.State == "unknown" {
		row.Actions = append(row.Actions, "reconcile")
	}
	if (row.Kind == "create" || row.Kind == "revoke") && row.State == "unknown" {
		row.Actions = append(row.Actions, "rebuild")
	}
	if row.State == "unknown" || row.State == "needs_review" || row.State == "failed" {
		row.Actions = append(row.Actions, "review")
	}
	if row.State == "unknown" {
		row.BlockedReason = "unknown_effect"
	}
	if row.Kind == "decline" && row.State == "unknown" || row.State == "needs_review" {
		row.BlockedReason = "manual_only"
	}
}

// ListAccessReviews includes unknown-link applications without trusted sources or linked operations.
func (store *Store) ListAccessReviews(ctx context.Context, bot, after uint, limit int) ([]c.AccessReview, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	rows := []c.AccessReview{}
	err := accessReviewQuery(store.db.WithContext(ctx), bot).Where("id > ?", after).Order("id").Limit(limit).Scan(&rows).Error
	return rows, err
}

// accessReviewQuery shares the exact cursor/page projection and review-state filter.
// tx supplies the connection; bot restricts all rows, including reviewed historical applications.
func accessReviewQuery(tx *gorm.DB, bot uint) *gorm.DB {
	return tx.Model(&d.AccessJoin{}).Select("id, bot_id, chat_id, telegram_id, invite_id, state, received_at, updated_at, actor_admin_id, review_reason, reviewed_at, COALESCE((?), '') AS telegram_username", telegramProfileUsername(tx, "telegram_membership_access_joins")).Where("bot_id = ? AND state = ?", bot, "needs_review")
}

// AccessTasksPage returns a bounded bot-scoped OFFSET page and authoritative filtered count.
// page is one-based, size is at most 100; failures never return a fabricated total.
func (store *Store) AccessTasksPage(ctx context.Context, bot uint, page, size int, orderNo ...string) ([]c.AccessTask, int64, error) {
	filter, filterErr := c.AccessOrderFilter(orderNo...)
	if filterErr != nil || bot == 0 || !c.ValidAdminPage(page, size) {
		return nil, 0, c.ErrInvalid
	}
	rows := []c.AccessTask{}
	var total int64
	query := accessTaskQuery(store.db.WithContext(ctx)).Where("op.bot_id = ? AND op.kind <> ?", bot, "lifecycle")
	if filter != "" {
		query = query.Where("purchase.order_no = ?", filter)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("op.id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	for index := range rows {
		accessTaskActions(&rows[index])
	}
	return rows, total, nil
}

// AccessReviewsPage preserves the existing review filter and bot scope for COUNT and OFFSET.
// page/size are validated before reading; the result contains no invitation secrets.
func (store *Store) AccessReviewsPage(ctx context.Context, bot uint, page, size int) ([]c.AccessReview, int64, error) {
	return store.SearchAccessReviewsPage(ctx, bot, page, size, "")
}

// RecoverAccess applies administrator intent inside a ledger transaction; rejected actions retain an audit receipt.
func (store *Store) RecoverAccess(ctx context.Context, bot, id, actor uint, input c.AccessRecoveryInput, route *c.AccessRoute, at time.Time) error {
	if input.Action == "review_join" {
		return store.reviewAccessJoin(ctx, bot, id, actor, input, at)
	}
	if bot == 0 || id == 0 || actor == 0 || input.ExpectedVersion == 0 || input.ExpectedRouteVersion == 0 || input.Reason != "operator_verified" {
		return c.ErrInvalid
	}
	if input.Action != "retry" && input.Action != "reconcile" && input.Action != "refresh_member" && input.Action != "rebuild" && input.Action != "review" {
		return c.ErrInvalid
	}
	var denied error
	err := store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var operation d.AccessOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("bot_id = ? AND id = ?", bot, id).First(&operation).Error; err != nil {
			return err
		}
		if operation.Version != input.ExpectedVersion || operation.RouteVersion != input.ExpectedRouteVersion || operation.State == "creating" {
			return c.ErrConflict
		}
		if err := tx.SavePoint("access_recovery").Error; err != nil {
			return err
		}
		denied = recoverAccessAction(tx, &operation, input.Action, route, at)
		if denied != nil {
			if err := tx.RollbackTo("access_recovery").Error; err != nil {
				return err
			}
		}
		outcome := "accepted"
		if denied != nil {
			outcome = "blocked"
		}
		receipt := d.AccessResult{OperationID: operation.ID, LeaseVersion: operation.Version + 1, Generation: operation.Generation, Outcome: outcome, ActorAdminID: actor, Action: input.Action, Reason: input.Reason, ReceivedAt: at}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		updated := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ?", operation.ID, input.ExpectedVersion).Updates(map[string]any{"version": operation.Version + 1, "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	return denied
}

// reviewAccessJoin records the first authorized manual-only disposition; unknown links cannot be auto-approved.
// bot/id/actor scope the durable inbox record; fixed reason avoids storing secrets in free text.
func (store *Store) reviewAccessJoin(ctx context.Context, bot, id, actor uint, input c.AccessRecoveryInput, at time.Time) error {
	if bot == 0 || id == 0 || actor == 0 || input.Reason != "operator_verified" {
		return c.ErrInvalid
	}
	update := store.db.WithContext(ctx).Model(&d.AccessJoin{}).Where("bot_id = ? AND id = ? AND state = ? AND reviewed_at IS NULL", bot, id, "needs_review").Updates(map[string]any{"actor_admin_id": actor, "review_reason": "manual_only", "reviewed_at": at, "updated_at": at})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// recoverAccessAction never calls Telegram; review preserves facts instead of claiming an effect succeeded.
func recoverAccessAction(tx *gorm.DB, operation *d.AccessOperation, action string, route *c.AccessRoute, at time.Time) error {
	if operation.Kind == "lifecycle" {
		return c.ErrInvalid
	}
	if action == "review" {
		if operation.State != "unknown" && operation.State != "needs_review" && operation.State != "failed" {
			return c.ErrConflict
		}
		return tx.Model(operation).Update("review_reason", gorm.Expr("CASE WHEN review_reason = '' OR review_reason IS NULL THEN 'manual_only' ELSE review_reason END")).Error
	}
	if action == "reconcile" || action == "refresh_member" {
		if action == "reconcile" && (operation.Kind != "approve" || operation.State != "unknown") {
			return c.ErrInvalid
		}
		if action == "refresh_member" {
			if (operation.Kind != "create" && operation.Kind != "approve") || (operation.State != "failed" && operation.State != "retryable") {
				return c.ErrInvalid
			}
			if route == nil || route.TargetID != operation.TargetID || route.BotID != operation.BotID || route.ChatID != operation.ChatID || route.RouteVersion != operation.RouteVersion {
				return c.ErrCapability
			}
			read := *operation
			read.ParentOperationID = operation.ID
			if _, err := reconcileAccessEligibility(tx, &read, at); err != nil {
				return err
			}
			// Suspend automatic retries: a successful read requires a separate, fully validated retry.
			if err := tx.Model(operation).Update("state", c.AccessFailed).Error; err != nil {
				return err
			}
		}
		if err := enqueueAccessReconciliation(tx, *operation, at); err != nil {
			return err
		}
		// 人工处理后的 needs_review 仅重新排队只读核对；保留旧观测和原因作为历史证据。
		updated := tx.Model(&d.AccessOperation{}).Where("parent_operation_id = ? AND kind = ? AND state IN ?", operation.ID, "reconcile", []string{"pending", "retryable", "failed", "succeeded", "needs_review"}).Updates(map[string]any{"state": "pending", "next_attempt_at": at, "version": gorm.Expr("version + 1"), "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	}
	if action == "retry" && operation.Kind == "reconcile" {
		if operation.State != "failed" && operation.State != "retryable" {
			return c.ErrConflict
		}
		return tx.Model(operation).Updates(map[string]any{"state": "pending", "next_attempt_at": at}).Error
	}
	if route == nil || route.TargetID != operation.TargetID || route.BotID != operation.BotID || route.ChatID != operation.ChatID || route.RouteVersion != operation.RouteVersion {
		return c.ErrCapability
	}
	if operation.Kind == "revoke" && operation.CleanupCycleID != 0 {
		return recoverInviteCleanup(tx, operation, action, at)
	}
	identity := c.AccessIdentity{AccountID: operation.AccountID, TelegramID: operation.TelegramID, BotID: operation.BotID, Route: route}
	eligible, err := accessEligibility(tx, identity, operation.TargetID, at)
	if err != nil {
		return err
	}
	if !eligible.Active || operation.BindingVersion == 0 || eligible.BindingVersion != operation.BindingVersion {
		return c.ErrUntrustedIdentity
	}
	var invite d.AccessInvite
	if err := tx.First(&invite, operation.InviteID).Error; err != nil {
		return err
	}
	if action == "rebuild" {
		return rebuildAccess(tx, operation, invite, eligible, *route, at)
	}
	if operation.State != "failed" && operation.State != "retryable" {
		return c.ErrInvalid
	}
	if operation.Kind == "reconcile" || !d.IsInviteUsableAt(invite, at) {
		return c.ErrConflict
	}
	if operation.JoinID != 0 {
		latest, err := latestAccessJoin(tx, operation.BotID, operation.ChatID, operation.TelegramID)
		if err != nil {
			return err
		}
		if latest.ID != operation.JoinID {
			return c.ErrConflict
		}
	}
	if operation.Kind == "approve" && invite.State != "active" {
		return c.ErrConflict
	}
	if operation.Kind == "revoke" && (invite.LinkCipher == "" || invite.LinkHash == "") {
		return c.ErrInvalid
	}
	if err := tx.Model(operation).Updates(map[string]any{"state": "pending", "next_attempt_at": at, "entitlement_version": eligible.Version, "generation": route.Generation, "error_class": ""}).Error; err != nil {
		return err
	}
	return accessRelatedState(tx, *operation, "pending", at)
}

// rebuildAccess releases only a bounded, known expired entry with a conservative five-minute clock margin.
// Original unknown operations stay unknown; expiration never pretends Telegram confirmed revocation.
func rebuildAccess(tx *gorm.DB, operation *d.AccessOperation, invite d.AccessInvite, eligible c.AccessEligibility, route c.AccessRoute, at time.Time) error {
	if (operation.Kind != "create" && operation.Kind != "revoke") || operation.State != "unknown" {
		return c.ErrInvalid
	}
	if !d.IsInviteExpiredAt(invite, at) || invite.ExpiresAt.IsZero() || !invite.ExpiresAt.After(invite.CreatedAt) || invite.ExpiresAt.Sub(invite.CreatedAt) > 24*time.Hour || at.Before(invite.ExpiresAt.Add(5*time.Minute)) {
		return c.ErrConflict
	}
	var current d.AccessOperation
	if err := tx.Where("invite_id = ? AND kind = ?", invite.ID, "create").First(&current).Error; err != nil {
		return err
	}
	if current.State == "pending" || current.State == "retryable" || current.State == "creating" {
		return c.ErrConflict
	}
	if current.LastAttemptAt != nil && (current.LastAttemptAt.After(invite.ExpiresAt) || at.Before(current.LastAttemptAt.Add(5*time.Minute))) {
		return c.ErrConflict
	}
	var slot d.AccessSlot
	if err := tx.First(&slot, "telegram_id = ? AND target_id = ?", invite.TelegramID, invite.TargetID).Error; err != nil {
		return err
	}
	if slot.InviteID != invite.ID || invite.BindingVersion != eligible.BindingVersion {
		return c.ErrConflict
	}
	var inFlight int64
	if err := tx.Model(&d.AccessOperation{}).Where("invite_id = ? AND state = ?", invite.ID, "creating").Count(&inFlight).Error; err != nil {
		return err
	}
	if inFlight != 0 {
		return c.ErrConflict
	}
	if err := tx.Model(&d.AccessInvite{}).Where("id = ?", invite.ID).Updates(map[string]any{"state": "expired", "updated_at": at}).Error; err != nil {
		return err
	}
	row := invite
	row.Term = d.InviteTermLongTerm
	row.ID, row.Generation, row.EntitlementVersion = 0, route.Generation, eligible.Version
	row.State, row.LinkCipher, row.LinkHash = "pending", "", ""
	row.SourceID, row.CreatedAt, row.UpdatedAt, row.ExpiresAt = eligible.SourceID, at, at, time.Time{}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	next := accessCreateOperation(row, at)
	if err := tx.Create(&next).Error; err != nil {
		return err
	}
	update := tx.Model(&d.AccessSlot{}).Where("telegram_id = ? AND target_id = ? AND version = ? AND invite_id = ?", row.TelegramID, row.TargetID, slot.Version, invite.ID).Updates(map[string]any{"invite_id": row.ID, "version": slot.Version + 1})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return c.ErrConflict
	}
	return tx.Model(operation).Update("review_reason", "expired_entry_rebuilt").Error
}
