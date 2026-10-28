package telegramapi

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
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeout                = 65 * time.Second
	DefaultMaxResponseBytes int64 = 4 << 20
	MaxUploadBytes                = 50 << 20
)

// APIError deliberately excludes Telegram descriptions and raw response bodies.
// RetryAfter is Telegram's retry_after value in seconds. No automatic retries
// are performed: retrying sends can duplicate messages.
type APIError struct {
	HTTPStatus int
	ErrorCode  int
	RetryAfter int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram API failure (HTTP %d, code %d, retry_after %d)", e.HTTPStatus, e.ErrorCode, e.RetryAfter)
}

// TransportError contains only safe classifications, never the underlying URL,
// token, body or transport error. Context cancellation remains errors.Is-able.
type TransportError struct{ Timeout bool }

func (e *TransportError) Error() string {
	if e.Timeout {
		return "telegram transport timeout"
	}
	return "telegram transport failure"
}

type Client struct {
	token            string
	baseURL          string
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
}

type Option func(*Client) error

// WithBaseURL accepts an HTTP(S) API root, without /bot<TOKEN>.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) error {
		u, err := url.Parse(baseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("telegram: invalid base URL")
		}
		c.baseURL = strings.TrimRight(baseURL, "/")
		return nil
	}
}

// WithHTTPClient copies the client; redirects are always refused to avoid
// leaking the bot token or request payload to a different endpoint.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) error {
		if client == nil {
			return errors.New("telegram: nil HTTP client")
		}
		clone := *client
		c.httpClient = &clone
		return nil
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) error {
		if timeout <= 0 {
			return errors.New("telegram: timeout must be positive")
		}
		c.timeout = timeout
		return nil
	}
}

func WithMaxResponseBytes(limit int64) Option {
	return func(c *Client) error {
		if limit <= 0 || limit > 64<<20 {
			return errors.New("telegram: response limit must be between 1 and 64 MiB")
		}
		c.maxResponseBytes = limit
		return nil
	}
}

func NewClient(token string, options ...Option) (*Client, error) {
	// Restrict tokens to safe path characters, without requiring production
	// token formatting (test servers can use dummy tokens).
	if token == "" || strings.IndexFunc(token, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '-')
	}) >= 0 {
		return nil, errors.New("telegram: invalid bot token")
	}
	c := &Client{token: token, baseURL: "https://api.telegram.org", httpClient: &http.Client{}, timeout: DefaultTimeout, maxResponseBytes: DefaultMaxResponseBytes}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("telegram: nil option")
		}
		if err := option(c); err != nil {
			return nil, err
		}
	}
	c.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

func (c *Client) callJSON(ctx context.Context, method string, request, result any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return errors.New("telegram: cannot encode request")
	}
	return c.call(ctx, method, "application/json", bytes.NewReader(body), result)
}

func (c *Client) call(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, body)
	if err != nil {
		return errors.New("telegram: cannot build request")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return transportError(ctx, err)
	}
	if int64(len(data)) > c.maxResponseBytes {
		return errors.New("telegram: response exceeds size limit")
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(data, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (decodeErr == nil && !envelope.OK) {
		return &APIError{HTTPStatus: resp.StatusCode, ErrorCode: envelope.ErrorCode, RetryAfter: envelope.Parameters.RetryAfter}
	}
	if decodeErr != nil || len(envelope.Result) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
		return errors.New("telegram: invalid API response")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return errors.New("telegram: invalid API result")
	}
	return nil
}

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("telegram request: %w", ctx.Err())
	}
	var timeout interface{ Timeout() bool }
	return &TransportError{Timeout: errors.As(err, &timeout) && timeout.Timeout()}
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var result User
	err := c.callJSON(ctx, "getMe", struct{}{}, &result)
	return result, err
}

func (c *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var result WebhookInfo
	err := c.callJSON(ctx, "getWebhookInfo", struct{}{}, &result)
	return result, err
}

func (c *Client) GetUpdates(ctx context.Context, request GetUpdatesRequest) ([]Update, error) {
	if request.Timeout < 0 || request.Timeout > 50 || request.Limit < 0 || request.Limit > 100 {
		return nil, errors.New("telegram: invalid update timeout or limit")
	}
	if request.Timeout > 0 && time.Duration(request.Timeout)*time.Second >= c.timeout {
		return nil, errors.New("telegram: client timeout must exceed long-poll timeout")
	}
	var result []Update
	err := c.callJSON(ctx, "getUpdates", request, &result)
	return result, err
}

func (c *Client) DeleteWebhook(ctx context.Context, dropPending bool) error {
	var result bool
	err := c.callJSON(ctx, "deleteWebhook", struct {
		DropPending bool `json:"drop_pending_updates"`
	}{dropPending}, &result)
	if err == nil && !result {
		return errors.New("telegram: webhook deletion refused")
	}
	return err
}

func (c *Client) SendMessage(ctx context.Context, request SendMessageRequest) (Message, error) {
	var result Message
	err := c.callJSON(ctx, "sendMessage", request, &result)
	return result, err
}

// SendPhoto uploads an in-memory PNG, never a local path or remote URL.
func (c *Client) SendPhoto(ctx context.Context, request SendPhotoRequest) (Message, error) {
	if !bytes.HasPrefix(request.PNG, []byte("\x89PNG\r\n\x1a\n")) {
		return Message{}, errors.New("telegram: photo must be PNG")
	}
	return c.sendFile(ctx, "sendPhoto", "photo", "photo.png", "image/png", request.PNG, request.ChatID, request.Caption, request.ParseMode, request.DisableNotification, request.ProtectContent, request.ReplyMarkup)
}

func (c *Client) SendDocument(ctx context.Context, request SendDocumentRequest) (Message, error) {
	if request.Filename == "" || strings.ContainsAny(request.Filename, "\r\n/\\") {
		return Message{}, errors.New("telegram: invalid document filename")
	}
	return c.sendFile(ctx, "sendDocument", "document", request.Filename, "application/octet-stream", request.Data, request.ChatID, request.Caption, request.ParseMode, request.DisableNotification, request.ProtectContent, request.ReplyMarkup)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, request AnswerCallbackQueryRequest) error {
	var result bool
	err := c.callJSON(ctx, "answerCallbackQuery", request, &result)
	if err == nil && !result {
		return errors.New("telegram: callback answer refused")
	}
	return err
}

func (c *Client) sendFile(ctx context.Context, method, field, filename, contentType string, data []byte, chatID int64, caption, parseMode string, silent, protect bool, keyboard *InlineKeyboardMarkup) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if len(data) == 0 || len(data) > MaxUploadBytes {
		return Message{}, errors.New("telegram: invalid upload size")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"chat_id": fmt.Sprint(chatID), "caption": caption, "parse_mode": parseMode, "disable_notification": fmt.Sprint(silent), "protect_content": fmt.Sprint(protect)}
	if keyboard != nil {
		encoded, err := json.Marshal(keyboard)
		if err != nil {
			return Message{}, errors.New("telegram: cannot encode keyboard")
		}
		fields["reply_markup"] = string(encoded)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return Message{}, errors.New("telegram: cannot encode upload")
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": field, "filename": filename}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return Message{}, errors.New("telegram: cannot encode upload")
	}
	if _, err := part.Write(data); err != nil {
		return Message{}, errors.New("telegram: cannot encode upload")
	}
	if err := writer.Close(); err != nil {
		return Message{}, errors.New("telegram: cannot encode upload")
	}
	var result Message
	err = c.call(ctx, method, writer.FormDataContentType(), &body, &result)
	return result, err
}
