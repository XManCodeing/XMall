package producthttp

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	promotiondomain "github.com/XManCodeing/XMall/internal/modules/promotion/domain"

	memberleveldomain "github.com/XManCodeing/XMall/internal/modules/memberlevel/domain"

	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	productpresenter "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/presenter"

	contentcontract "github.com/XManCodeing/XMall/internal/modules/content/contract"
	reseller "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const publicRelatedPostsLimit = 6

// PublicProductQueries 是公开商品列表/详情所需的最小用例接口。
type PublicProductQueries interface {
	ListPublicForTenant(tenant reseller.TenantContext, categoryID, search string, page, pageSize int) ([]productdomain.Product, int64, error)
	GetPublicBySlugForTenant(tenant reseller.TenantContext, slug string) (*productdomain.Product, error)
	ApplyAutoStockCounts(products []productdomain.Product) error
}

// ResellerDisplayPricer 是分销站展示价解析端口。
type ResellerDisplayPricer interface {
	LoadDisplayPricingBatch(tenant reseller.TenantContext, products []productdomain.Product) (*reseller.DisplayPricingBatch, error)
	ResolveDisplayPrices(tenant reseller.TenantContext, product *productdomain.Product, batch *reseller.DisplayPricingBatch) (*reseller.DisplayPriceResult, error)
}

// ProductPromotionDecorator 是公开商品促销装饰端口。
type ProductPromotionDecorator interface {
	GetProductPromotions(productID uint) ([]promotiondomain.Promotion, error)
	ApplyPromotion(product *productdomain.Product, quantity int) (*promotiondomain.Promotion, money.Amount, error)
}

// MemberLevelPricing 是公开商品会员价装饰端口。
type MemberLevelPricing interface {
	GetLevelPricesByProduct(productID uint) ([]memberleveldomain.MemberLevelPrice, error)
	ResolveMemberPrice(levelID, productID, skuID uint, basePrice decimal.Decimal) (decimal.Decimal, decimal.Decimal)
}

// LocalProductMappingReader 按本地商品 ID 读取上游映射。
type LocalProductMappingReader interface {
	GetByLocalProductID(productID uint) (*mappingdomain.Mapping, error)
}

// RelatedPostReader 是商品详情消费方所需的最小 Content 读取接口。
type RelatedPostReader interface {
	ListPostsForProduct(ctx context.Context, productID uint, limit int) ([]contentcontract.RelatedPost, error)
}

// PublicHandler 处理公开商品目录 HTTP 请求。
type PublicHandler struct {
	membership   membership.DisplayReader
	products     PublicProductQueries
	pricer       ResellerDisplayPricer
	promotions   ProductPromotionDecorator
	memberLevels MemberLevelPricing
	mappings     LocalProductMappingReader
	skuMappings  SKUMappingLookup
	relatedPosts RelatedPostReader
}

// NewPublicHandler 创建公开商品目录 Handler。
func NewPublicHandler(
	products PublicProductQueries,
	pricer ResellerDisplayPricer,
	promotions ProductPromotionDecorator,
	memberLevels MemberLevelPricing,
	mappings LocalProductMappingReader,
	skuMappings SKUMappingLookup,
	relatedPosts RelatedPostReader,
) *PublicHandler {
	if products == nil || relatedPosts == nil {
		panic("catalog public handler: required dependency is nil")
	}
	return &PublicHandler{
		products:     products,
		pricer:       pricer,
		promotions:   promotions,
		memberLevels: memberLevels,
		mappings:     mappings,
		skuMappings:  skuMappings,
		relatedPosts: relatedPosts,
	}
}

// WithMembership 注入共享配置 reader，返回原公开 handler；不改变商品可见性。
func (h *PublicHandler) WithMembership(reader membership.DisplayReader) *PublicHandler {
	h.membership = reader
	return h
}

