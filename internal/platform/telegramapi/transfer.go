package telegramapi

import (
	"context"
	"strings"
	"unicode/utf16"
)

// TransferContent reuses Telegram file IDs only; this client never fetches or
// uploads customer media. Known sizes are conservatively capped at 10 MiB for
// photos and 50 MiB otherwise. Telegram still enforces format-specific limits.
type TransferContent struct {
	Kind                string
	Text                string
	Entities            []MessageEntity
	FileID              string
	FileSize            int64
	Caption             string
	CaptionEntities     []MessageEntity
	HasProtectedContent bool
}

// TransferRequest is independent of the legacy upload/shopping request types.
// ReplyMessageID refers to the destination chat and must be authorized upstream.
type TransferRequest struct {
	SourceChatID     int64
	SourceMessageIDs []int64
	ChatID           int64
	ThreadID         int64
	ReplyMessageID   int64
	ProtectContent   bool
	Contents         []TransferContent
}

// UncertainResultError means the request may have taken effect. Callers must
// retain any returned messages and reconcile, never automatically resend.
type UncertainResultError struct{}

func (*UncertainResultError) Error() string   { return "telegram: uncertain operation result" }
func (*UncertainResultError) Uncertain() bool { return true }

// Transfer sends one typed message or one ordered album, with no fallback and
// no retries. On an incomplete response, observed messages accompany an error.
func (client *Client) Transfer(ctx context.Context, request TransferRequest) ([]Message, error) {
	if request.ChatID == 0 || request.ThreadID < 0 || (request.ChatID > 0 && request.ThreadID != 0) || request.ReplyMessageID < 0 || len(request.Contents) < 1 || len(request.Contents) > 10 {
		return nil, invalid("invalid_content")
	}
	for _, content := range request.Contents {
		if err := validateTransferContent(content); err != nil {
			return nil, err
		}
	}
	if request.SourceChatID != 0 || len(request.SourceMessageIDs) != 0 {
		return client.forwardTransfer(ctx, request)
	}
	payload := map[string]any{"chat_id": request.ChatID, "message_thread_id": request.ThreadID, "protect_content": request.ProtectContent}
	if request.ReplyMessageID > 0 {
		payload["reply_parameters"] = map[string]any{"message_id": request.ReplyMessageID, "allow_sending_without_reply": false}
	}
	if len(request.Contents) > 1 {
		return client.transferAlbum(ctx, request.Contents, payload)
	}
	content := request.Contents[0]
	methods := map[string]string{"text": "sendMessage", "photo": "sendPhoto", "video": "sendVideo", "voice": "sendVoice", "audio": "sendAudio", "document": "sendDocument", "animation": "sendAnimation", "sticker": "sendSticker"}
	if content.Kind == "text" {
		payload["text"], payload["entities"] = content.Text, content.Entities
	} else {
		payload[content.Kind] = content.FileID
		if content.Kind != "sticker" {
			payload["caption"], payload["caption_entities"] = content.Caption, content.CaptionEntities
		}
	}
	var result Message
	if err := client.callJSON(ctx, methods[content.Kind], payload, &result); err != nil {
		return nil, err
	}
	messages := []Message{result}
	return messages, validateTransferResult(messages, 1, request.ChatID, request.ThreadID)
}

// forwardTransfer preserves Telegram's native attribution and album grouping.
// Source IDs are authorized upstream; this boundary rejects malformed routes and
// unordered IDs. One HTTP attempt only: protected/partial/timeout never fallback.
func (client *Client) forwardTransfer(ctx context.Context, request TransferRequest) ([]Message, error) {
	if request.SourceChatID <= 0 || request.ChatID >= 0 || request.ThreadID <= 0 || request.ReplyMessageID != 0 || len(request.SourceMessageIDs) != len(request.Contents) {
		return nil, invalid("invalid_content")
	}
	previous := int64(0)
	for _, id := range request.SourceMessageIDs {
		if id <= previous {
			return nil, invalid("invalid_content")
		}
		previous = id
	}
	payload := map[string]any{"chat_id": request.ChatID, "message_thread_id": request.ThreadID, "from_chat_id": request.SourceChatID, "protect_content": request.ProtectContent}
	if len(request.SourceMessageIDs) == 1 {
		payload["message_id"] = request.SourceMessageIDs[0]
		var message Message
		if err := client.callJSON(ctx, "forwardMessage", payload, &message); err != nil {
			return nil, err
		}
		messages := []Message{message}
		return messages, validateTransferResult(messages, 1, request.ChatID, request.ThreadID)
	}
	payload["message_ids"] = request.SourceMessageIDs
	// forwardMessages returns MessageId, not Message. Route metadata is absent.
	var ids []struct {
		MessageID int64 `json:"message_id"`
	}
	if err := client.callJSON(ctx, "forwardMessages", payload, &ids); err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(ids))
	for _, item := range ids {
		messages = append(messages, Message{MessageID: item.MessageID, Chat: Chat{ID: request.ChatID}, MessageThreadID: request.ThreadID})
	}
	return messages, validateTransferResult(messages, len(request.Contents), request.ChatID, request.ThreadID)
}

