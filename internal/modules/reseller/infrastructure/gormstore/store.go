package gormstore

import (
	"errors"
	"fmt"
	"strings"

	resellercontract "github.com/dujiao-next/internal/modules/reseller/contract"
	resellerdomain "github.com/dujiao-next/internal/modules/reseller/domain"
	"gorm.io/gorm"
)

// Store is the single GORM implementation of all reseller persistence ports.
type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Migrate creates the tables and partial unique indexes owned by the reseller module.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errors.New("reseller migration database is nil")
	}
	if err := db.AutoMigrate(
		&resellerdomain.Profile{},
		&resellerdomain.Domain{},
		&resellerdomain.SiteConfig{},
		&resellerdomain.ProductSetting{},
		&resellerdomain.OrderSnapshot{},
		&resellerdomain.LedgerEntry{},
		&resellerdomain.WithdrawRequest{},
		&resellerdomain.BalanceAccount{},
		&resellerdomain.RelatedAccount{},
	); err != nil {
		return err
	}
	// These constraints are migration-owned: MySQL does not support partial
	// indexes and silently ignores GORM's `where` index option.
	indexes := []activeUniqueIndex{
		{"reseller_domains", "idx_reseller_domains_active_domain", []string{"domain"}},
		{"reseller_site_configs", "idx_reseller_site_configs_active_reseller", []string{"reseller_id"}},
		{"reseller_product_settings", "idx_reseller_product_settings_active_scope", []string{"reseller_id", "product_id", "sku_id"}},
		{"reseller_balance_accounts", "idx_reseller_balance_accounts_active_currency", []string{"reseller_id", "currency"}},
		{"reseller_related_accounts", "idx_reseller_related_accounts_active_user", []string{"reseller_id", "user_id"}},
	}
	for _, index := range indexes {
		if err := ensureActiveUniqueIndex(db, index); err != nil {
			return err
		}
	}
	// Replace the old tag-owned scope index only after its replacement exists.
	// On MySQL the old index could have been created without its WHERE clause,
	// incorrectly preventing reuse of a soft-deleted product setting.
	const legacy = "idx_reseller_product_setting_scope"
	if db.Migrator().HasIndex(&resellerdomain.ProductSetting{}, legacy) {
		return db.Migrator().DropIndex(&resellerdomain.ProductSetting{}, legacy)
	}
	return nil
}

type activeUniqueIndex struct {
	table   string
	name    string
	columns []string
}

func ensureActiveUniqueIndex(db *gorm.DB, index activeUniqueIndex) error {
	quote := func(name string) string {
		return db.Statement.Quote(name)
	}
	columns := make([]string, 0, len(index.columns)+1)
	for _, column := range index.columns {
		columns = append(columns, quote(column))
	}
	predicate := " WHERE deleted_at IS NULL"
	if db.Dialector.Name() == "mysql" {
		// NULL entries do not conflict in a MySQL unique index. Every live row
		// gets 1; deleted rows get NULL, allowing arbitrarily many tombstones.
		// Keep this column out of models so AutoMigrate never turns it writable.
		const activeColumn = "active_unique_guard"
		if !db.Migrator().HasColumn(index.table, activeColumn) {
			statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TINYINT GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN 1 ELSE NULL END) VIRTUAL", quote(index.table), quote(activeColumn))
			if err := db.Exec(statement).Error; err != nil && !db.Migrator().HasColumn(index.table, activeColumn) {
				return fmt.Errorf("create active-row guard for %s: %w", index.table, err)
			}
		}
		columns = append(columns, quote(activeColumn))
		predicate = ""
	}
	if db.Migrator().HasIndex(index.table, index.name) {
		return nil
	}
	statement := fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s)%s", quote(index.name), quote(index.table), strings.Join(columns, ", "), predicate)
	if err := db.Exec(statement).Error; err != nil && !db.Migrator().HasIndex(index.table, index.name) {
		return fmt.Errorf("create active unique index %s (resolve duplicate live rows manually; no rows deleted): %w", index.name, err)
	}
	return nil
}

// BindTx returns the same reseller store surface bound to a caller-owned transaction.
func (s *Store) BindTx(tx *gorm.DB) *Store {
	if tx == nil {
		return s
	}
	return New(tx)
}

func (s *Store) transaction(run func(*Store) error) error {
	if s == nil || s.db == nil || run == nil {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return run(New(tx))
	})
}

func (s *Store) WithinManagementTransaction(run func(resellercontract.ManagementStore) error) error {
	return s.transaction(func(tx *Store) error { return run(tx) })
}

func (s *Store) WithinProductSettingTransaction(run func(resellercontract.ProductSettingStore) error) error {
	return s.transaction(func(tx *Store) error { return run(tx) })
}

func (s *Store) WithinWithdrawTransaction(run func(resellercontract.AccountingWithdrawStore) error) error {
	return s.transaction(func(tx *Store) error { return run(tx) })
}

func (s *Store) WithinLedgerTransaction(run func(resellercontract.AccountingLedgerStore) error) error {
	return s.transaction(func(tx *Store) error { return run(tx) })
}

func applyPagination(query *gorm.DB, page, pageSize int) *gorm.DB {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	return query.Offset((page - 1) * pageSize).Limit(pageSize)
}