// publicMembership 在 ctx 下读取可见 product 的配置，返回白名单投影或错误。
// 仅缺配置视为普通商品；沿现有逐商品装饰模式，每商品读取一次，并复用预加载 SKU。
func (h *PublicHandler) publicMembership(ctx context.Context, product *productdomain.Product) (*productpresenter.Membership, error) {
	if h.membership == nil {
		return nil, nil
	}
	config, err := h.membership.Display(ctx, product.ID)
	if errors.Is(err, membership.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "public membership configuration read failed", "product_id", product.ID)
		return nil, err
	}
	if config == nil || config.ProductID != product.ID || config.Version == 0 || config.Quantity != 1 || len(config.Terms) == 0 || len(config.GroupNames) == 0 {
		return nil, membership.ErrInvalid
	}
	projection := &productpresenter.Membership{
		ConfigVersion: config.Version,
		Quantity:      1,
		SalesOpen:     config.SalesOpen && product.IsActive,
		GroupNames:    append([]string(nil), config.GroupNames...),
		Terms:         make([]productpresenter.MembershipTerm, 0, len(config.Terms)),
	}
	codes := make(map[uint]string, len(product.SKUs))
	for _, sku := range product.SKUs {
		if sku.IsActive {
			codes[sku.ID] = sku.SKUCode
		}
	}
	seen := make(map[uint]bool, len(config.Terms))
	for _, term := range config.Terms {
		if term.ProductID != product.ID || term.SKUID == 0 || seen[term.SKUID] ||
			(term.Kind != "months" && term.Kind != "permanent") ||
			(term.Kind == "months" && term.Months <= 0) || (term.Kind == "permanent" && term.Months != 0) {
			return nil, membership.ErrInvalid
		}
		seen[term.SKUID] = true
		// 已下架/删除 SKU 不随会员扩展重新暴露；隐藏规格 DEFAULT 仍有稳定 ID/编码。
		code, visible := codes[term.SKUID]
		if !visible {
			continue
		}
		projection.Terms = append(projection.Terms, productpresenter.MembershipTerm{
			SKUID: term.SKUID, SKUCode: code, Kind: term.Kind, Months: term.Months,
		})
	}
	return projection, nil
}

func tenantFromRequest(c *gin.Context) reseller.TenantContext {
	return reseller.TenantOrMain(c.Request.Context())
}

func isResellerTenant(tenant reseller.TenantContext) bool {
	return tenant.ResellerID != nil && !tenant.IsMain && !tenant.Unavailable
}

// GetProducts 获取商品列表
func (h *PublicHandler) GetProducts(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)
	categoryID := c.Query("category_id")
	search := strings.TrimSpace(c.Query("search"))
	tenant := tenantFromRequest(c)

	products, total, err := h.products.ListPublicForTenant(tenant, categoryID, search, page, pageSize)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}

	if err := h.products.ApplyAutoStockCounts(products); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}

	var resellerBatch *reseller.DisplayPricingBatch
	if isResellerTenant(tenant) {
		if h.pricer == nil {
			ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
			return
		}
		resellerBatch, err = h.pricer.LoadDisplayPricingBatch(tenant, products)
		if err != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
			return
		}
	}

	decorated := make([]productpresenter.Product, 0, len(products))
	for i := range products {
		item, derr := h.decoratePublicProductForTenant(&products[i], h.promotions, tenant, resellerBatch)
		if derr != nil {
			if errors.Is(derr, productcontract.ErrResellerProductNotListed) {
				continue
			}
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", derr)
			return
		}
		item.Membership, derr = h.publicMembership(c.Request.Context(), &products[i])
		if derr != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", derr)
			return
		}
		decorated = append(decorated, item)
	}

	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, decorated, pagination)
}

// GetProductBySlug 根据 slug 获取商品详情
func (h *PublicHandler) GetProductBySlug(c *gin.Context) {
	slug := c.Param("slug")
	tenant := tenantFromRequest(c)

	product, err := h.products.GetPublicBySlugForTenant(tenant, slug)
	if err != nil {
		if errors.Is(err, productcontract.ErrNotFound) {
			ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}

	temp := []productdomain.Product{*product}
	if err := h.products.ApplyAutoStockCounts(temp); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}
	*product = temp[0]

	var resellerBatch *reseller.DisplayPricingBatch
	if isResellerTenant(tenant) {
		if h.pricer == nil {
			ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
			return
		}
		resellerBatch, err = h.pricer.LoadDisplayPricingBatch(tenant, []productdomain.Product{*product})
		if err != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
			return
		}
	}

	decorated, derr := h.decoratePublicProductForTenant(product, h.promotions, tenant, resellerBatch)
	if derr != nil {
		if errors.Is(derr, productcontract.ErrResellerProductNotListed) {
			ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", derr)
		return
	}

	decorated.Membership, derr = h.publicMembership(c.Request.Context(), product)
	if derr != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", derr)
		return
	}

	if posts, relatedErr := h.loadRelatedPostCards(c.Request.Context(), product.ID); relatedErr == nil {
		decorated.RelatedPosts = posts
	}

	response.Success(c, decorated)
}

func (h *PublicHandler) loadRelatedPostCards(ctx context.Context, productID uint) ([]productpresenter.RelatedPost, error) {
	posts, err := h.relatedPosts.ListPostsForProduct(ctx, productID, publicRelatedPostsLimit)
	if err != nil {
		return nil, err
	}
	result := make([]productpresenter.RelatedPost, 0, len(posts))
	for i := range posts {
		post := &posts[i]
		result = append(result, productpresenter.RelatedPost{
			ID:          post.ID,
			Slug:        post.Slug,
			Type:        post.Type,
			Title:       post.Title,
			Summary:     post.Summary,
			Thumbnail:   post.Thumbnail,
			PublishedAt: post.PublishedAt,
		})
	}
	return result, nil
}
