package application

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"
	"testing"
	"time"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// pendingAccess separates explicit Request from asynchronous worker completion.
type pendingAccess struct {
	*membershipAccessStub
	existing c.MembershipInvitation
	readHook func()
}

// GetExisting reads only; readHook injects concurrent navigation for CAS tests.
func (access *pendingAccess) GetExisting(context.Context, c.Actor, uint) (c.MembershipInvitation, error) {
	if access.readHook != nil {
		access.readHook()
	}
	return access.existing, nil
}

// Request records explicit intent and exposes processing to read-only retries.
func (access *pendingAccess) Request(ctx context.Context, actor c.Actor, target uint) (c.MembershipInvitation, error) {
	access.membershipAccessStub.Request(ctx, actor, target)
	access.existing = c.MembershipInvitation{State: "processing"}
	return access.existing, nil
}

// replacedOrderPanel builds a factual old-group order and a receipt-backed panel.
func replacedOrderPanel(t *testing.T) (*Service, *memoryStore, *routingIdentity, *pendingAccess, []rt.Message) {
	t.Helper()
	service, store, identity, access := membershipAccessSetup()
	access.page.Items[0].Replaced = true
	access.page.Items[0].HistoricalChats = []string{"-10010", "-10020"}
	reader := &pendingAccess{membershipAccessStub: access, existing: c.MembershipInvitation{State: "available"}}
	service.SetMembershipAccess(reader)
	service.d.Orders = &snapshotOrdersStub{backend: service.d.Orders.(*backend), groups: access.page.Items}
	messages := run(t, service, update(1, "/start"))
	messages = click(t, service, 2, messages, text("zh-CN", "orders"))
	messages = click(t, service, 3, messages, "ORDER · ")
	key := sessionKey(1, 11, 11)
	row := store.sessions[key]
	row.ID, row.PanelMessageID, row.PanelMessageEpoch, row.PanelKind, row.PanelState = 1, 77, row.PanelEpoch, "text", "ready"
	store.sessions[key] = row
	return service, store, identity, reader, messages
}

// TestReplacedOrderExplicitRequest verifies history, scoped callback and same-panel URL.
func TestReplacedOrderExplicitRequest(t *testing.T) {
	service, store, _, access, messages := replacedOrderPanel(t)
	if !strings.Contains(messages[0].Text, "-10010") || !strings.Contains(messages[0].Text, "-10020") || !strings.Contains(messages[0].Text, "旧群已更换") || strings.Contains(messages[0].Text, "封") || access.requests != 0 {
		t.Fatal("invalid history projection")
	}
	for _, action := range savedState(t, service, store).Actions {
		if action.Kind == "membership_apply" && (action.ID != 2 || action.Value != "8" || action.Owner != 7) {
			t.Fatal("unscoped application")
		}
	}
	messages = click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
	if access.requests != 1 || savedState(t, service, store).MembershipPending == nil {
		t.Fatal("explicit request not tracked")
	}
	access.existing = access.result
	messages, err := service.RefreshMembershipPanel(context.Background(), 1, 11)
	if err != nil || len(messages) != 1 || messages[0].TargetMessageID != 77 || messages[0].Operation != "edit" || !hasMembershipURL(messages, access.result.InviteURL) || access.requests != 1 {
		t.Fatalf("completion: %+v %v", messages, err)
	}
	for _, act := range savedState(t, service, store).Actions {
		if act.Kind == "membership_apply" && act.ID == 2 {
			t.Fatal("URL kept its mutating callback")
		}
	}
	firstRevision := messages[0].PanelRevision
	messages, err = service.RefreshMembershipPanel(context.Background(), 1, 11)
	if err != nil || len(messages) != 1 || messages[0].PanelRevision != firstRevision {
		t.Fatal("enqueue repair rerendered")
	}
}

