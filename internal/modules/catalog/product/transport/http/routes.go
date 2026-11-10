package producthttp

import (
	"context"
	"errors"
	"github.com/XManCodeing/XMall/internal/i18n"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
)

// RegisterPublicRoutes 注册公开商品目录端点。
func RegisterPublicRoutes(public gin.IRoutes, handler *PublicHandler) {
	if public == nil || handler == nil {
		panic("catalog product public routes: required dependency is nil")
	}
	public.GET("/products", handler.GetProducts)
	public.GET("/products/:slug", handler.GetProductBySlug)
}

// RegisterAdminRoutes 注册商品后台端点。
func RegisterAdminRoutes(admin gin.IRoutes, handler *AdminProductHandler) {
	if admin == nil || handler == nil {
		panic("catalog product admin routes: required dependency is nil")
	}
	admin.GET("/products", handler.GetAdminProducts)
	admin.GET("/products/:id", handler.GetAdminProduct)
	admin.POST("/products", handler.CreateProduct)
	admin.PUT("/products/:id", handler.UpdateProduct)
	admin.PATCH("/products/:id/wholesale-prices", handler.UpdateProductWholesalePrices)
	admin.PATCH("/products/:id", handler.QuickUpdateProduct)
	admin.DELETE("/products/:id", handler.DeleteProduct)
	admin.POST("/products/batch-status", handler.BatchUpdateProductStatus)
	admin.POST("/products/batch-category", handler.BatchUpdateProductCategory)
	admin.POST("/products/batch-stock-display", handler.BatchUpdateProductStockDisplay)
	admin.POST("/products/batch-delete", handler.BatchDeleteProducts)
}

// WithMembership 装配只读配置和商品查询；返回当前 handler 以复用原商品写入口。
func (h *AdminProductHandler) WithMembership(reader membership.ConfigReader, products productcontract.Repository) *AdminProductHandler {
	h.membershipReader, h.membershipProducts = reader, products
	return h
}

// WithMembershipShare injects the read-only projection and explicit share command.
func (h *AdminProductHandler) WithMembershipShare(reader membership.ShareReader, writer membership.ShareWriter) *AdminProductHandler {
	h.membershipShare, h.membershipWriter = reader, writer
	return h
}

// RegisterMembershipRoutes 注册独立授权的会员后台，不注册退款或支付操作。
func RegisterMembershipRoutes(admin gin.IRoutes, h *AdminProductHandler) {
	admin.GET("/telegram-bot/membership/products", h.membershipList)
	admin.GET("/telegram-bot/membership/products/:id", h.membershipGet)
	admin.POST("/telegram-bot/membership/products/:id/share", h.membershipCreateShare)
	admin.POST("/telegram-bot/membership/products", h.CreateProduct)
	admin.PUT("/telegram-bot/membership/products/:id", h.UpdateProduct)
	admin.PATCH("/telegram-bot/membership/products/:id", h.membershipStatus)
	admin.DELETE("/telegram-bot/membership/products/:id", h.membershipDelete)
}

// membershipCreateShare validates the route ID and explicitly creates/reuses an eligible link.
// Success always returns both contract fields, including unavailable reasons without a URL.
func (h *AdminProductHandler) membershipCreateShare(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, strconv.IntSize)
	if err != nil || id == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	if h.membershipWriter == nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", nil)
		return
	}
	share, err := h.membershipWriter.EnsureProductShare(c.Request.Context(), uint(id))
	if err != nil {
		if !respondMembershipError(c, err) {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		}
		return
	}
	actor, _ := c.Get("admin_id")
	ginutil.RequestLog(c).Infow("membership_product_shared", "admin_id", actor, "product_id", id)
	response.Success(c, share)
}

// membershipView 保持通用商品 JSON 结构并添加一次保存的配置及 SKU 编码。
type membershipView struct {
	productdomain.Product
	Membership             membershipConfigView `json:"membership"`
	ShareURL               string               `json:"share_url"`
	ShareUnavailableReason string               `json:"share_unavailable_reason"`
}
type membershipConfigView struct {
	Version         uint64                 `json:"version"`
	ExpectedVersion uint64                 `json:"expected_version"`
	Enabled         bool                   `json:"enabled"`
	Quantity        int                    `json:"quantity"`
	TargetNames     map[uint]string        `json:"target_names"`
	TargetIDs       []uint                 `json:"target_ids"`
	Terms           []membership.AdminTerm `json:"terms"`
}

