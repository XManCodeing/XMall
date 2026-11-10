package botapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

// validSupportGateCleanup permits only deletion of an explicitly authorized private challenge.
// message must contain no media, text or unrelated capabilities; runtime proves persisted ownership.
func validSupportGateCleanup(message c.Message) bool {
	if message.ChatID <= 0 || message.TargetMessageID <= 0 || !strings.HasPrefix(message.BusinessRef, "support:gate-cleanup:") {
		return false
	}
	expected := c.Message{TargetType: c.TargetSupportPrivate, BusinessRef: message.BusinessRef, ChatID: message.ChatID, Purpose: "support_gate_cleanup", Operation: "delete", TargetMessageID: message.TargetMessageID}
	return reflect.DeepEqual(expected, message)
}

// validSupportGate permits the existing text notice or generated PNG challenge.
// It never enables arbitrary remote media; the final authorizer binds exact bytes.
func validSupportGate(message c.Message) bool {
	if message.ChatID <= 0 || message.BusinessRef == "" || message.Purpose != "support_gate" || message.Operation != "send" || message.ParseMode != "" || message.FileID != "" || message.PhotoURL != "" || message.ForceDocument {
		return false
	}
	if message.TargetKind == "text" {
		return len(message.Photo) == 0 && len([]rune(message.Text)) <= 4000
	}
	return message.TargetKind == "photo" && len(message.Photo) <= 10<<20 && bytes.HasPrefix(message.Photo, []byte("\x89PNG\r\n\x1a\n")) && len([]rune(message.Text)) <= 1024
}

// validSupportControl permits only bounded text and actor-bound operation callbacks.
// Runtime's final authorizer must independently prove the persisted exact envelope.
func validSupportControl(message c.Message) bool {
	if message.ChatID >= 0 || message.ThreadID <= 0 || message.RouteVersion == 0 || !strings.HasPrefix(message.BusinessRef, "support:control:") || len(message.BusinessRef) != len("support:control:")+24 || message.Operation != "send" || message.TargetKind != "text" || message.Text == "" || len([]rune(message.Text)) > 1000 || message.ParseMode != "" || message.Transfer != nil || message.FileID != "" || message.PhotoURL != "" || len(message.Photo) > 0 || message.ForceDocument || message.OwnerUserID != 0 || message.IdentityStamp != "" || message.TargetMessageID != 0 || len(message.Buttons) == 0 || len(message.Buttons) > 3 {
		return false
	}
	for _, row := range message.Buttons {
		// Manual menus have one action per row; daily identity cards have four.
		if len(row) != 1 && !(len(message.Buttons) == 1 && len(row) == 4) {
			return false
		}
		for _, button := range row {
			if button.Text == "" || len([]rune(button.Text)) > 64 || button.URL != "" || button.Style != "" || !strings.HasPrefix(button.Data, "support:op:") || len(button.Data) != len("support:op:")+24 {
				return false
			}
		}
	}
	return true
}

var _ c.TransferAPI = Adapter{}
var _ c.ForumTopicAPI = Adapter{}

