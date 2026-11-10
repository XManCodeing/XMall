package application

import (
	"errors"
	"fmt"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	"sort"
	"strings"
	"time"

	fulfillmentcontract "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	externalidentitycontract "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/logger"
	cardsecretdomain "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// Service 编排人工交付与自动交付。
type Service struct {
	orderStore            ordercontract.Store
	fulfillmentRepo       fulfillmentcontract.Store
	orderQueue            ordercontract.Queue
	botNotifier           BotNotifier
	settingService        *settingsapp.Service
	defaultEmailConfig    config.EmailConfig
	downstreamCallbackSvc DownstreamCallbackEnqueuer
	userOAuthIdentityRepo externalidentitycontract.Store
}

type BotNotifier interface {
	EnqueueOrderFulfilled(telegramUserID string, orderID uint) error
}

type DownstreamCallbackEnqueuer interface {
	EnqueueCallback(orderID uint)
}

// SetDownstreamCallbackService 设置下游回调服务（解决循环依赖）
func (s *Service) SetDownstreamCallbackService(svc DownstreamCallbackEnqueuer) {
	s.downstreamCallbackSvc = svc
}

// Options 汇总交付用例依赖。
type Options struct {
	OrderStore            ordercontract.Store
	FulfillmentStore      fulfillmentcontract.Store
	OrderQueue            ordercontract.Queue
	BotNotifier           BotNotifier
	SettingService        *settingsapp.Service
	DefaultEmailConfig    config.EmailConfig
	ExternalIdentityStore externalidentitycontract.Store
}

// New 创建交付服务。
func New(
	opts Options,
) *Service {
	return &Service{
		orderStore:            opts.OrderStore,
		fulfillmentRepo:       opts.FulfillmentStore,
		orderQueue:            opts.OrderQueue,
		botNotifier:           opts.BotNotifier,
		settingService:        opts.SettingService,
		defaultEmailConfig:    opts.DefaultEmailConfig,
		userOAuthIdentityRepo: opts.ExternalIdentityStore,
	}
}

// CreateManualInput 创建人工交付输入
type CreateManualInput struct {
	OrderID      uint
	AdminID      uint
	Payload      string
	DeliveryData jsonmap.JSON
	DeliveredAt  *time.Time
}

// CreateManual 创建人工交付
func (s *Service) CreateManual(input CreateManualInput) (*fulfillmentdomain.Fulfillment, error) {
	if input.OrderID == 0 || input.AdminID == 0 {
		return nil, ErrFulfillmentInvalid
	}
	payload := strings.TrimSpace(input.Payload)
	deliveryData := normalizeManualDeliveryData(input.DeliveryData)
	if payload == "" && len(deliveryData) == 0 {
		return nil, ErrFulfillmentInvalid
	}
	if payload == "" {
		payload = buildManualDeliveryPayload(deliveryData)
	}
	ftype := constants.FulfillmentTypeManual

	order, err := s.orderStore.GetByID(input.OrderID)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if order.ParentID == nil && len(order.Children) > 0 {
		return nil, ErrFulfillmentInvalid
	}
	if order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling {
		return nil, ErrOrderStatusInvalid
	}

	now := time.Now()
	deliveredAt := input.DeliveredAt
	if deliveredAt == nil {
		deliveredAt = &now
	}

	var created *fulfillmentdomain.Fulfillment
	var completed bool
	err = s.orderStore.WithinTransaction(func(tx ordercontract.Transaction) error {
		if err := prepareOrdinary(tx, order, ftype); err != nil {
			return err
		}

		fulfillment := &fulfillmentdomain.Fulfillment{
			OrderID:       input.OrderID,
			Type:          ftype,
			Status:        constants.FulfillmentStatusDelivered,
			Payload:       payload,
			LogisticsJSON: deliveryData,
			DeliveredBy:   &input.AdminID,
			DeliveredAt:   deliveredAt,
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if err := saveOrdinary(tx, fulfillment); err != nil {
			return ErrFulfillmentCreateFailed
		}
		var finishErr error
		completed, finishErr = completeOrdinary(tx, order, fulfillment)
		if finishErr != nil {
			return finishErr
		}
		created = fulfillment
		if !completed {
			return nil
		}
		return appendFulfilledEvent(tx, order, fulfillment)
	})
	if err != nil {
		if errors.Is(err, ErrFulfillmentExists) {
			return nil, ErrFulfillmentExists
		}
		if errors.Is(err, ErrOrderUpdateFailed) {
			return nil, ErrOrderUpdateFailed
		}
		return nil, ErrFulfillmentCreateFailed
	}
	if !completed {
		return created, nil
	}
	s.notifyCompleted(order)
	return created, nil
}

// CreateAuto 自动交付
func (s *Service) CreateAuto(orderID uint) (*fulfillmentdomain.Fulfillment, error) {
	if orderID == 0 {
		return nil, ErrFulfillmentInvalid
	}

	order, err := s.orderStore.GetByID(orderID)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if order.ParentID == nil && len(order.Children) > 0 {
		return nil, ErrFulfillmentInvalid
	}
	if order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling {
		return nil, ErrOrderStatusInvalid
	}
	if len(order.Items) == 0 {
		return nil, ErrFulfillmentInvalid
	}

	now := time.Now()
	var fulfillment *fulfillmentdomain.Fulfillment
	var completed bool
	err = s.orderStore.WithinTransaction(func(tx ordercontract.Transaction) error {
		if err := prepareOrdinary(tx, order, constants.FulfillmentTypeAuto); err != nil {
			return err
		}

		secrets, err := selectAutoSecrets(tx, order)
		if err != nil {
			return err
		}

		ids := make([]uint, 0, len(secrets))
		secretLines := make([]string, 0, len(secrets))
		for _, secret := range secrets {
			ids = append(ids, secret.ID)
			secretLines = append(secretLines, secret.Secret)
		}

		affected, err := tx.CardSecrets().MarkUsed(ids, orderID, now)
		if err != nil {
			return err
		}
		if int(affected) != len(ids) {
			return ErrCardSecretInsufficient
		}

		payload := strings.Join(secretLines, "\n")
		fulfillment = &fulfillmentdomain.Fulfillment{
			OrderID:     orderID,
			Type:        constants.FulfillmentTypeAuto,
			Status:      constants.FulfillmentStatusDelivered,
			Payload:     payload,
			DeliveredAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := saveOrdinary(tx, fulfillment); err != nil {
			return ErrFulfillmentCreateFailed
		}
		var finishErr error
		completed, finishErr = completeOrdinary(tx, order, fulfillment)
		if finishErr != nil {
			return finishErr
		}
		if !completed {
			return nil
		}
		return appendFulfilledEvent(tx, order, fulfillment)
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrFulfillmentExists):
			return nil, ErrFulfillmentExists
		case errors.Is(err, ErrCardSecretInsufficient):
			return nil, ErrCardSecretInsufficient
		case errors.Is(err, ErrOrderUpdateFailed):
			return nil, ErrOrderUpdateFailed
		case errors.Is(err, ErrFulfillmentNotAuto):
			return nil, ErrFulfillmentNotAuto
		default:
			return nil, ErrFulfillmentCreateFailed
		}
	}
	if !completed {
		return fulfillment, nil
	}
	s.notifyCompleted(order)
	return fulfillment, nil
}

// selectAutoSecrets 仅给已过滤的普通卡密项选卡；tx 为真实事务，返回预留/可用卡密或库存错误。
func selectAutoSecrets(tx ordercontract.Transaction, order *orderdomain.Order) ([]cardsecretdomain.Secret, error) {
	repo := tx.CardSecrets()
	reserved, err := repo.ListByOrderAndStatus(order.ID, cardsecretdomain.StatusReserved)
	if err != nil {
		return nil, err
	}
	byKey := map[string][]cardsecretdomain.Secret{}
	for _, secret := range reserved {
		key := orderdomain.ItemKey(secret.ProductID, secret.SKUID)
		byKey[key] = append(byKey[key], secret)
	}
	var secrets []cardsecretdomain.Secret
	for _, item := range order.Items {
		if item.ProductID == 0 || item.Quantity <= 0 {
			return nil, ErrFulfillmentInvalid
		}
		key := orderdomain.ItemKey(item.ProductID, item.SKUID)
		cached := byKey[key]
		take := min(len(cached), item.Quantity)
		selected := append([]cardsecretdomain.Secret(nil), cached[:take]...)
		byKey[key] = cached[take:]
		if len(selected) < item.Quantity {
			rows, err := repo.ListAvailableByProduct(item.ProductID, item.SKUID, item.Quantity-len(selected))
			if err != nil {
				return nil, err
			}
			selected = append(selected, rows...)
		}
		if len(selected) < item.Quantity {
			return nil, ErrCardSecretInsufficient
		}
		secrets = append(secrets, selected...)
	}
	return secrets, nil
}

// notifyCompleted 在提交后读取真实聚合状态并发送通知；失败只记日志，不回滚权益。
// order 为刚完成的叶单，不在事务中调用本函数，也不猜测混合订单为 completed。
func (s *Service) notifyCompleted(order *orderdomain.Order) {
	notifyOrderID := order.ID
	if order.ParentID != nil {
		notifyOrderID = *order.ParentID
	}
	target, err := s.orderStore.GetByID(notifyOrderID)
	if err != nil || target == nil {
		logger.Warnw("fulfillment_notify_order_fetch_failed", "order_id", notifyOrderID, "error", err)
		return
	}
	if s.orderQueue != nil {
		if _, err := orderapp.EnqueueStatusEmailTaskIfEligible(s.orderStore, s.orderQueue, s.settingService, s.defaultEmailConfig, notifyOrderID, target.Status); err != nil {
			logger.Warnw("fulfillment_enqueue_status_email_failed", "order_id", notifyOrderID, "status", target.Status, "error", err)
		}
	}
	go s.NotifyBotOrderFulfilled(order.UserID, notifyOrderID)
	if s.downstreamCallbackSvc != nil {
		s.downstreamCallbackSvc.EnqueueCallback(order.ID)
	}
}

// NotifyBotOrderFulfilled 查找用户 Telegram 绑定并入队通知任务。
func (s *Service) NotifyBotOrderFulfilled(userID, orderID uint) {
	// Once transactional events are enabled, even website destinations must be
	// resolved there; a login-identity fallback bypasses private-contact checks.
	if delivery.From(s.orderStore) != nil {
		return
	}
	if s.botNotifier == nil || userID == 0 || s.userOAuthIdentityRepo == nil {
		return
	}

	identity, err := s.userOAuthIdentityRepo.GetByUserProvider(userID, "telegram")
	if err != nil {
		logger.Warnw("fulfillment_notify_bot_fetch_identity_failed",
			"order_id", orderID, "user_id", userID, "error", err)
		return
	}
	if identity == nil || strings.TrimSpace(identity.ProviderUserID) == "" {
		return
	}

	if err := s.botNotifier.EnqueueOrderFulfilled(strings.TrimSpace(identity.ProviderUserID), orderID); err != nil {
		logger.Warnw("fulfillment_notify_bot_enqueue_failed",
			"order_id", orderID, "user_id", userID, "error", err)
	}
}

func normalizeManualDeliveryData(raw jsonmap.JSON) jsonmap.JSON {
	if len(raw) == 0 {
		return jsonmap.JSON{}
	}
	normalized := jsonmap.JSON{}
	note := strings.TrimSpace(toStringValue(raw["note"]))
	if note != "" {
		normalized["note"] = note
	}
	if entriesRaw, ok := raw["entries"]; ok {
		entries := normalizeManualDeliveryEntries(entriesRaw)
		if len(entries) > 0 {
			normalized["entries"] = entries
		}
	}

	keys := make([]string, 0, len(raw))
	for key := range raw {
		if key == "note" || key == "entries" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := normalizeManualDeliveryPrimitive(raw[key])
		if value == nil {
			continue
		}
		normalized[key] = value
	}

	return normalized
}

func normalizeManualDeliveryEntries(raw interface{}) []jsonmap.JSON {
	appendEntry := func(entries []jsonmap.JSON, row map[string]interface{}) []jsonmap.JSON {
		key := strings.TrimSpace(toStringValue(row["key"]))
		value := strings.TrimSpace(toStringValue(row["value"]))
		if key == "" && value == "" {
			return entries
		}
		entry := jsonmap.JSON{}
		if key != "" {
			entry["key"] = key
		}
		if value != "" {
			entry["value"] = value
		}
		return append(entries, entry)
	}

	entries := make([]jsonmap.JSON, 0)
	switch value := raw.(type) {
	case []jsonmap.JSON:
		for _, item := range value {
			entries = appendEntry(entries, item)
		}
		return entries
	case []map[string]interface{}:
		for _, item := range value {
			entries = appendEntry(entries, item)
		}
		return entries
	case []interface{}:
		for _, item := range value {
			row, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			entries = appendEntry(entries, row)
		}
		return entries
	default:
		return nil
	}
}

func normalizeManualDeliveryPrimitive(raw interface{}) interface{} {
	switch value := raw.(type) {
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil
		}
		return text
	case bool:
		return value
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return value
	default:
		return nil
	}
}

func buildManualDeliveryPayload(data jsonmap.JSON) string {
	if len(data) == 0 {
		return ""
	}
	lines := make([]string, 0, 8)
	if note, ok := data["note"].(string); ok && strings.TrimSpace(note) != "" {
		lines = append(lines, note)
	}
	if entries, ok := data["entries"].([]jsonmap.JSON); ok {
		for _, item := range entries {
			key := strings.TrimSpace(toStringValue(item["key"]))
			value := strings.TrimSpace(toStringValue(item["value"]))
			if key == "" && value == "" {
				continue
			}
			if key == "" {
				lines = append(lines, value)
				continue
			}
			if value == "" {
				lines = append(lines, key)
				continue
			}
			lines = append(lines, fmt.Sprintf("%s: %s", key, value))
		}
	}

	extraKeys := make([]string, 0, len(data))
	for key := range data {
		if key == "note" || key == "entries" {
			continue
		}
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		valueText := strings.TrimSpace(toStringValue(data[key]))
		if valueText == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", key, valueText))
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func toStringValue(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, bool:
		return fmt.Sprintf("%v", value)
	default:
		return ""
	}
}
