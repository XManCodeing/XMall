package contract

import (
	"context"
	"errors"
)

var (
	ErrTransferUnsupported   = errors.New("telegram transfer unavailable")
	ErrForumTopicUnsupported = errors.New("telegram forum topic management unavailable")
	ErrInvalidTransfer       = errors.New("telegram invalid transfer content")
)

// TransferContent is one source message, not one photo size. FileID must belong
// to the same bot; URLs, uploads, parse modes and implicit text fallback are absent.
// HasProtectedContent describes the source and must not be bypassed by resending.
type TransferContent struct {
	Kind                string          `json:"kind"`
	Text                string          `json:"text,omitempty"`
	Entities            []MessageEntity `json:"entities,omitempty"`
	FileID              string          `json:"file_id,omitempty"`
	FileSize            int64           `json:"file_size,omitempty"`
	Caption             string          `json:"caption,omitempty"`
	CaptionEntities     []MessageEntity `json:"caption_entities,omitempty"`
	HasProtectedContent bool            `json:"has_protected_content"`
}

// TransferRequest carries an already-authorized destination. The caller must
// recheck actor, current route and reply mapping before invoking Transfer.
// Contents contains one message or 2..10 ordered album items. Runtime and the
// support adapter force ProtectContent=true until an explicit policy is approved.
// ReplyMessageID is a trusted destination message ID, never the unverified inbound reply ID.
type TransferRequest struct {
	// SourceChatID and SourceMessageIDs opt into native forwarding. Only support_ready
	// may set them, after comparing against the encrypted original updates.
	// Zero values retain the legacy typed-send behavior for already queued work.
	SourceChatID     int64             `json:"source_chat_id,omitempty"`
	SourceMessageIDs []int64           `json:"source_message_ids,omitempty"`
	TargetType       TargetType        `json:"target_type"`
	BusinessRef      string            `json:"business_ref"`
	RouteVersion     uint64            `json:"route_version"`
	ChatID           int64             `json:"chat_id"`
	ThreadID         int64             `json:"thread_id"`
	ReplyMessageID   int64             `json:"reply_message_id,omitempty"`
	ProtectContent   bool              `json:"protect_content"`
	Contents         []TransferContent `json:"contents"`
}

// TransferResult preserves wire order and all observed IDs even on error.
// An error with APIError.Uncertain()==true requires reconciliation, not replay;
// partial results are observations, never evidence that other items were unsent.
type TransferResult struct {
	Messages []DeliveryResult `json:"messages"`
}

// TransferAPI is optional and leaves legacy API/Apply shopping behavior intact.
type TransferAPI interface {
	Transfer(context.Context, string, TransferRequest) (TransferResult, error)
}

// ForumTopicRequest identifies one already-authorized forum route. Name is used
// only for creation; ThreadID is required for reopen/close and zero for create.
type ForumTopicRequest struct {
	ChatID   int64  `json:"chat_id"`
	ThreadID int64  `json:"thread_id"`
	Name     string `json:"name,omitempty"`
}

type ForumTopicResult struct {
	ThreadID int64 `json:"thread_id"`
}

// ForumTopicAPI never retries. Persist a unique creation claim before calling;
// unknown creation outcomes must not automatically create a second topic.
type ForumTopicAPI interface {
	CreateForumTopic(context.Context, string, ForumTopicRequest) (ForumTopicResult, error)
	ReopenForumTopic(context.Context, string, ForumTopicRequest) error
	CloseForumTopic(context.Context, string, ForumTopicRequest) error
}

// TransferContentFromUpdate selects one supported message from an inbound
// envelope. Photos choose the largest area (then file size), not array order.
// Albums must be aggregated by the caller using bot/chat/media_group_id.
// It returns ErrInvalidTransfer instead of silently turning unknown media into text.
func TransferContentFromUpdate(update Update) (TransferContent, error) {
	content := TransferContent{Kind: update.MessageKind, Text: update.Text,
		Entities: update.Entities, Caption: update.Caption, CaptionEntities: update.CaptionEntities,
		HasProtectedContent: update.HasProtectedContent}
	if content.Kind == "text" {
		if content.Text == "" || len(update.Media) != 0 {
			return TransferContent{}, ErrInvalidTransfer
		}
		return content, nil
	}
	switch content.Kind {
	case "photo", "video", "voice", "audio", "document", "animation", "sticker":
	default:
		return TransferContent{}, ErrInvalidTransfer
	}
	var selected Media
	for _, media := range update.Media {
		if media.Type != content.Kind || media.FileID == "" {
			continue
		}
		if selected.FileID != "" && content.Kind != "photo" {
			return TransferContent{}, ErrInvalidTransfer
		}
		area, bestArea := int64(media.Width)*int64(media.Height), int64(selected.Width)*int64(selected.Height)
		if selected.FileID == "" || area > bestArea || (area == bestArea && media.FileSize > selected.FileSize) {
			selected = media
		}
	}
	if selected.FileID == "" || content.Text != "" || len(content.Entities) != 0 {
		return TransferContent{}, ErrInvalidTransfer
	}
	content.FileID, content.FileSize = selected.FileID, selected.FileSize
	return content, nil
}
