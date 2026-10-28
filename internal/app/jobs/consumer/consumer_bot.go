package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/logger"
	channelclientapp "github.com/XManCodeing/XMall/internal/modules/channelclient/application"
	"github.com/XManCodeing/XMall/internal/queue"
	"github.com/XManCodeing/XMall/internal/upstream"

	"github.com/hibiken/asynq"
)

// LegacyNotifyRouter resolves explicit routing or a persisted historical mapping.
// A nil endpoint is allowed only after durable conversion to runtime deliveries.
// Unknown routing must return ErrLegacyNotifyUnmapped, never choose an active bot.
type LegacyNotifyRouter interface {
	Resolve(context.Context, queue.BotNotifyPayload, uint) (*channelclientapp.ActiveEndpoint, error)
	LegacyAllowed(context.Context, uint) (bool, error)
}

type LegacyHistory interface {
	ChannelForNotification(context.Context, queue.BotNotifyPayload) (uint, error)
}
type LegacyConverter interface {
	ConvertLegacyNotification(context.Context, uint, queue.BotNotifyPayload) error
}
type LegacyChannelGuard interface {
	LegacyAllowed(context.Context, uint) (bool, error)
}

// RoutedLegacyNotify never consults active-channel ordering. History resolves old
// queue entries; conversion must durably enqueue before returning success.
type RoutedLegacyNotify struct {
	Channels  *channelclientapp.Service
	Guard     LegacyChannelGuard
	History   LegacyHistory
	Converter LegacyConverter
}

