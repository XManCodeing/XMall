package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// reconcileStore injects independent scan/process errors and records saved work.
type reconcileStore struct {
	m.ProactiveRepository
	rows    []*d.ProactiveDelivery
	saved   []*d.ProactiveDelivery
	scanErr error
}

// ScanProactive returns the configured historical scan failure without network I/O.
func (store *reconcileStore) ScanProactive(context.Context, uint, int, time.Time) (int, error) {
	return 0, store.scanErr
}

// ClaimProactive returns each existing responsibility once for bounded reconciliation.
func (store *reconcileStore) ClaimProactive(context.Context, uint, string, time.Time, time.Duration) (*d.ProactiveDelivery, error) {
	if len(store.rows) == 0 {
		return nil, nil
	}
	row := store.rows[0]
	store.rows = store.rows[1:]
	return row, nil
}

// SaveProactive captures transitions for assertions without mutating immutable identity.
func (store *reconcileStore) SaveProactive(_ context.Context, row *d.ProactiveDelivery, _ time.Time) error {
	store.saved = append(store.saved, row)
	return nil
}

// reconcileQueue supplies exact receipts and captures independent notification inserts.
type reconcileQueue struct {
	r.Store
	receipts map[string]r.InviteDeliveryStatus
	failKey  string
	inserted []*rd.Delivery
}

// BusinessDelivery returns a configured durable fact or a missing receipt.
func (queue *reconcileQueue) BusinessDelivery(_ context.Context, _ uint, key string) (r.InviteDeliveryStatus, error) {
	if key == queue.failKey {
		return r.InviteDeliveryStatus{}, errors.New("receipt unavailable")
	}
	if receipt, ok := queue.receipts[key]; ok {
		return receipt, nil
	}
	return r.InviteDeliveryStatus{State: "not_requested"}, nil
}

// Contact returns a reachable, identity-matched private recipient for local queue tests.
func (queue *reconcileQueue) Contact(context.Context, uint, int64) (*rd.Contact, error) {
	return &rd.Contact{ChatID: 88, UserID: 7, Reachable: true, Locale: "en-US"}, nil
}

// Enqueue records payloads only; this test double cannot send to Telegram.
func (queue *reconcileQueue) Enqueue(_ context.Context, row *rd.Delivery) error {
	queue.inserted = append(queue.inserted, row)
	queue.receipts[row.Key] = r.InviteDeliveryStatus{State: "pending"}
	return nil
}

// noNetworkAccess satisfies composition while any unexpected access call fails loudly.
type noNetworkAccess struct{ Access }

// noNetworkRoutes satisfies composition while receipt processing needs no route lookup.
type noNetworkRoutes struct{ m.AccessRoutes }

// plainCipher makes locally captured payloads readable; no credentials are involved.
type plainCipher struct{}

// Seal returns the fixture plaintext for inspection, never for production storage.
func (plainCipher) Seal(value string) (string, error) { return value, nil }

// Open returns the fixture payload unchanged for round-trip compatibility.
func (plainCipher) Open(value string) (string, error) { return value, nil }

// TestReconcileScanFailureDoesNotBlockExistingGroups verifies historical repair isolation.
func TestReconcileScanFailureDoesNotBlockExistingGroups(t *testing.T) {
	scanErr := errors.New("historical source mismatch")
	store := &reconcileStore{scanErr: scanErr, rows: []*d.ProactiveDelivery{
		{ID: 1, TargetID: 10, State: "queued", BusinessKey: "group-one"},
		{ID: 2, TargetID: 20, State: "queued", BusinessKey: "group-two"},
	}}
	queue := &reconcileQueue{failKey: "group-one", receipts: map[string]r.InviteDeliveryStatus{
		"group-two": {State: "sent", MessageID: 42},
	}}
	service := &Service{Store: store, Queue: queue, Access: noNetworkAccess{}, Routes: noNetworkRoutes{}, Cipher: plainCipher{}}
	if err := service.Reconcile(context.Background(), 1); !errors.Is(err, scanErr) {
		t.Fatalf("scan failure lost: %v", err)
	}
	if len(store.saved) != 2 || store.saved[0].State != "queued" || store.saved[1].State != "sent" || store.saved[1].MessageID != 42 {
		t.Fatalf("unrelated existing group stalled: %+v", store.saved)
	}
}

// feedbackAccess returns an authorized group snapshot without creating invitation work.
type feedbackAccess struct {
	Access
	group m.AccessGroup
}

// ListMemberships supplies the exact test scope; parameters are unused because no repository is involved.
func (access feedbackAccess) ListMemberships(context.Context, m.AccessIdentity, uint, int) ([]m.AccessGroup, error) {
	return []m.AccessGroup{access.group}, nil
}