// Transfer implements the optional customer-support transport. It validates the
// explicit target but does not replace application-level actor/route authorization.
// It returns each observed message ID, including partial uncertain album results.
func (adapter Adapter) Transfer(ctx context.Context, token string, request c.TransferRequest) (c.TransferResult, error) {
	if request.BusinessRef == "" || request.RouteVersion == 0 ||
		(request.TargetType != c.TargetSupportPrivate && request.TargetType != c.TargetSupportGroup) ||
		(request.TargetType == c.TargetSupportPrivate && (request.ChatID <= 0 || request.ThreadID != 0)) ||
		(request.TargetType == c.TargetSupportGroup && (request.ChatID >= 0 || request.ThreadID <= 0)) {
		return c.TransferResult{}, &sendError{code: 400, kind: "unsupported_target"}
	}
	client, err := adapter.client(token)
	if err != nil {
		return c.TransferResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	wire := api.TransferRequest{SourceChatID: request.SourceChatID, SourceMessageIDs: request.SourceMessageIDs, ChatID: request.ChatID, ThreadID: request.ThreadID, ReplyMessageID: request.ReplyMessageID, ProtectContent: true}
	for _, content := range request.Contents {
		wire.Contents = append(wire.Contents, api.TransferContent{Kind: content.Kind, Text: content.Text,
			Entities: transferEntities(content.Entities), FileID: content.FileID, FileSize: content.FileSize,
			Caption: content.Caption, CaptionEntities: transferEntities(content.CaptionEntities), HasProtectedContent: content.HasProtectedContent})
	}
	messages, err := client.Transfer(ctx, wire)
	result := c.TransferResult{Messages: make([]c.DeliveryResult, 0, len(messages))}
	for index, message := range messages {
		kind := messageKind(message)
		if request.SourceChatID != 0 && index < len(request.Contents) {
			kind = request.Contents[index].Kind
		}
		result.Messages = append(result.Messages, c.DeliveryResult{MessageID: message.MessageID, MessageKind: kind, FileID: transferFileID(message)})
	}
	return result, supportOperationError("transfer", err)
}

// transferEntities maps explicit UTF-16 entities without parse-mode conversion.
func transferEntities(entities []c.MessageEntity) []api.MessageEntity {
	result := make([]api.MessageEntity, 0, len(entities))
	for _, entity := range entities {
		mapped := api.MessageEntity{Type: entity.Type, Offset: entity.Offset, Length: entity.Length, URL: entity.URL, Language: entity.Language, CustomEmojiID: entity.CustomEmojiID}
		if user := entity.User; user != nil {
			mapped.User = &api.User{ID: user.ID, IsBot: user.IsBot, FirstName: user.FirstName, LastName: user.LastName, Username: user.Username, LanguageCode: user.LanguageCode}
		}
		result = append(result, mapped)
	}
	return result
}

// transferFileID returns the delivered type's reusable ID, selecting a large photo.
func transferFileID(message api.Message) string {
	if messageKind(message) == "photo" {
		return message.PhotoFileID()
	}
	for _, media := range mediaRefs(message) {
		if media.Type == messageKind(message) {
			return media.FileID
		}
	}
	return ""
}

// supportOperationError exposes only finite safe classifications and logs no
// token, user content or file ID. Network/5xx/invalid results remain uncertain.
func supportOperationError(operation string, err error) error {
	result := classifyOperationError(err)
	if result == nil {
		return nil
	}
	slog.Warn("telegram_support_operation_failed", "operation", operation, "kind", result.Kind(), "status", result.code, "uncertain", result.unknown)
	return result
}

// classifyOperationError maps err to safe shared transport categories, returning
// nil on success. It never retains the original error, URL, token or response.
func classifyOperationError(err error) *sendError {
	if err == nil {
		return nil
	}
	result := &sendError{unknown: true, kind: "unknown"}
	var upstream *api.APIError
	var validation *api.ValidationError
	if errors.As(err, &validation) {
		result = &sendError{code: 400, kind: validation.Kind()}
	} else if errors.As(err, &upstream) {
		code := upstream.ErrorCode
		if code == 0 {
			code = upstream.HTTPStatus
		}
		result = &sendError{code: code, delay: upstream.RetryAfter, kind: upstream.Kind(), unknown: code >= 500 || upstream.HTTPStatus >= 500 || code < 400}
	}
	return result
}

// CreateForumTopic performs one creation; callers own persisted claims/recovery.
func (adapter Adapter) CreateForumTopic(ctx context.Context, token string, request c.ForumTopicRequest) (c.ForumTopicResult, error) {
	client, err := adapter.client(token)
	if err != nil {
		return c.ForumTopicResult{}, &sendError{code: 400, kind: "invalid_content"}
	}
	result, err := client.CreateForumTopic(ctx, api.ForumTopicRequest{ChatID: request.ChatID, ThreadID: request.ThreadID, Name: request.Name})
	return c.ForumTopicResult{ThreadID: result.ThreadID}, supportOperationError("create_topic", err)
}

// ReopenForumTopic preserves the existing topic mapping and never creates one.
func (adapter Adapter) ReopenForumTopic(ctx context.Context, token string, request c.ForumTopicRequest) error {
	client, err := adapter.client(token)
	if err != nil {
		return &sendError{code: 400, kind: "invalid_content"}
	}
	return supportOperationError("reopen_topic", client.ReopenForumTopic(ctx, api.ForumTopicRequest{ChatID: request.ChatID, ThreadID: request.ThreadID, Name: request.Name}))
}

// CloseForumTopic closes without deleting any local or remote message mapping.
func (adapter Adapter) CloseForumTopic(ctx context.Context, token string, request c.ForumTopicRequest) error {
	client, err := adapter.client(token)
	if err != nil {
		return &sendError{code: 400, kind: "invalid_content"}
	}
	return supportOperationError("close_topic", client.CloseForumTopic(ctx, api.ForumTopicRequest{ChatID: request.ChatID, ThreadID: request.ThreadID, Name: request.Name}))
}
