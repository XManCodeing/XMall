package application

import (
	"context"
	"fmt"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	"testing"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	"github.com/dujiao-next/internal/shared/money"
	"github.com/dujiao-next/internal/upstream"

	"github.com/shopspring/decimal"
)

type scriptedProductsAdapter struct {
	upstream.Adapter
	list func(upstream.ListProductsOpts) (*upstream.ProductListResult, error)
}

func (a scriptedProductsAdapter) ListProducts(_ context.Context, opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
	return a.list(opts)
}

func TestUnknownTotalPaginationPreservesOpaqueIDs(t *testing.T) {
	for _, cursorMode := range []bool{false, true} {
		t.Run(fmt.Sprint(cursorMode), func(t *testing.T) {
			calls := 0
			adapter := scriptedProductsAdapter{list: func(opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
				calls++
				if opts.Page != calls || opts.Offset != calls-1 {
					t.Fatalf("bad pagination: %+v", opts)
				}
				if cursorMode && calls == 2 && opts.Cursor != "opaque-next" {
					t.Fatalf("cursor lost: %+v", opts)
				}
				result := &upstream.ProductListResult{}
				if calls < 3 {
					result.Items = []upstream.UpstreamProduct{{ExternalID: []string{"001", "9223372036854775808"}[calls-1]}}
				}
				if cursorMode {
					more := calls < 2
					result.HasMore = &more
					if more {
						result.NextCursor = "opaque-next"
					}
				}
				return result, nil
			}}
			products, err := listAllUpstreamProducts(context.Background(), adapter)
			if err != nil || len(products) != 2 {
				t.Fatalf("products=%+v err=%v", products, err)
			}
			if !cursorMode && calls != 3 {
				t.Fatal("unknown total stopped on a short page")
			}
		})
	}
}

func TestCursorOnlyPaginationEndsWhenCursorExhausted(t *testing.T) {
	calls := 0
	adapter := scriptedProductsAdapter{list: func(opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
		calls++
		if calls > 2 {
			t.Fatal("exhausted cursor restarted pagination")
		}
		result := &upstream.ProductListResult{Items: []upstream.UpstreamProduct{{ExternalID: fmt.Sprint(calls)}}}
		if calls == 1 {
			result.NextCursor = "next"
		} else if opts.Cursor != "next" {
			t.Fatalf("cursor lost: %+v", opts)
		}
		return result, nil
	}}
	products, err := listAllUpstreamProducts(context.Background(), adapter)
	if err != nil || len(products) != 2 || calls != 2 {
		t.Fatalf("products=%v calls=%d err=%v", products, calls, err)
	}
}

func TestPaginationIntegrityGuards(t *testing.T) {
	for _, scenario := range []string{"truncated", "changing-total", "repeated-cursor", "limit"} {
		t.Run(scenario, func(t *testing.T) {
			adapter := scriptedProductsAdapter{list: func(opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
				result := &upstream.ProductListResult{Items: []upstream.UpstreamProduct{{ExternalID: fmt.Sprint(opts.Page)}}}
				switch scenario {
				case "truncated":
					result.Total = 3
					if opts.Page == 2 {
						result.Items = nil
					}
				case "changing-total":
					result.Total = 3 + opts.Page
				case "repeated-cursor":
					result.NextCursor = "same"
				}
				return result, nil
			}}
			if _, _, err := fetchUpstreamProducts(context.Background(), adapter, upstream.ListProductsOpts{PageSize: 50}, 3); err == nil {
				t.Fatal("expected integrity error")
			}
		})
	}
}

type captureSKUMappings struct{ items []mappingdomain.SKUMapping }

func (c *captureSKUMappings) Create(item *mappingdomain.SKUMapping) error {
	c.items = append(c.items, *item)
	return nil
}

