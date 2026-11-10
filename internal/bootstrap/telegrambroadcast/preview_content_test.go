package telegrambroadcast

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcast "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type currentShop struct{ stamp string }

func (*currentShop) AuthorizeDelivery(context.Context, uint, int64, uint) error { return nil }
func (s *currentShop) DeliveryStamp(context.Context, uint, int64) (string, error) {
	return s.stamp, nil
}

type scopedBridgeStore struct{ bridgeStore }

func (s *scopedBridgeStore) Contact(ctx context.Context, bot uint, tg int64) (*d.Contact, error) {
	if bot != 7 {
		return nil, nil
	}
	return s.bridgeStore.Contact(ctx, bot, tg)
}

func TestPreviewUsesInstancePrivateCurrentIdentitySnapshot(t *testing.T) {
	ctx := context.Background()
	store := &scopedBridgeStore{bridgeStore{contacts: map[int64]*d.Contact{11: {UserID: 1, ChatID: 11, Reachable: true}, 12: {UserID: 2, ChatID: -12, Reachable: true}, 13: {UserID: 3, ChatID: 13, Reachable: false}}, rows: map[string]d.Delivery{}}}
	identity := &currentShop{stamp: "42:1"}
	bridge := &RuntimeBridge{Store: store, Cipher: plainCipher{}, Identity: ShopIdentity{Shop: identity}, Users: testUsers{{UserID: 1, TelegramUserID: "11"}, {UserID: 2, TelegramUserID: "12"}, {UserID: 3, TelegramUserID: "13"}}}
	svc := app.NewRuntimeService(nil, nil, bridge, nil) // No persistence dependency required for preview.
	input := app.CreateInput{BotID: 7, Title: "test", RecipientType: "all", MessageHTML: "secret"}
	check := func(want int) {
		t.Helper()
		got, err := svc.PreviewBroadcast(ctx, input)
		if err != nil || got.RecipientCount != want || len(store.rows) != 0 {
			t.Fatalf("preview=%+v err=%v", got, err)
		}
	}
	check(1)
	input.BotID = 8
	check(0)
	input.BotID = 7
	identity.stamp = "43:9" // Historical owner authorization must not broaden broadcasts.
	check(0)
	if _, err := bridge.Identity.IdentityStamp(ctx, 7, 11, 1); !errors.Is(err, shop.ErrDenied) {
		t.Fatal(err)
	}
	identity.stamp = "44:1"
	input.RecipientType = "specific"
	check(0) // Empty selection must not become all users.
	input.UserIDs = []uint{2}
	check(0)
	input.UserIDs = []uint{1, 1}
	check(1)
	store.contacts[11].Reachable = false
	check(0)
}

type failingCipher struct{}

func (failingCipher) Seal(string) (string, error) { return "", errors.New("secret seal error") }
func (failingCipher) Open(string) (string, error) { return "", errors.New("secret decrypt error") }

func TestReadContentOnlyUsesEncryptedSnapshot(t *testing.T) {
	snap := snapshot{Recipients: []recipient{{UserID: 1, TelegramID: 11, ChatID: 11, Stamp: "private identity"}}, Message: c.Message{Text: "private body", Purpose: "broadcast", PhotoURL: "/uploads/photo.png", MediaName: "stored.png", Buttons: [][]c.Button{{{Text: "Shop", URL: "https://example.test"}}}}, AttachmentName: "original.png"}
	raw, _ := json.Marshal(snap)
	bridge := &RuntimeBridge{Cipher: plainCipher{}}
	content, err := bridge.ReadContent(string(raw))
	if err != nil || content.MessageHTML != snap.Message.Text || content.AttachmentName != "original.png" || content.AttachmentURL != snap.Message.PhotoURL || len(content.Buttons) != 1 {
		t.Fatalf("%+v %v", content, err)
	}
	encoded, _ := json.Marshal(content)
	if strings.Contains(string(encoded), "private identity") || strings.Contains(string(encoded), "Recipients") {
		t.Fatal("recipient stamp leaked")
	}
	snap.AttachmentName = ""
	raw, _ = json.Marshal(snap)
	content, err = bridge.ReadContent(string(raw))
	if err != nil || content.AttachmentName != "stored.png" {
		t.Fatal("old encrypted snapshot incompatible", err)
	}
	for _, raw := range []string{"", "invalid json", "{}"} {
		if _, err := bridge.ReadContent(raw); !errors.Is(err, app.ErrInvalid) {
			t.Fatal("invalid snapshot accepted", err)
		}
	}
	bridge.Cipher = failingCipher{}
	if _, err := bridge.ReadContent("cipher"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("decrypt error leaked", err)
	}
	store := &bridgeStore{rows: map[string]d.Delivery{}}
	bridge.Store = store
	if err := bridge.Enqueue(context.Background(), &broadcast.Broadcast{BotID: 7, PayloadCipher: "broken", MessageHTML: "plaintext fallback"}); err == nil || strings.Contains(err.Error(), "secret") || len(store.rows) != 0 {
		t.Fatal("unsafe enqueue fallback", err)
	}
}