// TestMembershipInviteHistoryDoesNotRevokeCurrentRights covers all Bot panel entry paths.
// t runs in-memory callbacks in three locales; no external requests occur or values are returned.
func TestMembershipInviteHistoryDoesNotRevokeCurrentRights(t *testing.T) {
	for _, language := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, inviteState := range []string{"revoked", "expired", "unavailable"} {
			for _, entry := range []string{"memberships", "order"} {
				t.Run(language+"/"+inviteState+"/"+entry, func(t *testing.T) {
					service, _, _, access := membershipAccessSetup()
					reader := &pendingAccess{membershipAccessStub: access, existing: c.MembershipInvitation{State: inviteState}}
					service.SetMembershipAccess(reader)
					service.d.Orders = &snapshotOrdersStub{backend: service.d.Orders.(*backend), groups: access.page.Items}
					input := update(1, "/memberships")
					input.Locale = language
					if entry == "order" {
						input.Text = "/start"
					}
					messages := run(t, service, input)
					if entry == "order" {
						messages = click(t, service, 2, messages, text(language, "orders"))
						messages = click(t, service, 3, messages, "ORDER · ")
					}
					body := messages[0].Text
					if !strings.Contains(body, text(language, "membership_available")) || strings.Contains(body, text(language, "membership_revoked")) || strings.Contains(body, text(language, "membership_expired")) {
						t.Fatalf("invitation overwrote current entitlement: %s", body)
					}
					if inviteState != "unavailable" && !strings.Contains(body, text(language, "membership_invite_"+inviteState)) {
						t.Fatalf("historical invitation fact missing: %s", body)
					}
					if entry == "order" {
						messages = click(t, service, 4, messages, "Group")
					}
					messages = click(t, service, 5, messages, text(language, "refresh"))
					if reader.requests != 0 || messages[0].InviteDelivery != nil {
						t.Fatal("read-only refresh requested or disclosed an invitation")
					}
					click(t, service, 6, messages, text(language, "membership_apply")+" · Group")
					if reader.requests != 1 {
						t.Fatal("explicit application did not request current entitlement")
					}
				})
			}
		}
	}
}

// TestMembershipCurrentStateOverridesOldInvitation checks fail-closed display and callbacks.
// t provides only in-memory projections; returns no value and never authorizes real access.
func TestMembershipCurrentStateOverridesOldInvitation(t *testing.T) {
	for _, state := range []string{"revoked", "expired", "unknown", "recovering", "unavailable", "member"} {
		t.Run(state, func(t *testing.T) {
			service, store, _, access := membershipAccessSetup()
			reader := &pendingAccess{membershipAccessStub: access, existing: c.MembershipInvitation{State: "revoked"}}
			service.SetMembershipAccess(reader)
			messages := run(t, service, update(1, "/memberships"))
			callback := button(t, messages, text("zh-CN", "membership_apply")+" · Group")
			access.page.Items[0].State, access.page.Items[0].CanRequest = state, false
			reader.existing = access.result // A stale readable link must not override the newer group state.
			input := update(2, "")
			input.CallbackData = callback
			messages = run(t, service, input)
			if reader.requests != 0 || messages[0].InviteDelivery != nil || hasMembershipURL(messages, access.result.InviteURL) || !strings.Contains(messages[0].Text, text("zh-CN", "membership_"+state)) {
				t.Fatalf("current %s state ignored: %+v", state, messages)
			}
			for _, act := range savedState(t, service, store).Actions {
				if act.Kind == "membership_apply" {
					t.Fatal("unavailable entitlement kept a request action")
				}
			}
		})
	}
}

// TestMembershipCompletionIsolation covers navigation, identity and physical/CAS races.
func TestMembershipCompletionIsolation(t *testing.T) {
	for _, scenario := range []string{"navigation", "identity", "physical", "unknown", "cas"} {
		t.Run(scenario, func(t *testing.T) {
			service, store, identity, access, messages := replacedOrderPanel(t)
			messages = click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
			access.existing = access.result
			key := sessionKey(1, 11, 11)
			switch scenario {
			case "navigation":
				click(t, service, 5, messages, text("zh-CN", "back"))
			case "identity":
				identity.generation = "rebound"
			case "physical":
				row := store.sessions[key]
				row.PanelMessageID++
				store.sessions[key] = row
			case "unknown":
				row := store.sessions[key]
				row.PanelState = "unknown"
				store.sessions[key] = row
			case "cas":
				access.readHook = func() { row := store.sessions[key]; row.Version++; store.sessions[key] = row }
			}
			result, err := service.RefreshMembershipPanel(context.Background(), 1, 11)
			if len(result) != 0 || (err != nil && !errors.Is(err, c.ErrDenied) && !errors.Is(err, rt.ErrConflict)) || access.requests != 1 {
				t.Fatalf("isolation failed: %+v %v", result, err)
			}
		})
	}
}

// TestReplacedOrderClickRevalidates rejects obsolete rights and bindings before Request.
func TestReplacedOrderClickRevalidates(t *testing.T) {
	for _, scenario := range []string{"entitlement", "target", "beneficiary", "binding"} {
		t.Run(scenario, func(t *testing.T) {
			service, _, identity, access, messages := replacedOrderPanel(t)
			switch scenario {
			case "entitlement":
				access.page.Items[0].CanRequest = false
			case "target":
				access.page.Items[0].TargetID = "3"
			case "beneficiary":
				access.page.Items[0].BeneficiaryTelegramID = "12"
			case "binding":
				identity.generation = "rebound"
			}
			click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
			if access.requests != 0 {
				t.Fatal("obsolete callback requested access")
			}
		})
	}
}