func TestCreateSKUMappingsDoesNotCollapseEmptyCodes(t *testing.T) {
	repo := &captureSKUMappings{}
	err := createSKUMappings(repo, 1, []productdomain.ProductSKU{{ID: 1}, {ID: 2}, {ID: 3}}, []upstream.UpstreamSKU{
		{ExternalID: "001", PriceAmount: "1"}, {ExternalID: "1", PriceAmount: "2"}, {ProductLevel: true, PriceAmount: "3"},
	})
	if err != nil || len(repo.items) != 3 {
		t.Fatalf("%+v %v", repo.items, err)
	}
	if repo.items[0].UpstreamSKUExternalID != "001" || repo.items[1].UpstreamSKUExternalID != "1" || !repo.items[2].ProductLevel || repo.items[2].UpstreamSKUExternalID != "" {
		t.Fatalf("references collapsed: %+v", repo.items)
	}
	if upstreamReferenceKey(1, "", false) == upstreamReferenceKey(0, "1", false) {
		t.Fatal("numeric and external namespaces collapsed")
	}
}

func TestConvertUpstreamWholesalePricesRemapsUpstreamSKUScope(t *testing.T) {
	tiers := convertUpstreamWholesalePrices(productdomain.WholesalePriceTiers{
		{SKUID: 201, MinQuantity: 5, UnitPrice: money.FromDecimal(decimal.NewFromInt(80))},
	}, decimal.NewFromInt(1), decimal.Zero, "none", buildUpstreamWholesaleSKUIndex(
		[]productdomain.ProductSKU{{ID: 11, SKUCode: "SKU-A"}},
		[]upstream.UpstreamSKU{{ID: 201, SKUCode: "SKU-A"}},
		nil,
	))

	if len(tiers) != 1 {
		t.Fatalf("expected 1 tier, got %d", len(tiers))
	}
	if tiers[0].SKUID != 11 || tiers[0].SKUCode != "SKU-A" {
		t.Fatalf("expected upstream SKU scope to be remapped, got %+v", tiers[0])
	}
}

func TestConvertUpstreamWholesalePricesDropsUnmappedUpstreamSKUID(t *testing.T) {
	tiers := convertUpstreamWholesalePrices(productdomain.WholesalePriceTiers{
		{SKUID: 201, MinQuantity: 5, UnitPrice: money.FromDecimal(decimal.NewFromInt(80))},
	}, decimal.NewFromInt(1), decimal.Zero, "none")

	if len(tiers) != 0 {
		t.Fatalf("expected unmapped upstream sku_id tier to be dropped, got %+v", tiers)
	}
}

// pagedProductsAdapter 只实现 ListProducts：前 total 条商品按页返回，ignorePage 模拟上游忽略 page 参数。
type pagedProductsAdapter struct {
	upstream.Adapter
	total      int
	ignorePage bool
	calls      int
}

func (a *pagedProductsAdapter) ListProducts(_ context.Context, opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
	a.calls++
	page := opts.Page
	if a.ignorePage {
		page = 1
	}
	var items []upstream.UpstreamProduct
	for id := (page-1)*opts.PageSize + 1; id <= a.total && len(items) < opts.PageSize; id++ {
		items = append(items, upstream.UpstreamProduct{ID: uint(id), CategoryID: uint(id % 3)})
	}
	if a.ignorePage {
		return &upstream.ProductListResult{Total: 1 << 30, Items: items}, nil
	}
	return &upstream.ProductListResult{Total: a.total, Items: items}, nil
}

func TestListAllUpstreamProductsWalksEveryPage(t *testing.T) {
	adapter := &pagedProductsAdapter{total: 120}
	products, err := listAllUpstreamProducts(context.Background(), adapter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(products) != 120 || adapter.calls != 3 {
		t.Fatalf("expected 120 products in 3 calls, got %d in %d", len(products), adapter.calls)
	}
}

func TestListAllUpstreamProductsRejectsRepeatedPage(t *testing.T) {
	adapter := &pagedProductsAdapter{total: 50, ignorePage: true}
	if _, err := listAllUpstreamProducts(context.Background(), adapter); err == nil {
		t.Fatal("expected error when upstream pagination never terminates")
	}
	if adapter.calls != 2 {
		t.Fatalf("expected repeated page rejection after 2 calls, got %d", adapter.calls)
	}
}