// membershipProduct 从 reader 查询商品的实际扩展，不以分类推断会员。
func (h *AdminProductHandler) membershipProduct(c *gin.Context, product *productdomain.Product) (*membershipView, error) {
	if h.membershipReader == nil {
		return nil, membership.ErrNotFound
	}
	config, err := h.membershipReader.Config(c.Request.Context(), product.ID)
	if err != nil {
		return nil, err
	}
	view := &membershipView{Product: *product, Membership: membershipConfigView{Version: config.Version, ExpectedVersion: config.Version, Enabled: config.Enabled, Quantity: config.Quantity, TargetIDs: []uint{}, Terms: []membership.AdminTerm{}}}
	view.ShareUnavailableReason = membership.ShareUnavailable
	if h.membershipShare != nil {
		share, err := h.membershipShare.ProductShare(c.Request.Context(), config, product.IsActive)
		if err != nil {
			return nil, err
		}
		view.ShareURL, view.ShareUnavailableReason = share.URL, share.UnavailableReason
	}
	for _, target := range config.Targets {
		view.Membership.TargetIDs = append(view.Membership.TargetIDs, target.TargetID)
	}
	view.Membership.TargetNames = map[uint]string{}
	if reader, ok := h.membershipReader.(membership.TargetNameReader); ok {
		names, err := reader.TargetNames(c.Request.Context(), view.Membership.TargetIDs)
		if err != nil {
			return nil, err
		}
		view.Membership.TargetNames = names
	}
	for _, term := range config.Terms {
		code := ""
		for _, sku := range product.SKUs {
			if sku.ID == term.SKUID {
				code = sku.SKUCode
				break
			}
		}
		view.Membership.Terms = append(view.Membership.Terms, membership.AdminTerm{SKUID: term.SKUID, SKUCode: code, Kind: term.Kind, Months: term.Months})
	}
	return view, nil
}

// membershipList 只列真实配置商品，分页和查询复用原仓储。
func (h *AdminProductHandler) membershipList(c *gin.Context) {
	page, size := ginutil.ParsePagination(c)
	active, err := parseTriStateBoolFilter(c.Query("is_active"))
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	products, total, err := h.membershipProducts.List(productcontract.ListFilter{OnlyMembership: true, IsActive: active, Page: page, PageSize: size, Search: c.Query("search"), CategoryID: c.Query("category_id"), WithCategory: true})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}
	views := make([]membershipView, 0, len(products))
	for index := range products {
		view, err := h.membershipProduct(c, &products[index])
		if err != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
			return
		}
		views = append(views, *view)
	}
	response.SuccessWithPage(c, views, response.BuildPagination(page, size, total))
}

// membershipGet 拒绝将普通商品作为会员详情读取。
func (h *AdminProductHandler) membershipGet(c *gin.Context) {
	if !h.validateMembershipScope(c) {
		return
	}
	h.GetAdminProduct(c)
}

// validateMembershipScope 验证 path 中已有配置；不存在时返回 404。
func (h *AdminProductHandler) validateMembershipScope(c *gin.Context) bool {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return false
	}
	if h.membershipReader == nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", nil)
		return false
	}
	_, err = h.membershipReader.Config(c.Request.Context(), uint(id))
	if err != nil {
		if !respondMembershipError(c, err) {
			ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		}
		return false
	}
	return true
}

// validateMembershipRequest 禁止普通端点携带扩展，会员端点必须完整保存扩展。
func (h *AdminProductHandler) validateMembershipRequest(c *gin.Context, input *membership.AdminConfigInput) bool {
	scoped := strings.Contains(c.FullPath(), "/telegram-bot/membership/")
	if scoped != (input != nil) {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return false
	}
	if scoped && c.Param("id") != "" {
		return h.validateMembershipScope(c)
	}
	return true
}

// respondProduct 返回原商品或会员聚合视图；不改变通用后台的响应。
func (h *AdminProductHandler) respondProduct(c *gin.Context, product *productdomain.Product) {
	if !strings.Contains(c.FullPath(), "/telegram-bot/membership/") {
		response.Success(c, product)
		return
	}
	full, err := h.membershipProducts.GetAdminByID(strconv.FormatUint(uint64(product.ID), 10))
	if err != nil || full == nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}
	view, err := h.membershipProduct(c, full)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.product_fetch_failed", err)
		return
	}
	if c.Request.Method == "POST" || c.Request.Method == "PUT" {
		actor, _ := c.Get("admin_id")
		ginutil.RequestLog(c).Infow("membership_product_saved", "admin_id", actor, "product_id", product.ID, "version", view.Membership.Version)
	}
	response.Success(c, view)
}

