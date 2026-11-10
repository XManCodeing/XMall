package contract

import (
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"testing"
)

func TestReadyTransferPreservesOriginalAndProtection(t *testing.T) {
	source := r.Update{ChatID: 42, MessageID: 10, MessageKind: "photo", Caption: "caption", CaptionEntities: []r.MessageEntity{{Type: "bold", Offset: 0, Length: 2}}, Media: []r.Media{{Type: "photo", FileID: "small", Width: 2, Height: 2}, {Type: "photo", FileID: "large", Width: 100, Height: 100}}}
	message, e := EncodeReadyTransfer(source, DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}, "support:ready:1:2")
	if e != nil || message.Transfer == nil || message.Transfer.Contents[0].FileID != "large" || message.Transfer.Contents[0].Caption != source.Caption || !message.Transfer.ProtectContent || !ValidTransferEnvelope(message) {
		t.Fatal(message, e)
	}
	for _, mutate := range []func(*r.Message){func(m *r.Message) { m.Transfer.ChatID = -200 }, func(m *r.Message) { m.Transfer.BusinessRef = "forged" }, func(m *r.Message) { m.Transfer.ProtectContent = false }, func(m *r.Message) { m.Text = "mixed" }, func(m *r.Message) { m.Transfer.RouteVersion = 9 }} {
		fresh, e := EncodeReadyTransfer(source, DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}, "support:ready:1:2")
		if e != nil {
			t.Fatal(e)
		}
		mutate(&fresh)
		if ValidTransferEnvelope(fresh) {
			t.Fatal("mismatched nested destination", fresh)
		}
	}
	source.HasProtectedContent = true
	if _, e = EncodeReadyTransfer(source, DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}, "support:ready:1:2"); e == nil {
		t.Fatal("protected source bypassed")
	}
}
