package application

import (
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	"github.com/XManCodeing/XMall/internal/shared/money"
)

// ItemDetail 购物车项详情。
type ItemDetail struct {
	ProductID       uint
	SKUID           uint
	Quantity        int
	FulfillmentType string
	UnitPrice       money.Amount
	OriginalPrice   money.Amount
	Currency        string
	Product         *productdomain.Product
	SKU             *productdomain.ProductSKU
}

// UpsertItemInput 购物车更新输入。
type UpsertItemInput struct {
	UserID          uint
	ProductID       uint
	SKUID           uint
	Quantity        int
	FulfillmentType string
}