func (r *RoutedLegacyNotify) LegacyAllowed(ctx context.Context, id uint) (bool, error) {
	if r == nil || r.Guard == nil {
		return false, ErrLegacyNotifyUnmapped
	}
	return r.Guard.LegacyAllowed(ctx, id)
}
func (r *RoutedLegacyNotify) Resolve(ctx context.Context, p queue.BotNotifyPayload, channelID uint) (*channelclientapp.ActiveEndpoint, error) {
	if r == nil {
		return nil, ErrLegacyNotifyUnmapped
	}
	if channelID == 0 {
		if r.History == nil {
			return nil, ErrLegacyNotifyUnmapped
		}
		var err error
		channelID, err = r.History.ChannelForNotification(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	if channelID == 0 {
		return nil, ErrLegacyNotifyUnmapped
	}
	allowed, err := r.LegacyAllowed(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		if r.Converter == nil {
			return nil, ErrLegacyNotifyUnmapped
		}
		return nil, r.Converter.ConvertLegacyNotification(ctx, channelID, p)
	}
	if r.Channels == nil {
		return nil, ErrLegacyNotifyUnmapped
	}
	client, err := r.Channels.GetChannelClient(channelID)
	if err != nil {
		return nil, err
	}
	if client == nil || client.Status != 1 || client.ChannelType != "telegram_bot" || client.DeletedAt != nil {
		return nil, ErrLegacyNotifyUnmapped
	}
	secret, err := r.Channels.DecryptChannelSecret(client)
	if err != nil {
		return nil, errors.New("channel credential unavailable")
	}
	if secret == "" {
		return nil, ErrLegacyNotifyUnmapped
	}
	return &channelclientapp.ActiveEndpoint{ClientID: client.ID, ChannelKey: client.ChannelKey, CallbackURL: client.CallbackURL, ChannelSecret: secret}, nil
}

var ErrLegacyNotifyUnmapped = errors.New("legacy bot notification requires routing reconciliation")

func NewLegacyBotNotifyHandler(router LegacyNotifyRouter) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error { return handleRoutedBotNotify(ctx, task, router) }
}
func (c *Consumer) handleBotNotify(ctx context.Context, task *asynq.Task) error {
	return handleRoutedBotNotify(ctx, task, nil)
}
func handleRoutedBotNotify(ctx context.Context, task *asynq.Task, router LegacyNotifyRouter) error {
	if task == nil {
		return nil
	}
	if router == nil {
		return fmt.Errorf("%w: %w", ErrLegacyNotifyUnmapped, asynq.SkipRetry)
	}
	var payload queue.BotNotifyPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		logger.Warnw("worker_bot_notify_unmarshal_failed", "error", err)
		return fmt.Errorf("unmarshal bot notify payload: %w", err)
	}
	if strings.TrimSpace(payload.TelegramUserID) == "" {
		logger.Debugw("worker_bot_notify_skip_invalid", "event_type", payload.EventType, "order_id", payload.OrderID, "telegram_user_id", payload.TelegramUserID, "recharge_no", payload.RechargeNo)
		return nil
	}

	var route struct {
		ChannelID uint `json:"channel_id"`
	}
	if err := json.Unmarshal(task.Payload(), &route); err != nil {
		return err
	}
	channelEndpoint, err := router.Resolve(ctx, payload, route.ChannelID)
	if errors.Is(err, ErrLegacyNotifyUnmapped) {
		return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
	}
	if err != nil {
		return errors.New("legacy bot route resolution failed")
	}
	if channelEndpoint == nil {
		return nil
	}
	if channelEndpoint.ClientID == 0 || channelEndpoint.CallbackURL == "" {
		return fmt.Errorf("%w: %w", ErrLegacyNotifyUnmapped, asynq.SkipRetry)
	}
	if route.ChannelID != 0 && channelEndpoint.ClientID != route.ChannelID {
		return fmt.Errorf("%w: %w", ErrLegacyNotifyUnmapped, asynq.SkipRetry)
	}
	allowed, err := router.LegacyAllowed(ctx, channelEndpoint.ClientID)
	if err != nil {
		return errors.New("legacy channel guard unavailable")
	}
	if !allowed {
		return fmt.Errorf("legacy channel fenced: %w", asynq.SkipRetry)
	}

	timestamp := time.Now().Unix()
	path := "/internal/order-fulfilled"
	requestBody := map[string]interface{}{
		"order_id":         payload.OrderID,
		"telegram_user_id": payload.TelegramUserID,
	}
	switch payload.EventType {
	case queue.BotNotifyEventOrderPaid:
		if payload.OrderID == 0 {
			logger.Debugw("worker_bot_notify_skip_invalid", "event_type", payload.EventType, "order_id", payload.OrderID, "telegram_user_id", payload.TelegramUserID)
			return nil
		}
		path = "/internal/order-paid"
	case "", queue.BotNotifyEventOrderFulfilled:
		if payload.OrderID == 0 {
			logger.Debugw("worker_bot_notify_skip_invalid", "event_type", payload.EventType, "order_id", payload.OrderID, "telegram_user_id", payload.TelegramUserID)
			return nil
		}
	case queue.BotNotifyEventWalletRechargeSucceeded:
		if strings.TrimSpace(payload.RechargeNo) == "" {
			logger.Debugw("worker_bot_notify_skip_invalid", "event_type", payload.EventType, "telegram_user_id", payload.TelegramUserID, "recharge_no", payload.RechargeNo)
			return nil
		}
		path = "/internal/wallet-recharge-succeeded"
		requestBody = map[string]interface{}{
			"recharge_no":      payload.RechargeNo,
			"telegram_user_id": payload.TelegramUserID,
			"amount":           payload.Amount,
			"currency":         payload.Currency,
		}
	default:
		logger.Debugw("worker_bot_notify_skip_unknown_event", "event_type", payload.EventType)
		return nil
	}
	body, _ := json.Marshal(requestBody)
	signature := upstream.Sign(channelEndpoint.ChannelSecret, "POST", path, timestamp, body)

	requestURL, err := buildBotNotifyRequestURL(channelEndpoint.CallbackURL, path)
	if err != nil {
		return fmt.Errorf("build bot notify request url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create bot notify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Dujiao-Next-Channel-Key", channelEndpoint.ChannelKey)
	req.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("Dujiao-Next-Channel-Signature", signature)

	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bot notify outcome unknown: %w", asynq.SkipRetry)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logger.Debugw("worker_bot_notify_sent",
			"order_id", payload.OrderID, "telegram_user_id", payload.TelegramUserID)
		return nil
	}

	// Legacy callbacks have no durable remote idempotency receipt. A 5xx or
	// redirect can follow an accepted send; archive for reconciliation instead
	// of blindly sending again. Explicit 429 rejection is safe to retry.
	if resp.StatusCode == http.StatusTooManyRequests {
		return errors.New("legacy bot rate limited")
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("legacy bot rejected request (%d): %w", resp.StatusCode, asynq.SkipRetry)
	}
	return fmt.Errorf("legacy bot outcome unknown (%d): %w", resp.StatusCode, asynq.SkipRetry)
}

// buildBotNotifyRequestURL 构建 Bot 回调请求URL并重置查询参数。
func buildBotNotifyRequestURL(rawURL string, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", errors.New("invalid callback url")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("invalid callback url")
	}

	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

// handleTelegramBroadcast 处理 Telegram 群发任务。
func (c *Consumer) handleTelegramBroadcast(ctx context.Context, task *asynq.Task) error {
	if c == nil || task == nil {
		return nil
	}
	var payload queue.TelegramBroadcastPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		logger.Warnw("worker_telegram_broadcast_unmarshal_failed", "error", err)
		return err
	}
	if payload.BroadcastID == 0 {
		logger.Debugw("worker_telegram_broadcast_skip_invalid")
		return nil
	}
	if c.TelegramBroadcastService == nil {
		return nil
	}
	return c.TelegramBroadcastService.ProcessBroadcast(ctx, payload.BroadcastID)
}