// membershipAdminCommands 是原商品服务的窄运营端口，不建立第二套商品 CRUD。
type membershipAdminCommands interface {
	UpdateMembershipStatus(context.Context, string, bool, uint64) (*productdomain.Product, error)
	DeleteMembership(context.Context, string, uint64) error
}

// membershipStatus 处理独立会员 PATCH；必填布尔与版本，返回更新后的会员聚合。
// 业务层在同一事务内验证会员范围和版本，不依赖 HTTP 层的非原子预读。
func (h *AdminProductHandler) membershipStatus(c *gin.Context) {
	var input struct {
		IsActive        *bool  `json:"is_active" binding:"required"`
		ExpectedVersion uint64 `json:"expected_version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}
	commands, ok := h.admin.(membershipAdminCommands)
	if !ok {
		ginutil.RespondError(c, response.CodeInternal, "error.product_update_failed", nil)
		return
	}
	product, err := commands.UpdateMembershipStatus(c.Request.Context(), c.Param("id"), *input.IsActive, input.ExpectedVersion)
	if err != nil {
		h.membershipOperationError(c, err, "error.product_update_failed")
		return
	}
	if product == nil {
		ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
		return
	}
	actor, _ := c.Get("admin_id")
	ginutil.RequestLog(c).Infow("membership_product_deactivated", "admin_id", actor, "product_id", product.ID, "expected_version", input.ExpectedVersion)
	h.respondProduct(c, product)
}

// membershipDelete 接收 query expected_version，成功沿用原删除响应 data:null。
// 版本、会员范围及删除保护均由原商品服务事务完成。
func (h *AdminProductHandler) membershipDelete(c *gin.Context) {
	version, err := strconv.ParseUint(c.Query("expected_version"), 10, 64)
	if err != nil || version == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	commands, ok := h.admin.(membershipAdminCommands)
	if !ok {
		ginutil.RespondError(c, response.CodeInternal, "error.product_delete_failed", nil)
		return
	}
	if err := commands.DeleteMembership(c.Request.Context(), c.Param("id"), version); err != nil {
		h.membershipOperationError(c, err, "error.product_delete_failed")
		return
	}
	actor, _ := c.Get("admin_id")
	ginutil.RequestLog(c).Infow("membership_product_deleted", "admin_id", actor, "product_id", c.Param("id"), "expected_version", version)
	response.Success(c, nil)
}

// membershipOperationError 将 err 映射到既有会员/商品保护响应，fallback 仅用于未知错误。
func (h *AdminProductHandler) membershipOperationError(c *gin.Context, err error, fallback string) {
	if respondMembershipError(c, err) {
		return
	}
	switch {
	case errors.Is(err, productcontract.ErrProductHasStock):
		ginutil.RespondError(c, response.CodeBadRequest, "error.product_has_stock", nil)
	case errors.Is(err, productcontract.ErrProductHasOrderRecord):
		ginutil.RespondError(c, response.CodeBadRequest, "error.product_has_order_record", nil)
	default:
		ginutil.RespondError(c, response.CodeInternal, fallback, err)
	}
}

// respondMembershipError 将稳定配置错误转为已有三语响应；未知错误交回原处理。
func respondMembershipError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, membership.ErrConflict):
		response.ErrorWithHTTPStatus(c, 409, 409, i18n.T(i18n.ResolveLocale(c), "error.membership_conflict"))
	case errors.Is(err, membership.ErrNotFound):
		ginutil.RespondError(c, response.CodeNotFound, "error.product_not_found", nil)
	case errors.Is(err, membership.ErrCapability):
		ginutil.RespondError(c, response.CodeBadRequest, "error.telegram_group_capability", nil)
	case errors.Is(err, membership.ErrSalesClosed), errors.Is(err, productcontract.ErrMembershipSaleClosed):
		ginutil.RespondError(c, response.CodeBadRequest, "error.membership_sales_closed", nil)
	case errors.Is(err, membership.ErrInvalid):
		ginutil.RespondError(c, response.CodeBadRequest, "error.membership_invalid", nil)
	default:
		return false
	}
	return true
}
