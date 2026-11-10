package contract

import (
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"strings"
)

// ContentRejection returns a stable feedback reason for an unsafe source, or empty
// for supported content. Legacy text envelopes are recognized without altering them.
func ContentRejection(source r.Update) string {
	if source.HasProtectedContent {
		return "content_protected"
	}
	if source.MessageKind == "" && len(source.Media) == 0 {
		// Legacy store records did not persist a message-kind discriminator.
		return ""
	}
	if _, err := r.TransferContentFromUpdate(source); err != nil {
		return "content_unsupported"
	}
	return ""
}

// FeedbackReason reports whether reason is a supported durable feedback contract.
func FeedbackReason(reason string) bool {
	switch reason {
	case "limit", "cooldown", "expired", "blocked", "album_late_or_limit", "content_protected", "content_unsupported":
		return true
	default:
		return false
	}
}

// EncodeReadyTransfer deterministically preserves one original source message.
// The caller supplies a persisted destination and canonical business reference.
// Albums must use the bounded album store instead of sending individual items.
func EncodeReadyTransfer(source r.Update, binding DeliveryBinding, ref string) (r.Message, error) {
	if source.MediaGroupID != "" || binding.ChatID >= 0 || binding.ThreadID <= 0 || binding.RouteVersion == 0 || !strings.HasPrefix(ref, "support:ready:") {
		return r.Message{}, ErrDenied
	}
	content, err := r.TransferContentFromUpdate(source)
	if err != nil || content.HasProtectedContent {
		return r.Message{}, r.ErrInvalidTransfer
	}
	message := TransferEnvelope(r.TargetSupportGroup, ref, "support_ready", binding.ChatID, binding.ThreadID, binding.RouteVersion, 0, []r.TransferContent{content})
	if source.ChatID <= 0 || source.MessageID <= 0 {
		return r.Message{}, ErrDenied
	}
	message.Transfer.SourceChatID = source.ChatID
	message.Transfer.SourceMessageIDs = []int64{source.MessageID}
	return message, nil
}

// TransferEnvelope builds matching nested/outer destinations with content
// protection enabled. It performs no I/O and never introduces legacy fields.
func TransferEnvelope(target r.TargetType, ref, purpose string, chat, thread int64, route uint64, reply int64, contents []r.TransferContent) r.Message {
	kind := "album"
	if len(contents) == 1 {
		kind = contents[0].Kind
	}
	return r.Message{TargetType: target, BusinessRef: ref, Purpose: purpose, Operation: "send", ChatID: chat, ThreadID: thread, RouteVersion: route, TargetKind: kind, Transfer: &r.TransferRequest{TargetType: target, BusinessRef: ref, RouteVersion: route, ChatID: chat, ThreadID: thread, ReplyMessageID: reply, ProtectContent: true, Contents: contents}}
}

// ValidTransferEnvelope independently checks nested fields and forbids mixed
// legacy content. Exact content is additionally compared with the source by stores.
func ValidTransferEnvelope(message r.Message) bool {
	transfer := message.Transfer
	if transfer == nil {
		return true
	} // Legacy pure-text dispatches remain readable.
	if !transfer.ProtectContent || transfer.TargetType != message.TargetType || transfer.BusinessRef != message.BusinessRef || transfer.RouteVersion != message.RouteVersion || transfer.ChatID != message.ChatID || transfer.ThreadID != message.ThreadID || len(transfer.Contents) < 1 || len(transfer.Contents) > 10 {
		return false
	}
	if transfer.SourceChatID != 0 || len(transfer.SourceMessageIDs) != 0 {
		if message.Purpose != "support_ready" || message.TargetType != r.TargetSupportGroup || transfer.SourceChatID <= 0 || transfer.ReplyMessageID != 0 || len(transfer.SourceMessageIDs) != len(transfer.Contents) {
			return false
		}
		previous := int64(0)
		for _, id := range transfer.SourceMessageIDs {
			if id <= previous {
				return false
			}
			previous = id
		}
	}
	expected := TransferEnvelope(message.TargetType, message.BusinessRef, message.Purpose, message.ChatID, message.ThreadID, message.RouteVersion, transfer.ReplyMessageID, transfer.Contents)
	return message.Operation == "send" && message.TargetKind == expected.TargetKind && message.Text == "" && len(message.Buttons) == 0 && len(message.Photo) == 0 && message.PhotoURL == "" && message.FileID == "" && message.ParseMode == "" && message.TargetMessageID == 0 && message.PanelRevision == 0 && message.PanelEpoch == "" && !message.ReplacePanel && !message.ForceDocument && message.OwnerUserID == 0 && message.IdentityStamp == "" && message.MediaName == ""
}
