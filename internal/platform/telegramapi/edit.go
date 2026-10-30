package telegramapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ValidationError contains only a finite classification, never user content.
type ValidationError struct{ kind string }

func (e *ValidationError) Error() string { return "telegram: " + e.kind }
func (e *ValidationError) Kind() string  { return e.kind }
func invalid(kind string) error          { return &ValidationError{kind: kind} }

func classifyFailure(method string, status, code int, description string) string {
	if status >= 500 || code >= 500 {
		if strings.HasPrefix(method, "editMessage") || method == "deleteMessage" {
			return "retryable"
		}
		return "unknown" // Sending may have succeeded: never advertise safe retry.
	}
	if status == 429 || code == 429 {
		return "retryable"
	}
	if status == 401 || status == 403 || code == 401 || code == 403 {
		return "forbidden"
	}
	if code != 400 && status != 400 {
		return "unknown"
	}
	d := strings.ToLower(description)
	switch {
	case strings.Contains(d, "message is not modified"):
		return "not_modified"
	case strings.Contains(d, "message to edit not found"), strings.Contains(d, "message to delete not found"):
		return "target_missing"
	case strings.Contains(d, "wrong file identifier"), strings.Contains(d, "wrong remote file identifier"), strings.Contains(d, "failed to get http url content"), strings.Contains(d, "wrong type of the web page content"), strings.Contains(d, "photo_invalid"), strings.Contains(d, "image_process_failed"), strings.Contains(d, "photo_invalid_dimensions"), strings.Contains(d, "file is too big"), strings.Contains(d, "invalid file id"):
		return "invalid_media"
	case strings.Contains(d, "can't parse entities"), strings.Contains(d, "message is too long"), strings.Contains(d, "message text is empty"), strings.Contains(d, "caption is too long"), strings.Contains(d, "button_data_invalid"), strings.Contains(d, "unsupported parse_mode"):
		return "invalid_content"
	}
	return "unknown"
}

func validateContent(text, mode string, limit int, keyboard *InlineKeyboardMarkup) error {
	if !utf8.ValidString(text) {
		return invalid("invalid_content")
	}
	// Telegram applies limits after entity parsing; do not reject valid HTML source
	// simply because tags/entities make its wire representation longer.
	if mode == "" && utf8.RuneCountInString(text) > limit {
		return invalid("invalid_content")
	}
	switch mode {
	case "", "HTML", "Markdown", "MarkdownV2":
	default:
		return invalid("invalid_content")
	}
	if keyboard != nil {
		for _, row := range keyboard.InlineKeyboard {
			for _, b := range row {
				if b.Text == "" || (b.CallbackData == "") == (b.URL == "") || len(b.CallbackData) > 64 {
					return invalid("invalid_content")
				}
			}
		}
	}
	return nil
}

func photoSource(data []byte, filename, fileID, remote string) (string, string, string, error) {
	count := 0
	if len(data) > 0 {
		count++
	}
	if fileID != "" {
		count++
	}
	if remote != "" {
		count++
	}
	if count != 1 {
		return "", "", "", invalid("invalid_media")
	}
	if fileID != "" {
		return fileID, "", "", nil
	}
	if remote != "" {
		u, err := url.Parse(remote)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", "", "", invalid("invalid_media")
		}
		return remote, "", "", nil
	}
	if len(data) > 10<<20 {
		return "", "", "", invalid("invalid_media")
	}
	contentType, fallback := "", ""
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		contentType, fallback = "image/png", "photo.png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		contentType, fallback = "image/jpeg", "photo.jpg"
	default:
		return "", "", "", invalid("invalid_media")
	}
	if filename == "" {
		filename = fallback
	}
	if strings.ContainsAny(filename, "\r\n/\\") {
		return "", "", "", invalid("invalid_media")
	}
	return "attach://photo", filename, contentType, nil
}

func (c *Client) EditMessageText(ctx context.Context, request EditMessageTextRequest) (Message, error) {
	if request.MessageID <= 0 || request.Text == "" {
		return Message{}, invalid("invalid_content")
	}
	if err := validateContent(request.Text, request.ParseMode, 4096, request.ReplyMarkup); err != nil {
		return Message{}, err
	}
	var result Message
	err := c.callJSON(ctx, "editMessageText", request, &result)
	return result, err
}
func (c *Client) EditMessageCaption(ctx context.Context, request EditMessageCaptionRequest) (Message, error) {
	if request.MessageID <= 0 {
		return Message{}, invalid("invalid_content")
	}
	if err := validateContent(request.Caption, request.ParseMode, 1024, request.ReplyMarkup); err != nil {
		return Message{}, err
	}
	var result Message
	err := c.callJSON(ctx, "editMessageCaption", request, &result)
	return result, err
}
func (c *Client) EditMessageReplyMarkup(ctx context.Context, request EditMessageReplyMarkupRequest) (Message, error) {
	if request.MessageID <= 0 {
		return Message{}, invalid("invalid_content")
	}
	if request.ReplyMarkup == nil {
		request.ReplyMarkup = &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	}
	if err := validateContent("", "", 4096, request.ReplyMarkup); err != nil {
		return Message{}, err
	}
	var result Message
	err := c.callJSON(ctx, "editMessageReplyMarkup", request, &result)
	return result, err
}
func (c *Client) DeleteMessage(ctx context.Context, request DeleteMessageRequest) error {
	if request.MessageID <= 0 {
		return invalid("invalid_content")
	}
	var result bool
	if err := c.callJSON(ctx, "deleteMessage", request, &result); err != nil {
		return err
	}
	if !result {
		return invalid("unknown")
	}
	return nil
}

// EditMessageMedia also converts text messages to photos, as supported by
// https://core.telegram.org/bots/api#editmessagemedia . No URL is fetched here.
func (c *Client) EditMessageMedia(ctx context.Context, request EditMessageMediaRequest) (Message, error) {
	if request.MessageID <= 0 {
		return Message{}, invalid("invalid_content")
	}
	source, filename, contentType, err := photoSource(request.Data, request.Filename, request.FileID, request.URL)
	if err != nil {
		return Message{}, err
	}
	if err := validateContent(request.Caption, request.ParseMode, 1024, request.ReplyMarkup); err != nil {
		return Message{}, err
	}
	media := struct {
		Type      string `json:"type"`
		Media     string `json:"media"`
		Caption   string `json:"caption"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{"photo", source, request.Caption, request.ParseMode}
	var result Message
	if len(request.Data) == 0 {
		err = c.callJSON(ctx, "editMessageMedia", struct {
			ChatID      int64                 `json:"chat_id"`
			MessageID   int64                 `json:"message_id"`
			Media       any                   `json:"media"`
			ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
		}{request.ChatID, request.MessageID, media, request.ReplyMarkup}, &result)
		return result, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	encoded, _ := json.Marshal(media)
	fields := map[string]string{"chat_id": fmt.Sprint(request.ChatID), "message_id": fmt.Sprint(request.MessageID), "media": string(encoded)}
	if request.ReplyMarkup != nil {
		b, _ := json.Marshal(request.ReplyMarkup)
		fields["reply_markup"] = string(b)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return Message{}, invalid("invalid_content")
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "photo", "filename": filename}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return Message{}, invalid("invalid_media")
	}
	if _, err := part.Write(request.Data); err != nil {
		return Message{}, invalid("invalid_media")
	}
	if err := writer.Close(); err != nil {
		return Message{}, invalid("invalid_media")
	}
	err = c.call(ctx, "editMessageMedia", writer.FormDataContentType(), &body, &result)
	return result, err
}