// TestMembershipPendingSessionRetry repairs failed CAS without repeating Request.
func TestMembershipPendingSessionRetry(t *testing.T) {
	service, store, _, access, messages := replacedOrderPanel(t)
	input := update(4, "")
	input.CallbackData = button(t, messages, text("zh-CN", "membership_apply")+" · Group")
	store.fail = true
	if _, err := service.Handle(context.Background(), 1, input); !errors.Is(err, rt.ErrConflict) {
		t.Fatal(err)
	}
	run(t, service, input)
	if access.requests != 1 || savedState(t, service, store).MembershipPending == nil {
		t.Fatal("failed session save lost intent or repeated Request")
	}
}

// TestMembershipUnknownApplicationStaysReadOnly never retries uncertain delivery.
func TestMembershipUnknownApplicationStaysReadOnly(t *testing.T) {
	service, _, _, access, messages := replacedOrderPanel(t)
	access.existing = c.MembershipInvitation{State: "unknown"}
	click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
	if access.requests != 0 {
		t.Fatal("unknown delivery retried")
	}
}

// TestMembershipMultiTargetCompletion selects the requested target, never two URLs.
func TestMembershipMultiTargetCompletion(t *testing.T) {
	service, _, _, access, messages := replacedOrderPanel(t)
	orders := service.d.Orders.(*snapshotOrdersStub)
	orders.groups = append([]c.MembershipGroup{{TargetID: "1", BeneficiaryTelegramID: "11", Title: "Other", State: "available", Permanent: true, CanRequest: true}}, orders.groups...)
	messages = click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
	access.existing = access.result
	messages, err := service.RefreshMembershipPanel(context.Background(), 1, 11)
	if err != nil || len(messages) != 1 || messages[0].InviteDelivery == nil || messages[0].InviteDelivery.TargetID != 2 || access.requests != 1 {
		t.Fatal(messages, err)
	}
}

// panelQueueStore preserves key receipts and injects a recoverable enqueue failure.
type panelQueueStore struct {
	*memoryStore
	failQueue  bool
	deliveries map[string]rd.Delivery
}

// PanelSessionsAfter returns one session for bounded reconciliation tests.
func (store *panelQueueStore) PanelSessionsAfter(context.Context, uint, uint, int, time.Time) ([]rd.Session, error) {
	return []rd.Session{store.sessions[sessionKey(1, 11, 11)]}, nil
}

// Enqueue models idempotent unknown receipts without sending external messages.
func (store *panelQueueStore) Enqueue(_ context.Context, delivery *rd.Delivery) error {
	if store.failQueue {
		store.failQueue = false
		return errors.New("outbox unavailable")
	}
	if _, exists := store.deliveries[delivery.Key]; !exists {
		store.deliveries[delivery.Key] = *delivery
	}
	return nil
}