// transferAlbum validates Telegram's photo/video, audio-only and document-only
// grouping rules. Result order is preserved even when the response is partial.
func (client *Client) transferAlbum(ctx context.Context, contents []TransferContent, payload map[string]any) ([]Message, error) {
	group := ""
	items := make([]map[string]any, 0, len(contents))
	for _, content := range contents {
		category := content.Kind
		switch category {
		case "photo", "video":
			category = "visual"
		case "audio", "document":
		default:
			return nil, invalid("invalid_media")
		}
		if group != "" && group != category {
			return nil, invalid("invalid_media")
		}
		group = category
		items = append(items, map[string]any{"type": content.Kind, "media": content.FileID, "caption": content.Caption, "caption_entities": content.CaptionEntities})
	}
	payload["media"] = items
	var messages []Message
	if err := client.callJSON(ctx, "sendMediaGroup", payload, &messages); err != nil {
		return messages, err
	}
	return messages, validateTransferResult(messages, len(contents), payload["chat_id"].(int64), payload["message_thread_id"].(int64))
}

// validateTransferResult rejects missing, duplicate or wrong-route IDs without
// inferring that absent messages were not sent. Telegram may omit thread metadata.
func validateTransferResult(messages []Message, expected int, chatID, threadID int64) error {
	if len(messages) != expected {
		return &UncertainResultError{}
	}
	seen := make(map[int64]bool, len(messages))
	for _, message := range messages {
		if message.MessageID <= 0 || seen[message.MessageID] || message.Chat.ID != chatID || (message.MessageThreadID != 0 && message.MessageThreadID != threadID) {
			return &UncertainResultError{}
		}
		seen[message.MessageID] = true
	}
	return nil
}

// validateTransferContent rejects protected sources and ambiguous type/field
// combinations before HTTP. UTF-16 entity offsets are checked against boundaries.
func validateTransferContent(content TransferContent) error {
	if content.HasProtectedContent {
		return invalid("protected_content")
	}
	if content.Kind == "text" {
		if content.Text == "" || content.FileID != "" || content.FileSize != 0 || content.Caption != "" || len(content.CaptionEntities) != 0 {
			return invalid("invalid_content")
		}
		return validateTransferEntities(content.Text, content.Entities, 4096)
	}
	switch content.Kind {
	case "photo", "video", "voice", "audio", "document", "animation", "sticker":
	default:
		return invalid("invalid_media")
	}
	limit := int64(MaxUploadBytes)
	if content.Kind == "photo" {
		limit = 10 << 20
	}
	if content.Text != "" || len(content.Entities) != 0 || content.FileSize < 0 || content.FileSize > limit || !validTransferFileID(content.FileID) {
		return invalid("invalid_media")
	}
	if content.Kind == "sticker" && (content.Caption != "" || len(content.CaptionEntities) != 0) {
		return invalid("invalid_content")
	}
	return validateTransferEntities(content.Caption, content.CaptionEntities, 1024)
}

// validTransferFileID excludes URL, attach:// and local-file source injection.
func validTransferFileID(fileID string) bool {
	return len(fileID) > 0 && len(fileID) <= 1024 && strings.IndexFunc(fileID, func(char rune) bool {
		return !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-')
	}) == -1
}

// validateTransferEntities validates length and supported entity metadata without
// treating customer text as a parse-mode template or silently dropping entities.
func validateTransferEntities(text string, entities []MessageEntity, limit int) error {
	if err := validateContent(text, "", limit, nil); err != nil {
		return err
	}
	boundaries := map[int]bool{0: true}
	length := 0
	for _, char := range text {
		length += utf16.RuneLen(char)
		boundaries[length] = true
	}
	if len(entities) > 100 {
		return invalid("invalid_content")
	}
	for _, entity := range entities {
		if entity.Offset < 0 || entity.Length <= 0 || entity.Offset > length || entity.Length > length-entity.Offset || !boundaries[entity.Offset] || !boundaries[entity.Offset+entity.Length] {
			return invalid("invalid_content")
		}
		switch entity.Type {
		case "mention", "hashtag", "cashtag", "bot_command", "url", "email", "phone_number", "bold", "italic", "underline", "strikethrough", "spoiler", "blockquote", "expandable_blockquote", "code", "pre":
		case "text_link":
			if entity.URL == "" {
				return invalid("invalid_content")
			}
		case "text_mention":
			if entity.User == nil || entity.User.ID <= 0 {
				return invalid("invalid_content")
			}
		case "custom_emoji":
			if entity.CustomEmojiID == "" {
				return invalid("invalid_content")
			}
		default:
			return invalid("invalid_content")
		}
	}
	return nil
}
