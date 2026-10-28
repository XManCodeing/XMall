// migrate-demo imports only the Orbit demo catalog into an empty MySQL catalog.
// It never loads .env files or runs application schema migrations.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"github.com/glebarez/sqlite"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const prefix = "demo-orbit-20261025-"

type config struct {
	Database struct {
		Driver string `yaml:"driver"`
		DSN    string `yaml:"dsn"`
	} `yaml:"database"`
	Upload struct {
		Path string `yaml:"path"`
	} `yaml:"upload"`
}
type catalog struct {
	Categories []category.Category  `json:"categories"`
	Products   []product.Product    `json:"products"`
	SKUs       []product.ProductSKU `json:"skus"`
}

func main() {
	apply := flag.Bool("apply", false, "backup MySQL and import the verified catalog")
	flag.Parse()
	if err := run(*apply); err != nil {
		fmt.Fprintln(os.Stderr, "migration stopped:", err)
		os.Exit(1)
	}
}
func run(apply bool) error {
	raw, err := os.ReadFile("config.yml")
	if err != nil {
		return errors.New("cannot read config.yml")
	}
	var cfg config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return errors.New("invalid config.yml")
	}
	if cfg.Database.Driver != "mysql" {
		return errors.New("target must be configured as mysql")
	}
	src, err := gorm.Open(sqlite.Open("file:db/dujiao.db?mode=ro"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return errors.New("cannot open source SQLite read-only")
	}
	srcSQL, _ := src.DB()
	defer srcSQL.Close()
	var data catalog
	if err := src.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("deleted_at IS NULL AND slug LIKE ?", prefix+"%").Order("id").Find(&data.Categories).Error; err != nil {
			return err
		}
		if err := tx.Where("deleted_at IS NULL AND slug LIKE ?", prefix+"%").Order("id").Find(&data.Products).Error; err != nil {
			return err
		}
		ids := []uint{}
		for _, p := range data.Products {
			ids = append(ids, p.ID)
		}
		return tx.Where("deleted_at IS NULL AND product_id IN ?", ids).Order("id").Find(&data.SKUs).Error
	}); err != nil {
		return fmt.Errorf("read source catalog: %w", err)
	}
	if len(data.Categories) != 4 || len(data.Products) != 20 || len(data.SKUs) != 32 {
		return errors.New("source demo counts changed; manual review required")
	}
	cats := map[uint]bool{}
	products := map[uint]bool{}
	for _, c := range data.Categories {
		cats[c.ID] = true
		if c.ParentID != 0 || c.Icon != "" {
			return errors.New("unexpected category hierarchy or icon")
		}
	}
	files := map[string]bool{}
	uploadRoot := cfg.Upload.Path
	if uploadRoot == "" {
		uploadRoot = "./uploads"
	}
	for _, p := range data.Products {
		if !cats[p.CategoryID] || p.FulfillmentType != "manual" || p.IsMapped || p.ManualStockLocked != 0 || p.ManualStockSold != 0 || (p.PaymentChannelIDs != "" && p.PaymentChannelIDs != "[]") {
			return fmt.Errorf("product %d has unsupported dependencies", p.ID)
		}
		products[p.ID] = true
		for _, image := range p.Images {
			if !strings.HasPrefix(image, "/uploads/"+strings.TrimSuffix(prefix, "-")+"/") || strings.Contains(image, "..") {
				return errors.New("unexpected demo image reference")
			}
			filename := filepath.Join(uploadRoot, strings.TrimPrefix(image, "/uploads/"))
			info, err := os.Stat(filename)
			if err != nil || info.Size() == 0 {
				return fmt.Errorf("missing or empty image: %s", filename)
			}
			files[filename] = true
		}
	}
	for _, sku := range data.SKUs {
		if !products[sku.ProductID] || sku.ManualStockLocked != 0 || sku.ManualStockSold != 0 {
			return errors.New("SKU has unsupported dependencies")
		}
	}
	if len(files) != 20 {
		return errors.New("expected 20 demo images")
	}
	if err := gormdb.InitDB("mysql", cfg.Database.DSN, gormdb.DBPoolConfig{MaxOpenConns: 2, MaxIdleConns: 2}, "release"); err != nil {
		return errors.New("cannot connect to configured MySQL; credentials omitted")
	}
	db := gormdb.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	targetSQL, err := db.DB()
	if err != nil {
		return err
	}
	defer targetSQL.Close()
	for _, table := range []string{"categories", "products", "product_skus"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil {
			return fmt.Errorf("count %s failed", table)
		}
		fmt.Printf("target %s: %d rows\n", table, count)
		if count != 0 {
			return errors.New("target catalog is not empty; refusing overwrite or repeated import")
		}
	}
	var admins int64
	if err := db.Table("admins").Count(&admins).Error; err != nil {
		return err
	}
	fmt.Printf("source: %d categories, %d products, %d SKUs; %d local images verified; target admins: %d (untouched)\n", len(data.Categories), len(data.Products), len(data.SKUs), len(files), admins)
	if !apply {
		fmt.Println("read-only preflight complete; no target data changed")
		return nil
	}
	backup, err := backupDatabase(targetSQL)
	if err != nil {
		return fmt.Errorf("backup failed; import not started: %w", err)
	}
	fmt.Println("full target logical backup:", backup)
	// Preserve IDs only because all three target tables are empty. No upserts,
	// updates of existing rows, constraint disabling or schema changes are used.
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"categories", "products", "product_skus"} {
			var n int64
			if err := tx.Table(table).Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return errors.New("target changed after preflight; aborting")
			}
		}
		for _, c := range data.Categories {
			if err := insertExact(tx, &c); err != nil {
				return err
			}
		}
		for _, p := range data.Products {
			if err := insertExact(tx, &p); err != nil {
				return err
			}
		}
		for _, sku := range data.SKUs {
			if err := insertExact(tx, &sku); err != nil {
				return err
			}
		}
		var got catalog
		if err := tx.Order("id").Find(&got.Categories).Error; err != nil {
			return err
		}
		if err := tx.Order("id").Find(&got.Products).Error; err != nil {
			return err
		}
		if err := tx.Order("id").Find(&got.SKUs).Error; err != nil {
			return err
		}
		// MySQL rounds/truncates timestamps to milliseconds; compare all other
		// exported fields, including titles, prices, stock, JSON, ordering and IDs.
		if err := compareCatalog(data, got); err != nil {
			return err
		}
		var after int64
		if err := tx.Table("admins").Count(&after).Error; err != nil {
			return err
		}
		if after != admins {
			return errors.New("administrator count changed during migration")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("catalog transaction rolled back: %w", err)
	}
	fmt.Println("COMMITTED: 4 categories, 20 products, 32 SKUs; all catalog fields verified; images reused; administrators untouched")
	return nil
}

