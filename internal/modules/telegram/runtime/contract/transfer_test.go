package contract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTransferContentPhotoSelectsLargestAndKeepsMetadata(t *testing.T) {
	update := Update{MessageKind: "photo", Caption: "caption", CaptionEntities: []MessageEntity{{Type: "bold", Length: 7}}, HasProtectedContent: true, Media: []Media{
		{Type: "photo", FileID: "small", Width: 10, Height: 10},
		{Type: "photo", FileID: "large", Width: 100, Height: 100, FileSize: 400},
		{Type: "photo", FileID: "medium", Width: 50, Height: 50},
		{Type: "photo", FileID: "largest", Width: 100, Height: 100, FileSize: 500},
	}}
	content, err := TransferContentFromUpdate(update)
	if err != nil || content.FileID != "largest" || content.FileSize != 500 || content.Caption != "caption" || len(content.CaptionEntities) != 1 || !content.HasProtectedContent {
		t.Fatal(content, err)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TransferContent
	if err = json.Unmarshal(encoded, &decoded); err != nil || !decoded.HasProtectedContent {
		t.Fatal(decoded, err)
	}
}

func TestTransferContentUnsupportedNeverBecomesText(t *testing.T) {
	for _, update := range []Update{
		{MessageKind: "unknown", Text: "caption"}, {MessageKind: "video_note"},
		{MessageKind: "text", Text: "body", Media: []Media{{Type: "photo", FileID: "file"}}},
		{MessageKind: "photo"}, {MessageKind: "video", Media: []Media{{Type: "video", FileID: "first"}, {Type: "video", FileID: "second"}}},
	} {
		if _, err := TransferContentFromUpdate(update); !errors.Is(err, ErrInvalidTransfer) {
			t.Fatal(update, err)
		}
	}
}

func TestTransferContentAllSupportedKinds(t *testing.T) {
	for _, kind := range []string{"text", "photo", "video", "voice", "audio", "document", "animation", "sticker"} {
		update := Update{MessageKind: kind, Media: []Media{{Type: kind, FileID: "file"}}}
		if kind == "text" {
			update.Text = "hello"
			update.Media = nil
		}
		content, err := TransferContentFromUpdate(update)
		if err != nil || content.Kind != kind {
			t.Fatal(content, err)
		}
	}
}
