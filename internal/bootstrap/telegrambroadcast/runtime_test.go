package telegrambroadcast

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/XManCodeing/XMall/internal/crypto"
	channelapp "github.com/XManCodeing/XMall/internal/modules/channelclient/application"
	channelcontract "github.com/XManCodeing/XMall/internal/modules/channelclient/contract"
	channeldomain "github.com/XManCodeing/XMall/internal/modules/channelclient/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"github.com/hibiken/asynq"

	cutover "github.com/XManCodeing/XMall/internal/modules/telegram/cutover/application"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcast "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

type legacyChannelFake struct {
	channelcontract.Store
	row channeldomain.Client
}

func (s *legacyChannelFake) FindByID(id uint) (*channeldomain.Client, error) {
	if s.row.ID != id {
		return nil, nil
	}
	copy := s.row
	return &copy, nil
}
func (s *legacyChannelFake) Update(row *channeldomain.Client) error { s.row = *row; return nil }

type importAPIFake struct {
	c.API
	calls int
}

func (a *importAPIFake) Identity(_ context.Context, token string) (c.BotIdentity, error) {
	a.calls++
	if token != "123:test-only" {
		return c.BotIdentity{}, errors.New("bad token")
	}
	return c.BotIdentity{ID: 123, Username: "test_bot"}, nil
}
func TestExplicitLegacyImportChecksIdentityAndNeverGuessesContacts(t *testing.T) {
	ctx := context.Background()
	cipher, err := crypto.Encrypt(crypto.DeriveKey("test-secret"), "123:test-only")
	if err != nil {
		t.Fatal(err)
	}
	channels := &legacyChannelFake{row: channeldomain.Client{ID: 9, ChannelType: "telegram_bot", BotToken: cipher, Status: 1}}
	store := &cutoverStoreFake{}
	api := &importAPIFake{}
	manager := runtimeapp.NewManager(store, api, "test-secret")
	s := NewManagedCutover(manager, channelapp.NewService(channels, "test-secret"), nil, nil)
	for _, req := range []cutover.ImportRequest{{ChannelID: 8, TelegramID: 123}, {ChannelID: 9, TelegramID: 456}} {
		if _, err := s.Import(ctx, req); err == nil || len(store.rows) != 0 {
			t.Fatal("unverified source imported")
		}
	}
	channels.row.ChannelType = "other"
	if _, err := s.Import(ctx, cutover.ImportRequest{ChannelID: 9, TelegramID: 123}); err == nil {
		t.Fatal("wrong channel type")
	}
	channels.row.ChannelType = "telegram_bot"
	for range 2 {
		result, err := s.Import(ctx, cutover.ImportRequest{ChannelID: 9, TelegramID: 123})
		if err != nil || result.Mode != "external" || result.ContactsImported != 0 || result.ContactsPolicy == "" {
			t.Fatal(result, err)
		}
	}
	if len(store.rows) != 1 || store.rows[0].Enabled || *store.rows[0].ChannelClientID != 9 {
		t.Fatal("duplicate or unsafe import")
	}
	if _, err := s.Preflight(ctx, cutover.Request{BotID: 1, ChannelID: 9, Version: 1}); err != nil {
		t.Fatal(err)
	}
	store.rows[0].ChannelClientID = nil
	if _, err := s.Import(ctx, cutover.ImportRequest{ChannelID: 9, TelegramID: 123}); err == nil {
		t.Fatal("implicitly rebound existing bot")
	}
}

type inventoryOrdersFake struct{ ordercontract.Store }

func (inventoryOrdersFake) ListAdmin(filter ordercontract.ListFilter) ([]orderdomain.Order, int64, error) {
	if filter.Status == "" {
		return nil, 8, nil
	}
	return nil, 1, nil
}

type inventoryQueueFake struct{ err error }

func (q inventoryQueueFake) Queues() ([]string, error) { return []string{"default", "old"}, q.err }
func (q inventoryQueueFake) GetQueueInfo(string) (*asynq.QueueInfo, error) {
	return &asynq.QueueInfo{Pending: 1, Retry: 2, Scheduled: 3, Archived: 4, Active: 5, Aggregating: 6}, q.err
}
func TestInventoryCountsAllQueuesAndRevokesDedicatedCredential(t *testing.T) {
	channels := &legacyChannelFake{row: channeldomain.Client{ID: 9, ChannelType: "telegram_bot", ChannelSecret: "old-secret", Status: 1}}
	r := &legacyReadiness{channels: channelapp.NewService(channels, "test-secret"), orders: inventoryOrdersFake{}, queues: inventoryQueueFake{}}
	v, err := r.Inspect(context.Background(), 9)
	if err != nil || !v.Checked || v.UnfinishedOrders != 4 || v.Pending != 2 || v.Retry != 4 || v.Scheduled != 6 || v.Archived != 8 || v.Active != 22 {
		t.Fatal(v, err)
	}
	if err = r.RevokeCredentials(context.Background(), 9); err != nil || channels.row.Status != 0 || channels.row.ChannelSecret == "old-secret" {
		t.Fatal("credential remains usable", err)
	}
	r.queues = inventoryQueueFake{err: errors.New("redis unavailable")}
	if v, err = r.Inspect(context.Background(), 9); err == nil || v.Checked {
		t.Fatal("incomplete inventory accepted")
	}
}

type cutoverReadyFake struct{}

func (cutoverReadyFake) Inspect(context.Context, uint) (cutover.Inventory, error) {
	return cutover.Inventory{Checked: true, Scope: "test"}, nil
}
func (cutoverReadyFake) RevokeCredentials(context.Context, uint) error { return nil }

