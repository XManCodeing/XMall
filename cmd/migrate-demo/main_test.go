package main

import (
	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestInsertExactPreservesFalseAndZero(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&category.Category{}, &product.Product{}, &product.ProductSKU{}); err != nil {
		t.Fatal(err)
	}
	c := category.Category{ID: 8, Slug: "test-category", NameJSON: jsonmap.JSON{"zh-CN": "测试"}, IsActive: false, CreatedAt: time.Now()}
	if err := insertExact(db, &c); err != nil {
		t.Fatal(err)
	}
	p := product.Product{ID: 9, CategoryID: 8, Slug: "test-product", TitleJSON: jsonmap.JSON{"zh-CN": "测试商品"}, IsActive: false}
	if err := insertExact(db, &p); err != nil {
		t.Fatal(err)
	}
	sku := product.ProductSKU{ID: 10, ProductID: 9, SKUCode: "DEFAULT", IsActive: false, ManualStockTotal: 0}
	if err := insertExact(db, &sku); err != nil {
		t.Fatal(err)
	}
	var got category.Category
	if err := db.First(&got, 8).Error; err != nil {
		t.Fatal(err)
	}
	if got.IsActive {
		t.Fatal("category default overwrote false")
	}
	var gotSKU product.ProductSKU
	if err := db.First(&gotSKU, 10).Error; err != nil {
		t.Fatal(err)
	}
	if gotSKU.IsActive || gotSKU.ManualStockTotal != 0 {
		t.Fatal("SKU zero values changed")
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return insertExact(tx, &c) }); err == nil {
		t.Fatal("duplicate must fail, not update")
	}
}