// Maps avoid GORM replacing explicit false/zero fields with model defaults.
func insertExact(db *gorm.DB, value interface{}) error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(value); err != nil {
		return err
	}
	// Select persisted field names via JSON-free reflection from GORM's schema.
	rv := reflect.Indirect(reflect.ValueOf(value))
	row := map[string]interface{}{}
	for _, f := range stmt.Schema.Fields {
		if f.DBName != "" && f.Creatable {
			v, _ := f.ValueOf(context.Background(), rv)
			row[f.DBName] = v
		}
	}
	return db.Table(stmt.Schema.Table).Omit(clause.Associations).Create(row).Error
}

func compareCatalog(want, got catalog) error {
	zeroTimes := func(c *catalog) {
		for i := range c.Categories {
			c.Categories[i].CreatedAt = time.Time{}
		}
		for i := range c.Products {
			c.Products[i].CreatedAt = time.Time{}
			c.Products[i].UpdatedAt = time.Time{}
		}
		for i := range c.SKUs {
			c.SKUs[i].CreatedAt = time.Time{}
			c.SKUs[i].UpdatedAt = time.Time{}
		}
	}
	zeroTimes(&want)
	zeroTimes(&got)
	a, err := json.Marshal(want)
	if err != nil {
		return err
	}
	b, err := json.Marshal(got)
	if err != nil {
		return err
	}
	if string(a) != string(b) {
		return errors.New("imported catalog differs from source; refusing commit")
	}
	return nil
}

type tableBackup struct {
	Name    string          `json:"name"`
	DDL     string          `json:"ddl"`
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
}

// JSON retains byte values as base64, numeric values as numbers, and NULL as
// null. Each table includes its exact CREATE TABLE statement for restoration.
func backupDatabase(db *sql.DB) (string, error) {
	dir := filepath.Join("db", "migration-backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, "mysql-before-demo-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(name)
		}
	}()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	tables, err := tx.Query("SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME")
	if err != nil {
		return "", err
	}
	names := []string{}
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			tables.Close()
			return "", err
		}
		names = append(names, n)
	}
	err = tables.Err()
	tables.Close()
	if err != nil {
		return "", err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(map[string]interface{}{"format": "dujiao-mysql-logical-v1", "created_at": time.Now().UTC(), "tables": len(names)}); err != nil {
		return "", err
	}
	for _, n := range names {
		quoted := "`" + strings.ReplaceAll(n, "`", "``") + "`"
		var table, ddl string
		if err := tx.QueryRow("SHOW CREATE TABLE "+quoted).Scan(&table, &ddl); err != nil {
			return "", err
		}
		rows, err := tx.Query("SELECT * FROM " + quoted)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		dump := tableBackup{Name: n, DDL: ddl, Columns: columns, Rows: [][]interface{}{}}
		for rows.Next() {
			values := make([]interface{}, len(columns))
			ptrs := make([]interface{}, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return "", err
			}
			dump.Rows = append(dump.Rows, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
		if err := enc.Encode(dump); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	success = true
	return name, nil
}
