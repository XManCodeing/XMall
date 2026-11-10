package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	notifycontract "github.com/XManCodeing/XMall/internal/modules/telegram/notify/contract"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

type telegramSendMessageResponse struct {
	OK         bool `json:"ok"`
	ErrorCode  int  `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Client 通过 Telegram Bot API 发送消息。
type Client struct {
	httpClient *http.Client
}

var _ notifycontract.Sender = (*Client)(nil)

// New 创建 Telegram Bot API 客户端。
func New() *Client {
	return NewWithHTTPClient(&http.Client{Timeout: 6 * time.Second})
}

// NewWithHTTPClient 创建使用指定 HTTP 客户端的 Bot API 客户端。
func NewWithHTTPClient(client *http.Client) *Client {
	if client == nil {
		panic("telegram bot api: http client is nil")
	}
	// Match the platform client: never follow redirects containing credentials.
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &clone}
}

// SendWithBotToken 使用显式 bot token 发送 Telegram 消息。
func (s *Client) SendWithBotToken(ctx context.Context, botToken string, options notifycontract.SendOptions) error {
	chatID := strings.TrimSpace(options.ChatID)
	message := strings.TrimSpace(options.Message)
	botToken = strings.TrimSpace(botToken)
	if chatID == "" || message == "" || botToken == "" {
		return notifycontract.ErrNotifySendFailed
	}

	if strings.TrimSpace(options.AttachmentURL) != "" {
		attachmentURL := strings.TrimSpace(options.AttachmentURL)
		if isTelegramPhotoAttachment(attachmentURL, options.AttachmentDisplayName) {
			if filePath, ok := resolveTelegramAttachmentPath(attachmentURL); ok {
				return s.sendMultipartMedia(ctx, botToken, "sendPhoto", "photo", filePath, options)
			}
			payload := map[string]interface{}{
				"chat_id": chatID,
				"photo":   attachmentURL,
				"caption": message,
			}
			if parseMode := strings.TrimSpace(options.ParseMode); parseMode != "" {
				payload["parse_mode"] = parseMode
			}
			return s.sendJSONRequest(ctx, botToken, "sendPhoto", payload)
		}
		if filePath, ok := resolveTelegramAttachmentPath(attachmentURL); ok {
			return s.sendMultipartMedia(ctx, botToken, "sendDocument", "document", filePath, options)
		}
		payload := map[string]interface{}{
			"chat_id":  chatID,
			"document": attachmentURL,
			"caption":  message,
		}
		if parseMode := strings.TrimSpace(options.ParseMode); parseMode != "" {
			payload["parse_mode"] = parseMode
		}
		return s.sendJSONRequest(ctx, botToken, "sendDocument", payload)
	}

	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     message,
		"disable_web_page_preview": options.DisableWebPagePreview,
	}
	if parseMode := strings.TrimSpace(options.ParseMode); parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	return s.sendJSONRequest(ctx, botToken, "sendMessage", payload)
}

// sendMultipartMedia sends a local attachment; errors contain only fixed stage names, never paths or content.
// botToken and options are request credentials/content; the returned error retains ErrNotifySendFailed.
func (s *Client) sendMultipartMedia(ctx context.Context, botToken, method, fieldName, filePath string, options notifycontract.SendOptions) error {
	file, err := os.Open(filePath)
	if err != nil {
		return notifyFailure("open attachment failed")
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strings.TrimSpace(options.ChatID)); err != nil {
		return notifyFailure("encode chat failed")
	}
	if caption := strings.TrimSpace(options.Message); caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return notifyFailure("encode caption failed")
		}
	}
	if parseMode := strings.TrimSpace(options.ParseMode); parseMode != "" {
		if err := writer.WriteField("parse_mode", parseMode); err != nil {
			return notifyFailure("encode parse mode failed")
		}
	}
	part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return notifyFailure("create attachment part failed")
	}
	if _, err := io.Copy(part, file); err != nil {
		return notifyFailure("read attachment failed")
	}
	if err := writer.Close(); err != nil {
		return notifyFailure("finalize attachment failed")
	}

	requestURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", botToken, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, &body)
	if err != nil {
		return notifyFailure("build request failed")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return s.doRequest(req)
}

// sendJSONRequest encodes payload and sends it using botToken; errors exclude request values.
func (s *Client) sendJSONRequest(ctx context.Context, botToken, method string, payload map[string]interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return notifyFailure("encode request failed")
	}
	requestURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", botToken, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return notifyFailure("build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	return s.doRequest(req)
}

// doRequest executes req and returns only safe platform errors and fixed diagnostics.
// Raw URLs, response descriptions and underlying transport errors never enter the error chain.
func (s *Client) doRequest(req *http.Request) error {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return notifyTransportFailure(req.Context(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, telegramapi.DefaultMaxResponseBytes+1))
	if err != nil {
		return notifyTransportFailure(req.Context(), err)
	}
	if int64(len(body)) > telegramapi.DefaultMaxResponseBytes {
		return notifyFailure("response exceeds size limit")
	}
	var parsed telegramSendMessageResponse
	decodeErr := json.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (decodeErr == nil && !parsed.OK) {
		return fmt.Errorf("%w: %w", notifycontract.ErrNotifySendFailed, &telegramapi.APIError{
			HTTPStatus: resp.StatusCode, ErrorCode: parsed.ErrorCode, RetryAfter: parsed.Parameters.RetryAfter,
		})
	}
	if decodeErr != nil {
		return notifyFailure("parse telegram response failed")
	}
	return nil
}

// notifyFailure accepts a fixed internal stage name and returns the notification failure sentinel.
func notifyFailure(stage string) error {
	return fmt.Errorf("%w: %s", notifycontract.ErrNotifySendFailed, stage)
}

// notifyTransportFailure classifies err without retaining its text or cause; ctx cancellation remains detectable.
func notifyTransportFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%w: %w", notifycontract.ErrNotifySendFailed, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", notifycontract.ErrNotifySendFailed, context.DeadlineExceeded)
	}
	var timeout interface{ Timeout() bool }
	return fmt.Errorf("%w: %w", notifycontract.ErrNotifySendFailed, &telegramapi.TransportError{
		Timeout: errors.As(err, &timeout) && timeout.Timeout(),
	})
}

func resolveTelegramAttachmentPath(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed != nil && parsed.Scheme != "" {
		return "", false
	}

	normalized := strings.TrimPrefix(value, "/")
	normalized = filepath.Clean(normalized)
	if normalized == "." || normalized == "" {
		return "", false
	}
	if normalized == "uploads" || strings.HasPrefix(normalized, "uploads"+string(filepath.Separator)) {
		return normalized, true
	}
	return "", false
}

func isTelegramPhotoAttachment(rawURL, displayName string) bool {
	candidates := []string{
		strings.TrimSpace(displayName),
		strings.TrimSpace(rawURL),
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		value := candidate
		if parsed, err := url.Parse(candidate); err == nil && parsed != nil {
			if parsed.Path != "" {
				value = parsed.Path
			}
		}
		ext := strings.ToLower(strings.TrimSpace(filepath.Ext(value)))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			return true
		}
		if ext == ".gif" {
			return true
		}
		if detected := mime.TypeByExtension(ext); strings.HasPrefix(strings.ToLower(detected), "image/") {
			return true
		}
	}

	return false
}
