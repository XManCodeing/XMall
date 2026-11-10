package broadcastapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

type contentBridge struct {
	bridgeStub
	count, prepares int
	content         BroadcastContent
	readErr         error
}

func (b *contentBridge) Prepare(context.Context, CreateInput) (string, int, error) {
	b.prepares++
	return "encrypted", b.count, nil
}
func (b *contentBridge) ReadContent(string) (BroadcastContent, error) { return b.content, b.readErr }

func TestPreviewDoesNotPersistAndCreateRechecksAudience(t *testing.T) {
	ctx := context.Background()
	store := &broadcastStoreStub{}
	bridge := &contentBridge{count: 2, bridgeStub: bridgeStub{keys: map[uint]bool{}}}
	svc := NewRuntimeService(store, nil, bridge, nil)
	input := CreateInput{BotID: 7, Title: "preview", RecipientType: "all", MessageHTML: "private"}
	got, err := svc.PreviewBroadcast(ctx, input)
	if err != nil || got.RecipientCount != 2 || len(store.items) != 0 || len(bridge.keys) != 0 {
		t.Fatalf("preview=%+v err=%v", got, err)
	}
	bridge.count = 0 // Reachability/identity changed after the preview.
	got, err = svc.PreviewBroadcast(ctx, input)
	if err != nil || got.RecipientCount != 0 {
		t.Fatalf("zero preview=%+v err=%v", got, err)
	}
	if _, err = svc.CreateBroadcast(ctx, input); !errors.Is(err, ErrNoRecipients) {
		t.Fatal(err)
	}
	if bridge.prepares != 3 || len(store.items) != 0 || len(bridge.keys) != 0 {
		t.Fatal("preview persisted or create reused stale audience")
	}
}

func TestEncryptedContentNeverWrittenBack(t *testing.T) {
	ctx := context.Background()
	store := &broadcastStoreStub{}
	bridge := &contentBridge{count: 2, bridgeStub: bridgeStub{keys: map[uint]bool{}, progress: Progress{Total: 2, Pending: 2}}, content: BroadcastContent{MessageHTML: "secret body", AttachmentURL: "/uploads/private.png", AttachmentName: "private.png", Buttons: [][]Button{{{Text: "secret button", URL: "https://example.test/private"}}}}}
	svc := NewRuntimeService(store, nil, bridge, nil)
	job, err := svc.CreateBroadcast(ctx, CreateInput{BotID: 7, Title: "test", RecipientType: "all", MessageHTML: bridge.content.MessageHTML, AttachmentURL: bridge.content.AttachmentURL, AttachmentName: bridge.content.AttachmentName, Buttons: bridge.content.Buttons})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetBroadcast(ctx, job.ID)
	if err != nil || detail.MessageHTML != bridge.content.MessageHTML || detail.AttachmentURL != bridge.content.AttachmentURL || detail.AttachmentName != bridge.content.AttachmentName || len(detail.Buttons) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	detail.MessageHTML = "mutated response"
	if err := svc.ProcessBroadcast(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	row := store.items[job.ID]
	if row.PayloadCipher != "encrypted" || row.MessageHTML != "" || row.AttachmentURL != "" || row.AttachmentName != "" {
		t.Fatal("decrypted content persisted")
	}
}

func TestDetailFailsClosedAndLegacyIsDisplayOnly(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, cipher string
		version      int
		readErr      error
		ok           bool
	}{
		{"legacy", "", 0, nil, true},
		{"missing modern snapshot", "", 1, nil, false},
		{"decrypt failure", "broken", 1, errors.New("secret token and plaintext"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &broadcastStoreStub{items: map[uint]*d.Broadcast{1: {ID: 1, BotID: 7, Status: "pending", DeliveryVersion: tc.version, PayloadCipher: tc.cipher, MessageHTML: "legacy private body", AttachmentURL: "legacy private attachment"}}}
			bridge := &contentBridge{readErr: tc.readErr, bridgeStub: bridgeStub{keys: map[uint]bool{}}}
			svc := NewRuntimeService(store, nil, bridge, nil)
			detail, err := svc.GetBroadcast(ctx, 1)
			if tc.ok {
				if err != nil || detail.MessageHTML != "legacy private body" {
					t.Fatalf("%+v %v", detail, err)
				}
			} else if err == nil || detail != nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe fallback: %+v %v", detail, err)
			}
			if tc.cipher == "" {
				if err := svc.ProcessBroadcast(ctx, 1); err != nil {
					t.Fatal(err)
				}
				if len(bridge.keys) != 0 || store.items[1].Status != "needs_review" {
					t.Fatal("legacy/missing snapshot sent")
				}
			}
		})
	}
}
