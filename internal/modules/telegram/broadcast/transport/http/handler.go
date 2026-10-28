package broadcasthttp

import (
	"context"
	"errors"
	"strconv"
	"strings"

	broadcastapp "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"

	"github.com/gin-gonic/gin"
)

// AdminService is the transport-facing broadcast application contract.
type AdminService interface {
	ListBroadcasts(context.Context, broadcastapp.ListInput) ([]broadcastdomain.Broadcast, int64, error)
	GetBroadcast(context.Context, uint) (*broadcastdomain.Broadcast, error)
	CreateBroadcast(context.Context, broadcastapp.CreateInput) (*broadcastdomain.Broadcast, error)
	ListTelegramUsers(broadcastapp.UserQuery) ([]broadcastapp.UserItem, int64, error)
}

type createBroadcastRequest struct {
	BotID          uint         `json:"bot_id" binding:"required"`
	Title          string       `json:"title" binding:"required"`
	RecipientType  string       `json:"recipient_type" binding:"required"`
	UserIDs        []uint       `json:"user_ids"`
	Filters        jsonmap.JSON `json:"filters"`
	AttachmentURL  string       `json:"attachment_url"`
	AttachmentName string       `json:"attachment_name"`
	MessageHTML    string       `json:"message_html" binding:"required"`
}

// AdminHandler handles administrative Telegram broadcast requests.
type AdminHandler struct {
	broadcasts AdminService
}

func NewAdminHandler(broadcasts AdminService) *AdminHandler {
	if broadcasts == nil {
		panic("telegram broadcast handler: broadcasts is nil")
	}
	return &AdminHandler{broadcasts: broadcasts}
}

// List returns Telegram broadcast jobs.
func (h *AdminHandler) List(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	items, total, err := h.broadcasts.ListBroadcasts(c.Request.Context(), broadcastapp.ListInput{
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.bad_request", err)
		return
	}
	response.SuccessWithPage(c, items, response.BuildPagination(page, pageSize, total))
}

// Create persists and dispatches a Telegram broadcast job.
func (h *AdminHandler) Create(c *gin.Context) {
	var req createBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ginutil.RespondBindError(c, err)
		return
	}

	result, err := h.broadcasts.CreateBroadcast(c.Request.Context(), broadcastapp.CreateInput{
		BotID:          req.BotID,
		Title:          req.Title,
		RecipientType:  req.RecipientType,
		UserIDs:        req.UserIDs,
		Filters:        req.Filters,
		AttachmentURL:  req.AttachmentURL,
		AttachmentName: req.AttachmentName,
		MessageHTML:    req.MessageHTML,
	})
	if err != nil {
		if errors.Is(err, broadcastapp.ErrInvalid) ||
			errors.Is(err, broadcastapp.ErrNoRecipients) ||
			errors.Is(err, broadcastapp.ErrTokenUnavailable) {
			ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
			return
		}
		ginutil.RespondError(c, response.CodeInternal, "error.bad_request", err)
		return
	}
	response.Success(c, result)
}

// Get returns one Telegram broadcast job.
func (h *AdminHandler) Get(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", errors.New("invalid broadcast id"))
		return
	}
	broadcast, err := h.broadcasts.GetBroadcast(c.Request.Context(), id)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.bad_request", err)
		return
	}
	if broadcast == nil {
		ginutil.RespondError(c, response.CodeNotFound, "error.not_found", broadcastapp.ErrNotFound)
		return
	}
	response.Success(c, broadcast)
}

type deliveryAdminService interface {
	ListBroadcastDeliveries(context.Context, uint, int, int) ([]broadcastapp.DeliveryItem, int64, error)
	RetryBroadcastDelivery(context.Context, uint, uint) error
}

func deliveryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, broadcastapp.ErrNotFound):
		ginutil.RespondError(c, response.CodeNotFound, "error.not_found", err)
	case errors.Is(err, broadcastapp.ErrInvalid), errors.Is(err, broadcastapp.ErrRetryDenied):
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
	default:
		ginutil.RespondError(c, response.CodeInternal, "error.bad_request", err)
	}
}

func (h *AdminHandler) ListDeliveries(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil || id == 0 {
		deliveryError(c, broadcastapp.ErrInvalid)
		return
	}
	svc, ok := h.broadcasts.(deliveryAdminService)
	if !ok {
		deliveryError(c, broadcastapp.ErrInvalid)
		return
	}
	page, size := ginutil.ParsePagination(c)
	if size > 200 {
		size = 200
	}
	items, total, err := svc.ListBroadcastDeliveries(c.Request.Context(), id, page, size)
	if err != nil {
		deliveryError(c, err)
		return
	}
	response.SuccessWithPage(c, items, response.BuildPagination(page, size, total))
}

func (h *AdminHandler) RetryDelivery(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil || id == 0 {
		deliveryError(c, broadcastapp.ErrInvalid)
		return
	}
	deliveryID, err := ginutil.ParseParamUint(c, "delivery_id")
	if err != nil || deliveryID == 0 {
		deliveryError(c, broadcastapp.ErrInvalid)
		return
	}
	svc, ok := h.broadcasts.(deliveryAdminService)
	if !ok {
		deliveryError(c, broadcastapp.ErrInvalid)
		return
	}
	if err := svc.RetryBroadcastDelivery(c.Request.Context(), id, deliveryID); err != nil {
		deliveryError(c, err)
		return
	}
	// This is acceptance, not an assertion that the asynchronous send succeeded.
	response.Success(c, gin.H{"broadcast_id": id, "delivery_id": deliveryID, "accepted": true})
}

func RegisterAdminRoutes(admin gin.IRoutes, handler *AdminHandler) {
	admin.GET("/telegram-bot/broadcasts", handler.List)
	admin.GET("/telegram-bot/broadcasts/:id", handler.Get)
	admin.GET("/telegram-bot/broadcasts/:id/deliveries", handler.ListDeliveries)
	admin.POST("/telegram-bot/broadcasts/:id/deliveries/:delivery_id/retry", handler.RetryDelivery)
	admin.POST("/telegram-bot/broadcasts", handler.Create)
	admin.GET("/telegram-bot/users", handler.ListUsers)
}

// ListUsers returns Telegram users selectable as broadcast recipients.
func (h *AdminHandler) ListUsers(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	createdFrom, createdTo, err := ginutil.ParseQueryTimeRange(c, "created_from", "created_to")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	botID, parseErr := strconv.ParseUint(c.Query("bot_id"), 10, 32)
	if parseErr != nil || botID == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", broadcastapp.ErrInvalid)
		return
	}
	items, total, err := h.broadcasts.ListTelegramUsers(broadcastapp.UserQuery{
		BotID:            uint(botID),
		Page:             page,
		PageSize:         pageSize,
		Keyword:          strings.TrimSpace(c.Query("keyword")),
		DisplayName:      strings.TrimSpace(c.Query("display_name")),
		TelegramUsername: strings.TrimSpace(c.Query("telegram_username")),
		TelegramUserID:   strings.TrimSpace(c.Query("telegram_user_id")),
		CreatedFrom:      createdFrom,
		CreatedTo:        createdTo,
	})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.bad_request", err)
		return
	}
	response.SuccessWithPage(c, items, response.BuildPagination(page, pageSize, total))
}