// TestMembershipPendingOutboxRepair retries one durable revision/key after failure.
func TestMembershipPendingOutboxRepair(t *testing.T) {
	service, store, _, access, messages := replacedOrderPanel(t)
	click(t, service, 4, messages, text("zh-CN", "membership_apply")+" · Group")
	access.existing = access.result
	queue := &panelQueueStore{memoryStore: store, failQueue: true, deliveries: map[string]rd.Delivery{}}
	service.d.Store = queue
	if service.ReconcileMembershipPanels(context.Background(), 1) == nil {
		t.Fatal("missing enqueue failure")
	}
	revision := savedState(t, service, store).PanelRevision
	for repeat := 0; repeat < 3; repeat++ {
		if err := service.ReconcileMembershipPanels(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		for key, delivery := range queue.deliveries {
			delivery.Status = "unknown"
			queue.deliveries[key] = delivery
		}
	}
	if len(queue.deliveries) != 1 || savedState(t, service, store).PanelRevision != revision || access.requests != 1 {
		t.Fatal("lost pending update or repeated unknown delivery")
	}
}

// renewalOrders supplies immutable multi-item order snapshots for read-only renewal.
type renewalOrders struct {
	*snapshotOrdersStub
	items []c.OrderItem
}

// Detail returns the original items without ordering or charging.
func (orders *renewalOrders) Detail(_ context.Context, _ c.Actor, id, owner uint) (*c.Order, error) {
	return &c.Order{ID: id, UserID: owner, Number: "ORDER", Status: "completed", Items: orders.items}, nil
}

// TestMembershipRenewalReadOnlySKUAndBack checks exact SKU and non-replaying back.
func TestMembershipRenewalReadOnlySKUAndBack(t *testing.T) {
	service, store, _, access := membershipAccessSetup()
	backend := service.d.Orders.(*backend)
	orders := &renewalOrders{snapshotOrdersStub: &snapshotOrdersStub{backend: backend, groups: access.page.Items}, items: []c.OrderItem{{ProductID: 2, SKUID: 20, Title: "Membership", Quantity: 1}, {ProductID: 2, SKUID: 20, Title: "Membership", Quantity: 1}}}
	service.d.Orders = orders
	service.d.Catalog = displayCatalog{product: c.Product{ID: 2, Title: "Membership", Available: true, Min: 1, SKUs: []c.SKU{{ID: 20, Available: true}, {ID: 21, Available: true}}}}
	messages := run(t, service, update(1, "/start"))
	messages = click(t, service, 2, messages, text("zh-CN", "orders"))
	messages = click(t, service, 3, messages, "ORDER · ")
	state := savedState(t, service, store)
	count := 0
	for _, act := range state.Actions {
		if act.Kind == "membership_renew" {
			count++
		}
	}
	var label string
	for _, row := range messages[0].Buttons {
		for _, button := range row {
			if strings.HasPrefix(button.Text, text("zh-CN", "rebuy")) {
				label = button.Text
			}
		}
	}
	if count != 1 || label == "" {
		t.Fatal("missing renewal entry")
	}
	messages = click(t, service, 4, messages, label)
	state = savedState(t, service, store)
	if state.Selection.SKUID != 20 || state.Selection.ProductID != 2 || backend.creates != 0 || backend.payments != 0 || access.requests != 0 {
		t.Fatal("renewal mutated or changed SKU")
	}
	click(t, service, 5, messages, text("zh-CN", "nav_back"))
	if savedState(t, service, store).Current.Kind != "order" || backend.creates != 0 {
		t.Fatal("back replayed renewal")
	}
}

// TestMembershipRenewalUnavailableAndForeignOwner keeps stale snapshots and foreign actors read-only.
func TestMembershipRenewalUnavailableAndForeignOwner(t *testing.T) {
	for _, scenario := range []string{"stopped", "deleted", "removed_sku", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			service, _, _, access := membershipAccessSetup()
			backend := service.d.Orders.(*backend)
			service.d.Orders = &renewalOrders{snapshotOrdersStub: &snapshotOrdersStub{backend: backend, groups: access.page.Items}, items: []c.OrderItem{{ProductID: 2, SKUID: 20}}}
			product := c.Product{ID: 2, Available: true, Min: 1, SKUs: []c.SKU{{ID: 20, Available: true}}}
			location := action{Kind: "membership_renew", ID: 8, Owner: 7, Page: 1}
			switch scenario {
			case "stopped":
				product.Available = false
			case "deleted":
				product.ID = 0
			case "removed_sku":
				product.SKUs = nil
			case "foreign":
				location.Owner = 8
			}
			service.d.Catalog = displayCatalog{product: product}
			view := &view{service: service, state: &state{}, actor: c.Actor{BotID: 1, UserID: 7, TelegramID: 11, Locale: "zh-CN"}, update: update(1, "")}
			err := view.renewMembership(context.Background(), location)
			if scenario == "foreign" && err != c.ErrDenied {
				t.Fatalf("foreign owner accepted: %v", err)
			}
			if scenario != "foreign" && (err != nil || view.state.Current.Kind != "order") {
				t.Fatalf("stale purchase accepted: %v", err)
			}
			if backend.creates != 0 || backend.payments != 0 || access.requests != 0 || view.state.Quote != nil {
				t.Fatal("unavailable renewal produced effects")
			}
		})
	}
}

// snapshotOrdersStub supplies only the immutable targets associated with an authorized order.
type snapshotOrdersStub struct {
	*backend
	groups  []c.MembershipGroup
	orderID uint
}

// Targets records the order scope and returns a fixture without reading account-wide rights.
func (stub *snapshotOrdersStub) Targets(_ context.Context, _ c.Actor, orderID, _ uint) ([]c.MembershipGroup, error) {
	stub.orderID = orderID
	return stub.groups, nil
}

// TestOrderMembershipTargetsKeepSingleDeliveryScope checks per-order association and one invite per message.
func TestOrderMembershipTargetsKeepSingleDeliveryScope(t *testing.T) {
	for _, count := range []int{1, 2} {
		service, store, _, access := membershipAccessSetup()
		reader := &existingAccessStub{membershipAccessStub: access}
		service.SetMembershipAccess(reader)
		orders := &snapshotOrdersStub{backend: service.d.Orders.(*backend), groups: append([]c.MembershipGroup(nil), access.page.Items...)}
		if count == 2 {
			orders.groups = append(orders.groups, c.MembershipGroup{TargetID: "3", BeneficiaryTelegramID: "11", Title: "Second"})
		}
		service.d.Orders = orders
		messages := run(t, service, update(1, "/start"))
		messages = click(t, service, 2, messages, text("zh-CN", "orders"))
		messages = click(t, service, 3, messages, "ORDER · ")
		if orders.orderID != 8 || reader.requests != 0 {
			t.Fatal("missing order scope or mutating invite read")
		}
		if (messages[0].InviteDelivery != nil) != (count == 1) {
			t.Fatal("multiple targets disclosed multiple invites")
		}
		st := savedState(t, service, store)
		for _, act := range st.Actions {
			if act.Kind == "memberships" {
				t.Fatal("order detail points to unrelated account-wide targets")
			}
		}
	}
}

