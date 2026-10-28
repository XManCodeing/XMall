package siteconnectionhttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/homles698698-lab/XMall/internal/i18n"
	app "github.com/homles698698-lab/XMall/internal/modules/siteconnection/application"
	contract "github.com/homles698698-lab/XMall/internal/modules/siteconnection/contract"
	"github.com/homles698698-lab/XMall/internal/platform/http/ginutil"
	"github.com/homles698698-lab/XMall/internal/platform/http/response"
	"io"
	"net/http"
	"strings"
)

// HTTPConfigService is optional to preserve existing AdminService mocks.
type HTTPConfigService interface {
	GetHTTPConfig(uint) (*app.HTTPConfigResult, error)
	PutHTTPConfig(uint, app.HTTPConfigInput) (*app.HTTPConfigResult, error)
	HTTPDebug(context.Context, uint, app.HTTPDebugInput) (*app.HTTPDebugResult, error)
}

func respondHTTPConnectionError(c *gin.Context, err error) bool {
	key := ""
	switch {
	case errors.Is(err, contract.ErrProtocolUnsupported):
		key = "error.connection_protocol_unsupported"
	case errors.Is(err, contract.ErrProtocolImmutable):
		key = "error.connection_protocol_immutable"
	case errors.Is(err, contract.ErrHTTPDraftOnly):
		key = "error.connection_http_draft_only"
	case errors.Is(err, contract.ErrHTTPConfigConflict):
		key = "error.connection_http_config_conflict"
	case errors.Is(err, contract.ErrHTTPConfigInvalid):
		key = "error.connection_http_config_invalid"
	case errors.Is(err, contract.ErrHTTPDebugFailed):
		key = "error.connection_http_debug_failed"
	}
	if key == "" {
		return false
	}
	ginutil.RespondError(c, response.CodeBadRequest, key, nil)
	return true
}
func (h *AdminHandler) httpConfigService(c *gin.Context) (HTTPConfigService, uint, bool) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return nil, 0, false
	}
	service, ok := h.connections.(HTTPConfigService)
	if !ok {
		ginutil.RespondError(c, response.CodeInternal, "error.connection_http_debug_failed", nil)
		return nil, 0, false
	}
	return service, id, true
}
func finishHTTPConfig(c *gin.Context, data any, err error) {
	if err == nil {
		if config, ok := data.(*app.HTTPConfigResult); ok && config != nil {
			for i, key := range config.ValidationErrors {
				parts := strings.Split(key, "|")
				if len(parts) == 3 && parts[0] == "httpIntegration.missingField" {
					config.ValidationErrors[i] = i18n.Sprintf(i18n.ResolveLocale(c), parts[0], parts[1], parts[2])
				} else {
					config.ValidationErrors[i] = i18n.T(i18n.ResolveLocale(c), key)
				}
			}
		}
		response.Success(c, data)
		return
	}
	if respondHTTPConnectionError(c, err) {
		return
	}
	if errors.Is(err, contract.ErrNotFound) {
		ginutil.RespondError(c, response.CodeNotFound, "error.connection_not_found", nil)
		return
	}
	// Upstream and parser errors can contain secrets: never reflect or log them.
	ginutil.RespondError(c, response.CodeInternal, "error.connection_http_debug_failed", nil)
}
func bindHTTPConfig(c *gin.Context, input any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, app.HTTPMaxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	if decoder.Decode(input) != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.connection_http_config_invalid", nil)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		ginutil.RespondError(c, response.CodeBadRequest, "error.connection_http_config_invalid", nil)
		return false
	}
	return true
}
func (h *AdminHandler) GetHTTPConfig(c *gin.Context) {
	service, id, ok := h.httpConfigService(c)
	if !ok {
		return
	}
	data, err := service.GetHTTPConfig(id)
	finishHTTPConfig(c, data, err)
}
func (h *AdminHandler) PutHTTPConfig(c *gin.Context) {
	service, id, ok := h.httpConfigService(c)
	if !ok {
		return
	}
	var input app.HTTPConfigInput
	if !bindHTTPConfig(c, &input) {
		return
	}
	data, err := service.PutHTTPConfig(id, input)
	finishHTTPConfig(c, data, err)
}
func (h *AdminHandler) HTTPDebug(c *gin.Context) {
	service, id, ok := h.httpConfigService(c)
	if !ok {
		return
	}
	var input app.HTTPDebugInput
	if !bindHTTPConfig(c, &input) {
		return
	}
	data, err := service.HTTPDebug(c.Request.Context(), id, input)
	finishHTTPConfig(c, data, err)
}

// RegisterAdminRoutes 注册后台站点对接连接路由。
func RegisterAdminRoutes(admin gin.IRoutes, handler *AdminHandler) {
	admin.GET("/site-connections", handler.GetSiteConnections)
	admin.GET("/site-connections/:id", handler.GetSiteConnection)
	admin.POST("/site-connections", handler.CreateSiteConnection)
	admin.PUT("/site-connections/:id", handler.UpdateSiteConnection)
	admin.DELETE("/site-connections/:id", handler.DeleteSiteConnection)
	admin.GET("/site-connections/:id/http-config", handler.GetHTTPConfig)
	admin.PUT("/site-connections/:id/http-config", handler.PutHTTPConfig)
	admin.POST("/site-connections/:id/http-debug", handler.HTTPDebug)
	admin.POST("/site-connections/:id/ping", handler.PingSiteConnection)
	admin.PUT("/site-connections/:id/status", handler.UpdateSiteConnectionStatus)
	admin.POST("/site-connections/:id/reapply-markup", handler.ReapplyConnectionMarkup)
}
