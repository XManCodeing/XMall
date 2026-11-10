package contract

import (
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

// EncodeReadyAlbum preserves ordered customer originals and pins native-forward sources.
// sources must be a private customer album; binding is the authorized group topic.
func EncodeReadyAlbum(sources []r.Update, binding DeliveryBinding, ref string) (r.Message, error) {
	if len(sources) < 1 || sources[0].ChatID <= 0 || binding.ChatID >= 0 || binding.ThreadID <= 0 || binding.RouteVersion == 0 {
		return r.Message{}, ErrDenied
	}
	contents, err := AlbumContents(sources)
	if err != nil {
		return r.Message{}, err
	}
	message := TransferEnvelope(r.TargetSupportGroup, ref, "support_ready", binding.ChatID, binding.ThreadID, binding.RouteVersion, 0, contents)
	message.Transfer.SourceChatID = sources[0].ChatID
	for _, source := range sources {
		message.Transfer.SourceMessageIDs = append(message.Transfer.SourceMessageIDs, source.MessageID)
	}
	return message, nil
}

// AlbumContents validates ordered originals independently of upstream/downstream routing.
// It returns only content snapshots, requiring one actor/album and compatible media kinds.
func AlbumContents(sources []r.Update) ([]r.TransferContent, error) {
	if len(sources) < 1 || len(sources) > 10 {
		return nil, ErrDenied
	}
	contents := make([]r.TransferContent, 0, len(sources))
	first := sources[0]
	last := int64(0)
	category := ""
	if first.MediaGroupID == "" {
		return nil, ErrDenied
	}
	for _, source := range sources {
		if source.ChatID != first.ChatID || source.UserID != first.UserID || source.MediaGroupID != first.MediaGroupID || source.MessageID <= last {
			return nil, ErrDenied
		}
		last = source.MessageID
		content, e := r.TransferContentFromUpdate(source)
		if e != nil || content.HasProtectedContent {
			return nil, r.ErrInvalidTransfer
		}
		current := content.Kind
		if current == "photo" || current == "video" {
			current = "visual"
		}
		if current != "visual" && current != "audio" && current != "document" {
			return nil, r.ErrInvalidTransfer
		}
		if category != "" && category != current {
			return nil, r.ErrInvalidTransfer
		}
		category = current
		contents = append(contents, content)
	}
	return contents, nil
}