// TestMembershipDetailBackRestoresCursor verifies the list cursor survives target navigation.
func TestMembershipDetailBackRestoresCursor(t *testing.T) {
	service, store, _, access := membershipAccessSetup()
	reader := &existingAccessStub{membershipAccessStub: access}
	reader.page.Items = append(reader.page.Items, c.MembershipGroup{TargetID: "3", Title: "Second", BeneficiaryTelegramID: "11", Permanent: true, State: "available"})
	service.SetMembershipAccess(reader)
	messages := run(t, service, update(1, "/memberships"))
	st := savedState(t, service, store)
	st.Current.ID = 1
	setSavedState(t, service, store, st)
	messages = click(t, service, 2, messages, "Group")
	messages = click(t, service, 3, messages, text("zh-CN", "nav_back"))
	st = savedState(t, service, store)
	if st.Current.Kind != "memberships" || st.Current.ID != 1 || reader.requests != 0 {
		t.Fatal("membership back lost list context")
	}
}

type membershipAccessStub struct {
	page     c.MembershipPage
	result   c.MembershipInvitation
	actors   []c.Actor
	requests int
	cursors  []uint
}

// List records the trusted actor and returns a secret-free projection without writes.
func (stub *membershipAccessStub) List(_ context.Context, actor c.Actor, cursor uint, limit int) (c.MembershipPage, error) {
	stub.actors = append(stub.actors, actor)
	stub.cursors = append(stub.cursors, cursor)
	return stub.page, nil
}

// Request records only explicit requests; production adapters supply durable deduplication.
func (stub *membershipAccessStub) Request(_ context.Context, actor c.Actor, target uint) (c.MembershipInvitation, error) {
	stub.actors = append(stub.actors, actor)
	stub.requests++
	return stub.result, nil
}

// membershipAccessSetup returns a real handler and a verified, generation-stamped identity.
func membershipAccessSetup() (*Service, *memoryStore, *routingIdentity, *membershipAccessStub) {
	service, store, backend := setup()
	identity := &routingIdentity{backend: backend, generation: "verified:7"}
	service.d.Identity = identity
	expires := time.Now().Add(time.Hour)
	stub := &membershipAccessStub{
		page:   c.MembershipPage{Items: []c.MembershipGroup{{TargetID: "2", Title: "Group", BeneficiaryTelegramID: "11", Permanent: true, State: "available", CanRequest: true}}},
		result: c.MembershipInvitation{InviteDelivery: &rt.InviteDeliveryRef{InviteID: 3, TargetID: 2, BindingVersion: 1, RouteVersion: 1, EntitlementVersion: 1, Generation: 1}, State: "available", InviteURL: "https://t.me/+real-invite", ExpiresAt: &expires},
	}
	service.SetMembershipAccess(stub)
	return service, store, identity, stub
}

// TestMembershipAccessHandler verifies locales, escaped facts, explicit requests and read-only refresh.
func TestMembershipAccessHandler(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		t.Run(locale, func(t *testing.T) {
			service, _, identity, access := membershipAccessSetup()
			access.page.Items[0].Title = `<b>VIP & group</b>`
			deadline := time.Now().Add(time.Hour)
			access.page.Items[0].ExpiresAt = &deadline
			input := update(1, "/memberships")
			input.Locale = locale
			messages := run(t, service, input)
			body := messages[0].Text
			if identity.creates != 0 || access.requests != 0 || strings.Contains(body, access.page.Items[0].Title) || !strings.Contains(body, html.EscapeString(access.page.Items[0].Title)) {
				t.Fatalf("unsafe list: %s", body)
			}
			if !strings.Contains(body, text(locale, "membership_permanent")) || strings.Contains(body, text(locale, "expires")) {
				t.Fatal("permanent membership displayed deadline")
			}
			messages = click(t, service, 2, messages, text(locale, "refresh"))
			if access.requests != 0 {
				t.Fatal("refresh issued invitation")
			}
			messages = click(t, service, 3, messages, text(locale, "membership_apply")+" · "+access.page.Items[0].Title)
			if access.requests != 1 || !hasMembershipURL(messages, access.result.InviteURL) {
				t.Fatal("explicit request failed")
			}
			messages = click(t, service, 4, messages, text(locale, "refresh"))
			if access.requests != 1 || hasMembershipURL(messages, access.result.InviteURL) {
				t.Fatal("list generated or leaked link")
			}
			for _, actor := range access.actors {
				if actor.UserID != 7 || actor.TelegramID != 11 || actor.BotID != 1 || actor.Generation != identity.generation {
					t.Fatalf("wrong actor: %+v", actor)
				}
			}
		})
	}
}

