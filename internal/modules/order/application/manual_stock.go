package application

import (
	"strconv"
	"strings"
	"time"

	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"

	"github.com/XManCodeing/XMall/internal/constants"
)

// releaseCanceledResources releases leaf inventory and coupon usage inside the cancellation transaction.
// tx/order are locked snapshots; errors abort the enclosing transaction.
func releaseCanceledResources(tx ordercontract.Transaction, order *orderdomain.Order, rollbackCoupon bool) error {
	leaves := order.Children
	if len(leaves) == 0 {
		leaves = []orderdomain.Order{*order}
	}
	for _, leaf := range leaves {
		if _, err := tx.CardSecrets().ReleaseByOrder(leaf.ID); err != nil {
			return err
		}
		if err := releaseManualStockByItems(tx.Products(), tx.ProductSKUs(), leaf.Items); err != nil {
			return err
		}
	}
	if !rollbackCoupon {
		return nil
	}
	usages, err := tx.CouponUsages().ListByOrderID(order.ID)
	if err != nil || len(usages) == 0 {
		return err
	}
	if err := tx.CouponUsages().DeleteByOrderID(order.ID); err != nil {
		return err
	}
	counts := make(map[uint]int)
	for _, usage := range usages {
		counts[usage.CouponID]++
	}
	for couponID, count := range counts {
		if err := tx.Coupons().DecrementUsedCount(couponID, count); err != nil {
			return err
		}
	}
	return nil
}

// RestoreCanceledOrderResources re-reserves the resources released by cancellation.
// tx/order belong to the locked payment transaction; errors roll back every reservation.
// Coupons and upstream procurement cannot be reconstructed safely by these ports.
func RestoreCanceledOrderResources(tx ordercontract.Transaction, order *orderdomain.Order, now time.Time) error {
	if order.CouponID != nil || order.Fulfillment != nil {
		return ErrOrderCancelNotAllowed
	}
	for _, item := range order.Items {
		if item.Quantity <= 0 || item.ProductID == 0 {
			return ErrOrderCancelNotAllowed
		}
		switch strings.TrimSpace(item.FulfillmentType) {
		case constants.FulfillmentTypeManual:
			if err := restoreManualItem(tx, item); err != nil {
				return err
			}
		case constants.FulfillmentTypeAuto:
			rows, err := tx.CardSecrets().ListAvailableByProductForUpdate(item.ProductID, item.SKUID, item.Quantity)
			if err != nil {
				return err
			}
			if len(rows) != item.Quantity {
				return ErrCardSecretInsufficient
			}
			ids := make([]uint, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			affected, err := tx.CardSecrets().Reserve(ids, order.ID, now)
			if err != nil {
				return err
			}
			if affected != int64(item.Quantity) {
				return ErrCardSecretInsufficient
			}
		default:
			return ErrOrderCancelNotAllowed
		}
	}
	return nil
}

// restoreManualItem reuses atomic reserve for the immutable item and rejects missing products/SKUs.
// tx is the current payment transaction; returns inventory/query errors without partial commit.
func restoreManualItem(tx ordercontract.Transaction, item orderdomain.OrderItem) error {
	var affected int64
	var err error
	if item.SKUID != 0 {
		sku, fetchErr := tx.ProductSKUs().GetByID(item.SKUID)
		if fetchErr != nil {
			return fetchErr
		}
		if sku == nil || sku.ProductID != item.ProductID {
			return ErrManualStockInsufficient
		}
		if sku.ManualStockTotal == constants.ManualStockUnlimited {
			return nil
		}
		affected, err = tx.ProductSKUs().ReserveManualStock(item.SKUID, item.Quantity)
	} else {
		product, fetchErr := tx.Products().GetByID(strconv.FormatUint(uint64(item.ProductID), 10))
		if fetchErr != nil {
			return fetchErr
		}
		if product == nil {
			return ErrManualStockInsufficient
		}
		if product.ManualStockTotal == constants.ManualStockUnlimited {
			return nil
		}
		affected, err = tx.Products().ReserveManualStock(item.ProductID, item.Quantity)
	}
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrManualStockInsufficient
	}
	return nil
}

type manualStockSummary struct {
	BySKU           map[uint]int
	ByProductAll    map[uint]int
	ByLegacyProduct map[uint]int
}

func summarizeManualStockItems(items []orderdomain.OrderItem) manualStockSummary {
	result := manualStockSummary{
		BySKU:           make(map[uint]int),
		ByProductAll:    make(map[uint]int),
		ByLegacyProduct: make(map[uint]int),
	}
	for _, item := range items {
		if strings.TrimSpace(item.FulfillmentType) != constants.FulfillmentTypeManual {
			continue
		}
		if item.ProductID == 0 || item.Quantity <= 0 {
			continue
		}
		result.ByProductAll[item.ProductID] += item.Quantity
		if item.SKUID > 0 {
			result.BySKU[item.SKUID] += item.Quantity
			continue
		}
		result.ByLegacyProduct[item.ProductID] += item.Quantity
	}
	return result
}

func releaseManualStockByItems(productRepo productcontract.Repository, productSKURepo productcontract.SKURepository, items []orderdomain.OrderItem) error {
	var skuOp func(uint, int) (int64, error)
	if productSKURepo != nil {
		skuOp = productSKURepo.ReleaseManualStock
	}
	var productOp func(uint, int) (int64, error)
	if productRepo != nil {
		productOp = productRepo.ReleaseManualStock
	}
	return applyManualStockByItems(productRepo, productSKURepo, items, skuOp, productOp, false)
}

func ConsumeManualStockByItems(productRepo productcontract.Repository, productSKURepo productcontract.SKURepository, items []orderdomain.OrderItem) error {
	var skuOp func(uint, int) (int64, error)
	if productSKURepo != nil {
		skuOp = productSKURepo.ConsumeManualStock
	}
	var productOp func(uint, int) (int64, error)
	if productRepo != nil {
		productOp = productRepo.ConsumeManualStock
	}
	return applyManualStockByItems(productRepo, productSKURepo, items, skuOp, productOp, true)
}

func applyManualStockByItems(
	productRepo productcontract.Repository,
	productSKURepo productcontract.SKURepository,
	items []orderdomain.OrderItem,
	updateSKU func(uint, int) (int64, error),
	updateProduct func(uint, int) (int64, error),
	requireAffected bool,
) error {
	summary := summarizeManualStockItems(items)
	if productSKURepo != nil && updateSKU != nil {
		for skuID, quantity := range summary.BySKU {
			sku, err := productSKURepo.GetByID(skuID)
			if err != nil {
				return err
			}
			if sku == nil || sku.ManualStockTotal == constants.ManualStockUnlimited {
				continue
			}
			affected, err := updateSKU(skuID, quantity)
			if err != nil {
				return err
			}
			if requireAffected && affected != 1 {
				return ErrManualStockInsufficient
			}
		}
	}

	productSummary := summary.ByLegacyProduct
	if productSKURepo == nil {
		productSummary = summary.ByProductAll
	}
	if productRepo == nil || updateProduct == nil {
		return nil
	}
	for productID, quantity := range productSummary {
		product, err := productRepo.GetByID(strconv.FormatUint(uint64(productID), 10))
		if err != nil {
			return err
		}
		if product == nil || product.ManualStockTotal == constants.ManualStockUnlimited {
			continue
		}
		affected, err := updateProduct(productID, quantity)
		if err != nil {
			return err
		}
		if requireAffected && affected != 1 {
			return ErrManualStockInsufficient
		}
	}
	return nil
}