// TestMembershipFeedbackShowsEntitlementNotLinkTerm checks all locales and denies incomplete evidence.
func TestMembershipFeedbackShowsEntitlementNotLinkTerm(t *testing.T) {
	expiry := time.Date(2027, 1, 2, 19, 4, 0, 0, time.UTC)
	row := &d.ProactiveDelivery{TargetID: 20}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		service := &Service{Access: feedbackAccess{group: m.AccessGroup{TargetID: 20, Title: "VIP group", Active: true, Kind: "limited", ExpiresAt: &expiry}}}
		text, err := service.membershipFeedback(context.Background(), row, locale, "ready")
		if err != nil || !strings.Contains(text, "VIP group") || !strings.Contains(text, "2027-01-03 03:04 UTC+8") {
			t.Fatalf("missing entitlement description: %q %v", text, err)
		}
	}
	service := &Service{Access: feedbackAccess{group: m.AccessGroup{TargetID: 20, Active: true, Kind: string(d.TermPermanent)}}}
	text, err := service.membershipFeedback(context.Background(), row, "en-US", "ready")
	if err != nil || !strings.Contains(text, "Permanent") {
		t.Fatalf("permanent entitlement missing: %q %v", text, err)
	}
	service.Access = feedbackAccess{group: m.AccessGroup{TargetID: 20, Active: true, Kind: "limited"}}
	if _, err := service.membershipFeedback(context.Background(), row, "en-US", "ready"); !errors.Is(err, m.ErrConflict) {
		t.Fatalf("missing expiry was presented as permanent: %v", err)
	}
}

// preparationFixture supplies local authorization and an execution fence, with no network adapter.
type preparationFixture struct {
	m.ProactiveRepository
	row *d.ProactiveDelivery
}

// Proactive returns the durable responsibility to the send-time guard.
func (fixture preparationFixture) Proactive(context.Context, uint) (*d.ProactiveDelivery, error) {
	return fixture.row, nil
}

// ValidateProactive accepts only the configured local source for this isolated test.
func (fixture preparationFixture) ValidateProactive(context.Context, *d.ProactiveDelivery, time.Time) error {
	return nil
}

// MembershipRoute returns the exact generation and physical target used by the test.
func (fixture preparationFixture) MembershipRoute(context.Context, uint) (m.AccessRoute, error) {
	return m.AccessRoute{BotID: 1, ChatID: -20, RouteVersion: 1, Generation: 2}, nil
}

// Check represents a live synthetic runtime lease without a platform call.
func (fixture preparationFixture) Check(context.Context, uint, uint64) error { return nil }

// TestPreparationImmediateIdempotentAndAuthorized checks next-tick feedback and stale rejection.
func TestPreparationImmediateIdempotentAndAuthorized(t *testing.T) {
	now := time.Now().UTC()
	row := &d.ProactiveDelivery{ID: 4, BotID: 1, AccountID: 7, TelegramID: 88, TargetID: 20, ChatID: -20, RouteVersion: 1, BindingVersion: 3, State: "preparing", InviteState: "not_requested", CreatedAt: now}
	fixture := preparationFixture{row: row}
	queue := &reconcileQueue{receipts: map[string]r.InviteDeliveryStatus{}}
	service := &Service{Store: fixture, Routes: fixture, Queue: queue, Cipher: plainCipher{}, Now: func() time.Time { return now }}
	ctx := r.WithExecutionFence(context.Background(), fixture)
	// Simulate a lost orchestration save: the durable queue key must still suppress replay.
	for range 2 {
		row.Generation = 0
		if err := service.process(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.inserted) != 1 || row.State != "preparing" || row.Stage != "" {
		t.Fatalf("preparation stalled or replayed: row=%+v count=%d", row, len(queue.inserted))
	}
	var message r.Message
	if err := json.Unmarshal([]byte(queue.inserted[0].PayloadCipher), &message); err != nil {
		t.Fatal(err)
	}
	if message.ProactiveDelivery.Stage != "preparing" || message.PanelEpoch != "" || message.Operation != "send" {
		t.Fatalf("unexpected preparation envelope: %+v", message)
	}
	if err := service.AuthorizeProactiveDelivery(ctx, 1, message); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"ready", "unknown", "panel", "identity", "button"} {
		candidate := message
		row.InviteState, row.Stage = "not_requested", ""
		switch scenario {
		case "ready":
			row.Stage = "ready"
		case "unknown":
			row.InviteState = "unknown"
		case "panel":
			candidate.PanelEpoch = "forged"
		case "identity":
			candidate.IdentityStamp = "wrong"
		case "button":
			candidate.Buttons = [][]r.Button{{{Text: "unexpected", URL: "https://example.invalid"}}}
		}
		var invalid r.DeliveryInvalidated
		if err := service.AuthorizeProactiveDelivery(ctx, 1, candidate); !errors.As(err, &invalid) {
			t.Fatalf("accepted invalid preparation %s: %v", scenario, err)
		}
	}
}

// TestReceiptUnknownQueuesFeedbackWithoutReplayingInvite verifies independent idempotency.
func TestReceiptUnknownQueuesFeedbackWithoutReplayingInvite(t *testing.T) {
	queue := &reconcileQueue{receipts: map[string]r.InviteDeliveryStatus{"invite-original": {State: "unknown"}}}
	service := &Service{Queue: queue, Cipher: plainCipher{}}
	row := &d.ProactiveDelivery{ID: 3, BotID: 1, AccountID: 7, TelegramID: 88, TargetID: 20, State: "queued", Stage: "ready", BusinessKey: "invite-original"}
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.receipt(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	if row.State != "unknown" || row.BusinessKey != "invite-original" || len(queue.inserted) != 1 {
		t.Fatalf("unknown receipt replayed or lost: %+v, notifications=%d", row, len(queue.inserted))
	}
	notice := queue.inserted[0]
	if notice.Operation != "send" || notice.Purpose != "membership_proactive" || notice.Key == row.BusinessKey || notice.TargetMessageID != 0 {
		t.Fatalf("feedback overwrote invitation or panel: %+v", notice)
	}
}