// hasMembershipURL inspects real response buttons without relying on text rendering.
func hasMembershipURL(messages []rt.Message, expected string) bool {
	for _, message := range messages {
		for _, row := range message.Buttons {
			for _, button := range row {
				if button.URL == expected {
					return true
				}
			}
		}
	}
	return false
}

// TestMembershipAccessStates checks all states and refuses stale availability before an API request.
func TestMembershipAccessStates(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, status := range []string{"processing", "available", "recovering", "unknown", "expired", "revoked", "identity_mismatch", "unavailable", "member", "future_state"} {
			t.Run(locale+"/"+status, func(t *testing.T) {
				service, store, _, access := membershipAccessSetup()
				messages := run(t, service, update(1, "/memberships"))
				callback := button(t, messages, text("zh-CN", "membership_apply")+" · Group")
				access.page.Items[0].State = status
				input := update(2, "")
				input.CallbackData = callback
				if status != "available" {
					run(t, service, input)
				}
				if access.requests != 0 {
					t.Fatal("non-available state issued invitation")
				}
				saved := savedState(t, service, store)
				saved.Locale = locale
				setSavedState(t, service, store, saved)
				messages = run(t, service, update(3, "/memberships"))
				if !strings.Contains(messages[0].Text, text(locale, "membership_"+membershipState(status))) {
					t.Fatal(messages)
				}
				for _, act := range savedState(t, service, store).Actions {
					if act.Kind == "membership_request" && status != "available" {
						t.Fatal("unsafe request button")
					}
				}
			})
		}
	}
}

// TestMembershipAccessBindingChange retires buttons for account changes and same-account rebindings.
func TestMembershipAccessBindingChange(t *testing.T) {
	for _, changedAccount := range []bool{false, true} {
		service, _, identity, access := membershipAccessSetup()
		messages := run(t, service, update(1, "/memberships"))
		input := update(2, "")
		input.CallbackData = button(t, messages, text("zh-CN", "membership_apply")+" · Group")
		identity.generation = "new-binding"
		if changedAccount {
			identity.user, identity.history = 9, []uint{7}
		}
		run(t, service, input)
		if access.requests != 0 || len(access.actors) != 1 || identity.creates != 0 {
			t.Fatal("old binding reused")
		}
	}
}

// TestMembershipAccessInvalidProjection blocks malformed IDs and mismatching beneficiaries before rendering.
func TestMembershipAccessInvalidProjection(t *testing.T) {
	for _, invalid := range []string{"0", "-1", "+2", "02", "2.0", "18446744073709551616"} {
		for _, beneficiary := range []bool{false, true} {
			service, store, _, access := membershipAccessSetup()
			if beneficiary {
				access.page.Items[0].BeneficiaryTelegramID = invalid
			} else {
				access.page.Items[0].TargetID = invalid
			}
			run(t, service, update(1, "/memberships"))
			for _, act := range savedState(t, service, store).Actions {
				if act.Kind == "membership_request" {
					t.Fatal("invalid ID accepted")
				}
			}
			if access.requests != 0 {
				t.Fatal("invalid projection requested")
			}
		}
	}
	service, _, identity, access := membershipAccessSetup()
	identity.history = []uint{3}
	access.page.Items[0].BeneficiaryTelegramID = "12"
	messages := run(t, service, update(1, "/memberships"))
	if strings.Contains(messages[0].Text, "Group") {
		t.Fatal("another beneficiary leaked")
	}
	identity.user = 0
	if _, err := service.Handle(context.Background(), 1, update(2, "/memberships")); err != nil || identity.creates != 0 {
		t.Fatal("query registered account")
	}
}

// TestMembershipAccessDiscoverability verifies account/order entry points without private notifications.
func TestMembershipAccessDiscoverability(t *testing.T) {
	for _, entry := range []string{"account", "orders"} {
		service, _, _, access := membershipAccessSetup()
		messages := run(t, service, update(1, "/start"))
		messages = click(t, service, 2, messages, text("zh-CN", entry))
		messages = click(t, service, 3, messages, text("zh-CN", "memberships"))
		if !strings.Contains(messages[0].Text, "Group") || access.requests != 0 {
			t.Fatal("entry unavailable")
		}
	}
}

