package telegrambroadcast

import (
	"context"
	"encoding/json"
	"testing"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcast "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

type bridgeStore struct {
	c.Store
	contacts map[int64]*d.Contact
	rows     map[string]d.Delivery
}

func (s *bridgeStore) Get(context.Context, uint) (*d.Instance, error) {
	return &d.Instance{Mode: "internal"}, nil
}
func (s *bridgeStore) Contact(_ context.Context, _ uint, id int64) (*d.Contact, error) {
	return s.contacts[id], nil
}
func (s *bridgeStore) Enqueue(_ context.Context, row *d.Delivery) error {
	if _, ok := s.rows[row.Key]; !ok {
		s.rows[row.Key] = *row
	}
	return nil
}
func (s *bridgeStore) ListDeliveries(context.Context, uint, uint, int, int) ([]d.Delivery, int64, error) {
	var rows []d.Delivery
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	return rows, int64(len(rows)), nil
}

type plainCipher struct{}

func (plainCipher) Seal(s string) (string, error) { return s, nil }
func (plainCipher) Open(s string) (string, error) { return s, nil }

type testUsers []app.UserItem

func (u testUsers) ListTelegramUsers(app.UserQuery) ([]app.UserItem, int64, error) {
	return u, int64(len(u)), nil
}

type testIdentity struct{}

func (testIdentity) IdentityStamp(context.Context, uint, int64, uint) (string, error) {
	return "1:1", nil
}

func TestSnapshotRequiresPrivateReachableContacts(t *testing.T) {
	s := &bridgeStore{contacts: map[int64]*d.Contact{
		11: {TelegramUserID: 11, ChatID: 11, UserID: 1, Reachable: true},
		12: {TelegramUserID: 12, ChatID: -12, UserID: 2, Reachable: true},
		13: {TelegramUserID: 13, ChatID: 13, UserID: 3, Reachable: false},
		14: {TelegramUserID: 14, ChatID: 14, UserID: 99, Reachable: true},
	}, rows: map[string]d.Delivery{}}
	b := RuntimeBridge{Store: s, Cipher: plainCipher{}, Identity: testIdentity{}, Users: testUsers{
		{UserID: 1, TelegramUserID: "11"}, {UserID: 2, TelegramUserID: "12"}, {UserID: 3, TelegramUserID: "13"}, {UserID: 4, TelegramUserID: "14"}, {UserID: 5, TelegramUserID: "15"},
	}}
	input := app.CreateInput{BotID: 1, RecipientType: "all", MessageHTML: "hello"}
	raw, count, err := b.Prepare(context.Background(), input)
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	job := &broadcast.Broadcast{ID: 7, BotID: 1, RecipientCount: 1, PayloadCipher: raw}
	for range 2 {
		if err = b.Enqueue(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.rows) != 1 {
		t.Fatal("duplicate recipient delivery")
	}
	for key, row := range s.rows {
		row.Status = "retry"
		s.rows[key] = row
	}
	p, err := b.Progress(context.Background(), 1, 7)
	if err != nil || p.Pending != 1 || p.Total != 1 {
		t.Fatal(p, err)
	}
	for key, row := range s.rows {
		row.Status = "unknown"
		s.rows[key] = row
	}
	p, err = b.Progress(context.Background(), 1, 7)
	if err != nil || p.Unknown != 1 || p.Pending != 0 {
		t.Fatal(p, err)
	}
	input.Filters = jsonmap.JSON{"keyword": "limited"}
	if _, _, err = b.Prepare(context.Background(), input); err == nil {
		t.Fatal("all-audience filters silently ignored")
	}
	input.RecipientType = "specific"
	input.UserIDs = []uint{1}
	raw, count, err = b.Prepare(context.Background(), input)
	if err != nil || count != 1 || raw == "" {
		t.Fatal("specific audience rejected search filters", count, err)
	}
	var snap snapshot
	if err = json.Unmarshal([]byte(raw), &snap); err != nil || snap.Message.ParseMode != "HTML" || snap.Message.Purpose != "broadcast" {
		t.Fatalf("broadcast message missing HTML mode: %+v %v", snap.Message, err)
	}
	input.Filters = nil
	input.Buttons = [][]app.Button{{{Text: "Shop", URL: "https://example.com/shop"}}}
	raw, count, err = b.Prepare(context.Background(), input)
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = json.Unmarshal([]byte(raw), &snap); err != nil || len(snap.Message.Buttons) != 1 || snap.Message.Buttons[0][0].URL != "https://example.com/shop" {
		t.Fatalf("buttons missing: %+v %v", snap.Message.Buttons, err)
	}
	input.Buttons = [][]app.Button{{{Text: "Bad", URL: "javascript:alert(1)"}}}
	if _, _, err = b.Prepare(context.Background(), input); err == nil {
		t.Fatal("accepted unsafe button url")
	}
}
func TestCorruptSnapshotIsRejectedBeforeAnyEnqueue(t *testing.T) {
	for _, bad := range []recipient{{UserID: 2, TelegramID: 2, ChatID: -2, Stamp: "stamp"}, {UserID: 2, TelegramID: 1, ChatID: 1, Stamp: "stamp"}} {
		s := &bridgeStore{rows: map[string]d.Delivery{}}
		b := RuntimeBridge{Store: s, Cipher: plainCipher{}}
		raw, _ := json.Marshal(snapshot{Recipients: []recipient{{UserID: 1, TelegramID: 1, ChatID: 1, Stamp: "stamp"}, bad}})
		err := b.Enqueue(context.Background(), &broadcast.Broadcast{ID: 1, BotID: 1, RecipientCount: 2, PayloadCipher: string(raw)})
		if err == nil || len(s.rows) != 0 {
			t.Fatal("corrupt snapshot partially enqueued", err)
		}
	}
}