type cutoverStoreFake struct {
	c.Store
	rows []d.Instance
}

func (s *cutoverStoreFake) ImportLegacy(ctx context.Context, row *d.Instance, _ string) (*d.Instance, error) {
	for _, existing := range s.rows {
		if existing.TelegramID == row.TelegramID {
			if existing.ChannelClientID == nil || *existing.ChannelClientID != *row.ChannelClientID {
				return nil, c.ErrConflict
			}
			return &existing, nil
		}
	}
	if err := s.Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}
func (s *cutoverStoreFake) SwitchCutover(ctx context.Context, id uint, version uint64, mode string) error {
	return s.Configure(ctx, id, version, map[string]interface{}{"mode": mode, "enabled": false})
}

func (s *cutoverStoreFake) Create(_ context.Context, row *d.Instance) error {
	row.ID = uint(len(s.rows) + 1)
	s.rows = append(s.rows, *row)
	return nil
}
func (s *cutoverStoreFake) Get(_ context.Context, id uint) (*d.Instance, error) {
	for _, row := range s.rows {
		if row.ID == id {
			return &row, nil
		}
	}
	return nil, c.ErrConflict
}
func (s *cutoverStoreFake) List(context.Context) ([]d.Instance, error) { return s.rows, nil }
func (s *cutoverStoreFake) Configure(_ context.Context, id uint, version uint64, fields map[string]interface{}) error {
	for i := range s.rows {
		row := &s.rows[i]
		if row.ID == id && row.Version == version {
			row.Mode = fields["mode"].(string)
			row.Enabled = fields["enabled"].(bool)
			row.Version++
			return nil
		}
	}
	return c.ErrConflict
}

type cutoverAPIFake struct {
	c.API
	hook            bool
	checks, deletes int
	err             error
}

func (a *cutoverAPIFake) Webhook(context.Context, string) (string, error) {
	a.checks++
	if a.err != nil {
		return "", a.err
	}
	if a.hook {
		return "https://invalid.example/hook", nil
	}
	return "", nil
}
func (a *cutoverAPIFake) DeleteWebhook(_ context.Context, _ string, drop bool) error {
	a.deletes++
	if drop {
		panic("must preserve updates")
	}
	a.hook = false
	return a.err
}
func TestCutoverWiringResumesAndPreservesPolling(t *testing.T) {
	ctx := context.Background()
	channel := uint(9)
	store := &cutoverStoreFake{rows: []d.Instance{{ID: 4, ChannelClientID: &channel, Mode: "external", Version: 1}}}
	api := &cutoverAPIFake{hook: true}
	manager := runtimeapp.NewManager(store, api, "isolated-test-secret")
	cipher, err := manager.Seal("123:test-only")
	if err != nil {
		t.Fatal(err)
	}
	store.rows[0].TokenCipher = cipher
	s := NewCutover(manager)
	s.Readiness = cutoverReadyFake{}
	r := cutover.Request{BotID: 4, ChannelID: 9, Version: 1, ConfirmExternalStopped: true, ConfirmRevokeCredentials: true}
	if _, err = s.Preflight(ctx, r); err != nil || api.checks != 0 {
		t.Fatal("preflight used network", err)
	}
	if err = s.Apply(ctx, r); err == nil || store.rows[0].Mode != "cutover_pending" {
		t.Fatal("missing webhook confirmation accepted")
	}
	if allowed, err := s.LegacyAllowed(ctx, 9); allowed || err != nil {
		t.Fatal("old channel not fenced", err)
	}
	r.Version = store.rows[0].Version
	r.ConfirmDeleteWebhook = true
	if err = s.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	if api.deletes != 1 || store.rows[0].Mode != "internal" || store.rows[0].Enabled {
		t.Fatal("unsafe cutover")
	}
	store.rows[0].Enabled = true
	r.Version = store.rows[0].Version
	if err = s.Apply(ctx, r); err != nil || !store.rows[0].Enabled || api.deletes != 1 {
		t.Fatal("not reentrant", err)
	}
}
func TestCutoverNetworkRedactsErrorsAndAmbiguousChannelFailsClosed(t *testing.T) {
	ctx := context.Background()
	channel := uint(9)
	store := &cutoverStoreFake{rows: []d.Instance{{ID: 4, ChannelClientID: &channel, Mode: "cutover_pending", Version: 1}}}
	api := &cutoverAPIFake{err: errors.New("secret-url-token")}
	manager := runtimeapp.NewManager(store, api, "isolated-test-secret")
	store.rows[0].TokenCipher, _ = manager.Seal("123:test-only")
	n := cutoverNetwork{manager}
	if _, err := n.HasWebhook(ctx, 4); err != cutover.ErrBlocked {
		t.Fatal("unredacted network error", err)
	}
	if err := n.DeleteWebhook(ctx, 4, true); err == nil || api.deletes != 0 {
		t.Fatal("dropped updates")
	}
	store.rows = append(store.rows, d.Instance{ID: 5, ChannelClientID: &channel})
	if allowed, err := NewCutover(manager).LegacyAllowed(ctx, 9); allowed || err == nil {
		t.Fatal("ambiguous mapping allowed")
	}
	if allowed, err := NewCutover(nil).LegacyAllowed(ctx, 9); allowed || err == nil {
		t.Fatal("missing runtime allowed")
	}
}

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
		t.Fatal("filters silently ignored")
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