// TestMembershipAccessUnknownResultAndReplay never retries unknown outcomes on refresh or old callbacks.
func TestMembershipAccessUnknownResultAndReplay(t *testing.T) {
	service, _, _, access := membershipAccessSetup()
	access.result.State = "unknown"
	messages := run(t, service, update(1, "/memberships"))
	input := update(2, "")
	input.CallbackData = button(t, messages, text("zh-CN", "membership_apply")+" · Group")
	messages = run(t, service, input)
	run(t, service, input)
	if hasMembershipURL(messages, access.result.InviteURL) || access.requests != 1 {
		t.Fatal("unknown replay")
	}
	input.ID = 3
	run(t, service, input)
	if access.requests != 1 {
		t.Fatal("old callback repeated request")
	}
}

// TestMembershipAccessTextRestorationCannotRegister rejects unbound text restoration too.
func TestMembershipAccessTextRestorationCannotRegister(t *testing.T) {
	service, _, identity, _ := membershipAccessSetup()
	run(t, service, update(1, "/memberships"))
	identity.user = 0
	if _, err := service.Handle(context.Background(), 1, update(2, "refresh please")); err != nil || identity.creates != 0 {
		t.Fatal("membership panel restoration registered an account")
	}
}

// TestMembershipAccessPagination uses exclusive validated cursors and keeps all reads side-effect free.
func TestMembershipAccessPagination(t *testing.T) {
	service, _, _, access := membershipAccessSetup()
	access.page.NextCursor = "2"
	messages := run(t, service, update(1, "/memberships"))
	access.page = c.MembershipPage{Items: []c.MembershipGroup{{TargetID: "3", BeneficiaryTelegramID: "11", Title: "Next", State: "expired"}}}
	messages = click(t, service, 2, messages, text("zh-CN", "page_next"))
	if access.cursors[len(access.cursors)-1] != 2 || !strings.Contains(messages[0].Text, "Next") || access.requests != 0 {
		t.Fatal("cursor pagination failed")
	}
}

// TestMembershipAccessInvalidLinks excludes fallback pages, credentials and expired invitations.
func TestMembershipAccessInvalidLinks(t *testing.T) {
	for _, link := range []string{"https://support.example/help", "https://t.me/help", "http://t.me/+invite", "https://user@t.me/+invite", "https://t.me/+invite?token=secret", "https://t.me/+invite#secret"} {
		service, _, _, access := membershipAccessSetup()
		access.result.InviteURL = link
		messages := run(t, service, update(1, "/memberships"))
		messages = click(t, service, 2, messages, text("zh-CN", "membership_apply")+" · Group")
		if hasMembershipURL(messages, link) {
			t.Fatalf("unsafe invite accepted: %s", link)
		}
	}
	service, store, _, access := membershipAccessSetup()
	past := time.Now().Add(-time.Hour)
	access.page.Items[0].Permanent, access.page.Items[0].ExpiresAt = false, &past
	run(t, service, update(1, "/memberships"))
	for _, act := range savedState(t, service, store).Actions {
		if act.Kind == "membership_request" {
			t.Fatal("expired right requested")
		}
	}
}

// TestMembershipAccessOptionalPort displays unavailable without changing constructor contracts.
func TestMembershipAccessOptionalPort(t *testing.T) {
	service, _, _ := setup()
	messages := run(t, service, update(1, "/memberships"))
	if !strings.Contains(messages[0].Text, text("zh-CN", "membership_unavailable")) {
		t.Fatal(messages)
	}
}

type membershipOrders struct {
	*backend
	previews int
}

// Preview records any accidental payable quote request from the membership panel.
func (orders *membershipOrders) Preview(ctx context.Context, actor c.Actor, selection c.Selection) (*c.Quote, error) {
	orders.previews++
	return orders.backend.Preview(ctx, actor, selection)
}

// TestMembershipActionsAndLocales exercises real persisted callback routing for every term and locale.
func TestMembershipActionsAndLocales(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		t.Run(locale, func(t *testing.T) {
			service, store, backend := setup()
			orders := &membershipOrders{backend: backend}
			service.d.Orders = orders
			product := c.Product{ID: 2, Title: "Membership", Membership: true, Min: 5, Max: 99, GroupNames: []string{"Second group", `<a href="https://evil.invalid">first & group</a>`}}
			for index, months := range []int64{2, 5, 18, 0} {
				kind := "months"
				if months == 0 {
					kind = "permanent"
				}
				product.SKUs = append(product.SKUs, c.SKU{ID: uint(index + 10), Name: "not a term", MembershipTerm: &c.MembershipTerm{Kind: kind, Months: months}})
			}
			service.d.Catalog = displayCatalog{product: product}
			initial := update(1, "/start")
			initial.Locale = locale
			messages := run(t, service, initial)
			messages = click(t, service, 2, messages, text(locale, "catalog"))
			for index, sku := range product.SKUs {
				messages = click(t, service, int64(index+3), messages, membershipTermLabel(locale, sku.MembershipTerm))
				saved := savedState(t, service, store)
				if saved.Selection.SKUID != sku.ID || saved.Selection.Quantity != 1 || saved.Quote != nil || saved.Operation != "" {
					t.Fatalf("unsafe membership selection: %+v", saved.Selection)
				}
				if !strings.Contains(messages[0].Text, text(locale, "membership_closed")) {
					t.Fatal(messages)
				}
				if !strings.Contains(messages[0].Text, product.GroupNames[0]+"\n\n"+html.EscapeString(product.GroupNames[1])) || strings.Contains(messages[0].Text, product.GroupNames[1]) {
					t.Fatalf("unsafe or reordered group names: %s", messages[0].Text)
				}
				for _, act := range saved.Actions {
					switch act.Kind {
					case "delta", "quantity", "quantity_input", "more_quantity", "confirm", "pay":
						t.Fatalf("unexpected action: %+v", act)
					}
				}
			}
			if orders.previews != 0 || backend.creates != 0 || backend.payments != 0 {
				t.Fatal("membership invoked checkout")
			}
		})
	}
}

// TestEnabledMembershipCheckout reuses the normal quote/confirm flow for enabled membership.
// t runs the in-memory Bot handler only; no Telegram request or real payment is made.
func TestEnabledMembershipCheckout(t *testing.T) {
	service, store, backend := setup()
	orders := &membershipOrders{backend: backend}
	service.d.Orders = orders
	service.d.Catalog = displayCatalog{product: c.Product{ID: 2, Title: "Membership", Membership: true, Available: true, Min: 1, Max: 1, SKUs: []c.SKU{{ID: 10, Name: "2 months", Available: true, MembershipTerm: &c.MembershipTerm{Kind: "months", Months: 2}}}}}
	messages := run(t, service, update(1, "/start"))
	messages = click(t, service, 2, messages, text("zh-CN", "catalog"))
	saved := savedState(t, service, store)
	if saved.Selection.Quantity != 1 || saved.Quote == nil || saved.Operation == "" || orders.previews != 1 {
		t.Fatalf("enabled quote unavailable: %+v", saved)
	}
	confirm := ""
	for token, act := range saved.Actions {
		if act.Kind == "confirm" {
			confirm = token
		}
	}
	if confirm == "" || len(messages) == 0 {
		t.Fatal("enabled membership omitted confirm")
	}
	input := update(3, "")
	input.CallbackData = button(t, messages, text("zh-CN", "confirm")+" · "+money(saved.Quote.Currency, saved.Quote.Amount))
	run(t, service, input)
	if backend.creates != 1 {
		t.Fatal("enabled membership failed to create")
	}
}

// TestMembershipLegacyCallbacksFailClosed rejects persisted quantity/confirm buttons after conversion.
func TestMembershipLegacyCallbacksFailClosed(t *testing.T) {
	for _, kind := range []string{"delta", "quantity", "more_quantity", "quantity_input", "noop", "confirm"} {
		t.Run(kind, func(t *testing.T) {
			service, store, backend := setup()
			messages := quoteFlow(t, service)
			if len(messages) == 0 {
				t.Fatal("ordinary checkout unavailable")
			}
			product := c.Product{ID: 2, Title: "DEFAULT", Membership: true, SKUs: []c.SKU{{ID: 10, Name: "DEFAULT", MembershipTerm: &c.MembershipTerm{Kind: "months", Months: 2}}}}
			service.d.Catalog = displayCatalog{product: product}
			orders := &membershipOrders{backend: backend}
			service.d.Orders = orders
			saved := savedState(t, service, store)
			saved.Actions["legacy"] = action{Kind: kind, Value: "1000"}
			saved.MoreQuantity, saved.Step = true, "quantity"
			setSavedState(t, service, store, saved)
			callback := update(10, "")
			callback.CallbackData = "s:" + saved.Ref + ":" + strconv.FormatUint(saved.PanelRevision, 36) + ":legacy"
			run(t, service, callback)
			saved = savedState(t, service, store)
			if saved.Selection.Quantity != 1 || saved.Selection.SKUID != 10 || saved.Step != "" || saved.MoreQuantity || saved.Quote != nil || saved.Operation != "" {
				t.Fatalf("legacy %s bypassed: %+v", kind, saved.Selection)
			}
			if orders.previews != 0 || backend.creates != 0 {
				t.Fatal("legacy callback invoked checkout")
			}
		})
	}
}
