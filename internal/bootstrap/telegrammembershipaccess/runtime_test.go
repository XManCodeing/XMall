package telegrammembershipaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	assembly "github.com/XManCodeing/XMall/internal/bootstrap/telegrammembershiprecovery"
	"github.com/XManCodeing/XMall/internal/constants"
	external "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	externalstore "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/infrastructure/gormstore"
	users "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	groupstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	memberapp "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	memberdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	routerecovery "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	botreplacement "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	recoverydomain "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	recoverystore "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	shop "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"reflect"
	"strings"
	"testing"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	groupdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// migrationProbe records only real platform observations for the read-only runtime bridge.
type migrationProbe struct {
	calls        int
	observations []membership.AccessObservation
	route        membership.AccessRoute
	change       bool
	outcome      string
}

// PendingIdentityMigrations returns a single explicitly selected test scope.
func (probe *migrationProbe) PendingIdentityMigrations(context.Context, uint, uint, int) ([]membership.IdentityMigrationWork, error) {
	return []membership.IdentityMigrationWork{{ID: 1, TargetID: 1, BotID: 2, ChatID: -1001, RouteVersion: 3, OldTelegramID: 41, NewTelegramID: 42}}, nil
}

// ObserveAccess records trusted facts, without inferring an approval or a removal.
func (probe *migrationProbe) ObserveAccess(_ context.Context, input membership.AccessObservation) (bool, error) {
	probe.observations = append(probe.observations, input)
	return true, nil
}

// MembershipRoute returns the current route snapshot used before and after HTTP.
func (probe *migrationProbe) MembershipRoute(context.Context, uint) (membership.AccessRoute, error) {
	return probe.route, nil
}

// GetMembershipMember simulates an actual member response and optional route change during HTTP.
func (probe *migrationProbe) GetMembershipMember(context.Context, membership.AccessJoinAction) membership.AccessMemberResult {
	probe.calls++
	if probe.change {
		probe.route.RouteVersion++
	}
	return membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: probe.outcome}, Status: "member", IsMember: true}
}

// Check represents a currently valid supervised runtime lease in this test.
func (probe *migrationProbe) Check(context.Context, uint, uint64) error { return nil }

// TestMigrationObservationFence verifies no unleased reads and no stale/unknown admission evidence.
func TestMigrationObservationFence(t *testing.T) {
	for _, scenario := range []string{"ok", "no_fence", "changed", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			probe := &migrationProbe{route: membership.AccessRoute{TargetID: 1, BotID: 2, ChatID: -1001, RouteVersion: 3, Generation: 4}, outcome: membership.AccessSucceeded}
			ctx := runtime.WithExecutionFence(context.Background(), probe)
			if scenario == "no_fence" {
				ctx = context.Background()
			}
			if scenario == "changed" {
				probe.change = true
			}
			if scenario == "unknown" {
				probe.outcome = membership.AccessUnknown
			}
			bridge := &IdentityMigrations{Store: probe, Routes: probe, Platform: probe}
			_, err := bridge.Scan(ctx, 2, 0, 25)
			if scenario == "ok" {
				if err != nil || len(probe.observations) != 2 {
					t.Fatalf("missing observations: %v", err)
				}
			} else if err == nil || len(probe.observations) != 0 {
				t.Fatalf("unsafe observations: %v", err)
			}
			if scenario == "no_fence" && probe.calls != 0 {
				t.Fatal("unleased platform read")
			}
		})
	}
}

// TestReminderLocales verifies all supported languages, Shanghai formatting and stable non-secret entry commands.
func TestReminderLocales(t *testing.T) {
	row := memberdomain.Reminder{TargetID: 9, Days: 7, ExpiresAt: time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)}
	for locale, marker := range map[string]string{"zh-CN": "会员", "zh-TW": "會員", "en-US": "Membership"} {
		value := reminderText(locale, row)
		for _, expected := range []string{marker, "2026-11-02 09:00", "/memberships", "/start"} {
			if !strings.Contains(value, expected) {
				t.Fatal(locale, value)
			}
		}
		if strings.Contains(value, "t.me/+") {
			t.Fatal("invite leaked")
		}
	}
}

// TestReminderShanghaiQuietHours checks both boundaries with a non-Shanghai input location.
func TestReminderShanghaiQuietHours(t *testing.T) {
	for _, scenario := range []struct{ input, expected string }{
		{"2026-11-02T13:59:59Z", "2026-11-02T13:59:59Z"},
		{"2026-11-02T14:00:00Z", "2026-11-03T00:00:00Z"},
		{"2026-11-02T23:59:59Z", "2026-11-03T00:00:00Z"},
		{"2026-11-03T00:00:00Z", "2026-11-03T00:00:00Z"},
	} {
		input, err := time.Parse(time.RFC3339, scenario.input)
		if err != nil {
			t.Fatal(err)
		}
		if actual := memberdomain.ReminderAvailable(input.In(time.FixedZone("other", -7*3600))).Format(time.RFC3339); actual != scenario.expected {
			t.Fatal(actual, scenario.expected)
		}
	}
}

// actorStore provides the actual managed bot identity independently of event claims.
type actorStore struct {
	runtime.Store
	instance d.Instance
}

// Get returns the current synthetic runtime identity for actor provenance tests.
func (store actorStore) Get(context.Context, uint) (*d.Instance, error) {
	instance := store.instance
	return &instance, nil
}

// TestMemberActorProvenance rejects forged bot flags, other bots, missing identity and stale generations.
func TestMemberActorProvenance(t *testing.T) {
	for _, scenario := range []string{"managed", "human", "other_bot", "missing", "external", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			bridge, sink := runtimeFixture()
			instance := d.Instance{ID: 2, TelegramID: 12345, Generation: 4, Enabled: true, Mode: "internal", State: "running"}
			actor := runtime.UserReference{ID: 12345, IsBot: true}
			switch scenario {
			case "human":
				actor.IsBot = false
			case "other_bot":
				actor.ID++
			case "external":
				instance.Mode = "external"
			case "stale":
				instance.Generation++
			}
			if scenario != "missing" {
				bridge.Bots = actorStore{instance: instance}
			}
			update := runtime.Update{ID: 12, Type: "chat_member", ChatID: -1009, ChatType: "supergroup", Member: &runtime.MemberUpdate{Operator: actor, Subject: runtime.UserReference{ID: 77}, Date: time.Now().Unix(), New: runtime.ChatMember{User: runtime.UserReference{ID: 77}, Status: "kicked"}}}
			if _, err := bridge.Handle(context.Background(), 2, update); err != nil {
				t.Fatal(err)
			}
			if sink.observed == nil || sink.observed.ActorIsManagedBot != (scenario == "managed") {
				t.Fatalf("wrong actor attribution: %+v", sink.observed)
			}
		})
	}
}

// eventStore restricts the test route by the exact bot/chat filter.
type eventStore struct {
	groups.Store
	target groupdomain.Target
}

// List implements the precise registration lookup used for trusted platform events.
func (store eventStore) List(_ context.Context, filter groups.Filter) ([]groupdomain.Target, int64, error) {
	if filter.BotID != store.target.BotInstanceID || filter.ChatID != store.target.TelegramChatID || filter.Purpose != "membership" {
		return nil, 0, nil
	}
	return []groupdomain.Target{store.target}, 1, nil
}

// eventRoutes supplies a deterministic fresh execution route for isolation tests.
type eventRoutes struct{ route membership.AccessRoute }

// MembershipRoute returns only the requested registered logical target.
func (routes eventRoutes) MembershipRoute(_ context.Context, target uint) (membership.AccessRoute, error) {
	if target != routes.route.TargetID {
		return membership.AccessRoute{}, membership.ErrCapability
	}
	return routes.route, nil
}

// eventSink records observations without declaring approvals to be group admission.
type eventSink struct {
	join           *membership.AccessJoinRequest
	observed       *membership.AccessObservation
	runs           int
	reconcileRuns  int
	scanLimit      int
	lifecycleLimit int
}

// ReceiveMembershipJoin captures only trusted numeric applicant data.
func (sink *eventSink) ReceiveMembershipJoin(_ context.Context, input membership.AccessJoinRequest) error {
	sink.join = &input
	return nil
}

// ObserveMembership records a separate platform observation.
func (sink *eventSink) ObserveMembership(_ context.Context, input membership.AccessObservation) (bool, error) {
	sink.observed = &input
	return true, nil
}

// RunAccess asserts bounded runtime reconciliation and makes no Telegram calls.
func (sink *eventSink) RunAccess(_ context.Context, _ uint, limit int) (int, error) {
	sink.runs = limit
	return 0, nil
}

// RunAccessReconcile records the independent bounded read queue dispatch.
func (sink *eventSink) RunAccessReconcile(_ context.Context, _ uint, limit int) (int, error) {
	sink.reconcileRuns = limit
	return 0, nil
}

// Scan records the bounded durable cursor page limit.
func (sink *eventSink) Scan(_ context.Context, _ uint, limit int) (int, error) {
	sink.scanLimit = limit
	return 0, nil
}

// Run records the bounded lifecycle phase limit, returning no simulated work.
func (sink *eventSink) Run(_ context.Context, _ uint, limit int) (int, error) {
	sink.lifecycleLimit = limit
	return 0, nil
}

// runtimeFixture creates one registered group; no contact or account provisioning exists in the bridge.
func runtimeFixture() (*Runtime, *eventSink) {
	sink := &eventSink{}
	bridge := &Runtime{Service: sink, Lifecycle: sink, Groups: eventStore{target: groupdomain.Target{ID: 9, BotInstanceID: 2, TelegramChatID: -1009}}, Routes: eventRoutes{route: membership.AccessRoute{TargetID: 9, BotID: 2, ChatID: -1009, RouteVersion: 3, Generation: 4}}}
	return bridge, sink
}

// TestRuntimeJoinUsesApplicantNotTemporaryChat proves the event needs no private contact and never uses user_chat_id as identity.
func TestRuntimeJoinUsesApplicantNotTemporaryChat(t *testing.T) {
	bridge, sink := runtimeFixture()
	update := runtime.Update{ID: 11, Type: "chat_join_request", ChatID: -1009, ChatType: "supergroup", UserID: 999, JoinRequest: &runtime.JoinRequest{User: runtime.UserReference{ID: 77}, UserChatID: 888, Date: time.Now().Unix(), InviteLink: &runtime.InviteLink{InviteLink: "https://t.me/+synthetic-only", CreatesJoinRequest: true}}}
	messages, err := bridge.Handle(context.Background(), 2, update)
	if err != nil || len(messages) != 0 || sink.join == nil || sink.join.TelegramID != 77 || sink.join.ChatID != -1009 || sink.observed != nil {
		t.Fatalf("unexpected join dispatch: join=%+v err=%v", sink.join, err)
	}
	update.JoinRequest.InviteLink.IsRevoked = true
	if _, err := bridge.Handle(context.Background(), 2, update); err != nil || sink.join.InviteLink != "" {
		t.Fatal("revoked platform link must never reach trusted invitation matching")
	}
}

// TestRuntimeUnknownPhysicalGroupsDoNotEnrollMembers protects system-external members and route separation.
func TestRuntimeUnknownPhysicalGroupsDoNotEnrollMembers(t *testing.T) {
	bridge, sink := runtimeFixture()
	update := runtime.Update{ID: 12, Type: "chat_member", ChatID: -9999, ChatType: "supergroup", Member: &runtime.MemberUpdate{Subject: runtime.UserReference{ID: 77}, Date: time.Now().Unix(), New: runtime.ChatMember{User: runtime.UserReference{ID: 77}, Status: "member"}}}
	if _, err := bridge.Handle(context.Background(), 2, update); err != nil || sink.observed != nil {
		t.Fatal("unregistered group observation reached member enrollment")
	}
	update.ChatID = -1009
	update.Member.New.Status = "restricted"
	update.Member.New.IsMember = true
	if _, err := bridge.Handle(context.Background(), 2, update); err != nil || sink.observed == nil || !sink.observed.IsMember || sink.observed.TelegramID != 77 || sink.observed.Sequence != 12 {
		t.Fatalf("restricted member observation lost facts: %+v %v", sink.observed, err)
	}
}

// entryProbe implements isolated platform effects, never real network or contacts.
type entryProbe struct {
	db                        *gorm.DB
	newStatus, oldStatus      string
	creates, approves, bans   int
	denied                    map[uint]bool
	actors                    []uint
	unknownCreate, unknownBan bool
	failedBan                 bool
	unbans                    int
}

// Seal keeps synthetic payloads readable for deterministic tests only.
func (p *entryProbe) Seal(value string) (string, error) { return value, nil }

// Open decodes synthetic test-only ciphertext.
func (p *entryProbe) Open(value string) (string, error) { return value, nil }

// Check emulates the actual runtime lease identity, distinct from recovery authorization.
func (p *entryProbe) Check(_ context.Context, bot uint, generation uint64) error {
	if bot != 1 || generation != 7 {
		return membership.ErrConflict
	}
	return nil
}

// MembershipRoute is intentionally unavailable: frozen routes cannot use ordinary access.
func (p *entryProbe) MembershipRoute(context.Context, uint) (membership.AccessRoute, error) {
	return membership.AccessRoute{}, membership.ErrCapability
}

// MembershipGroupPlatform returns synthetic validated platform implementations.
func (p *entryProbe) MembershipGroupPlatform(uint, uint, membership.GroupRecoveryAuthorization) (membership.AccessPlatform, membership.LifecyclePlatform) {
	return p, p
}

// CreateMembershipInvite issues an approval-only synthetic invite.
func (p *entryProbe) CreateMembershipInvite(context.Context, membership.AccessCreateInvite) membership.AccessInviteResult {
	p.creates++
	if p.unknownCreate {
		return membership.AccessInviteResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessUnknown}}
	}
	return membership.AccessInviteResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Link: "https://t.me/+abcdefghijk"}
}

// ApproveMembershipJoin does not invent actual membership.
func (p *entryProbe) ApproveMembershipJoin(context.Context, membership.AccessJoinAction) membership.AccessAPIResult {
	p.approves++
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// DeclineMembershipJoin returns a deterministic result.
func (p *entryProbe) DeclineMembershipJoin(context.Context, membership.AccessJoinAction) membership.AccessAPIResult {
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// RevokeMembershipInvite returns a deterministic result.
func (p *entryProbe) RevokeMembershipInvite(context.Context, membership.AccessRoute, string) membership.AccessAPIResult {
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// GetMembershipMember queries SQL during simulated HTTP to detect single-connection deadlocks.
func (p *entryProbe) GetMembershipMember(ctx context.Context, input membership.AccessJoinAction) membership.AccessMemberResult {
	var count int64
	if err := p.db.WithContext(ctx).Model(&memberdomain.LedgerSource{}).Count(&count).Error; err != nil {
		return membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessUnknown}}
	}
	status := p.newStatus
	if input.Route.ChatID == -10 {
		status = p.oldStatus
	}
	return membership.AccessMemberResult{AccessAPIResult: membership.AccessAPIResult{Outcome: membership.AccessSucceeded}, Status: status, IsMember: status == "member"}
}

// BanMembershipMember only removes an old member after test-verified destination arrival.
func (p *entryProbe) BanMembershipMember(context.Context, membership.AccessJoinAction) membership.AccessAPIResult {
	p.bans++
	if p.failedBan {
		return membership.AccessAPIResult{Outcome: membership.AccessFailed}
	}
	if p.unknownBan {
		return membership.AccessAPIResult{Outcome: membership.AccessUnknown}
	}
	p.oldStatus = "kicked"
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// UnbanMembershipMember completes existing lifecycle-owned cleanup.
func (p *entryProbe) UnbanMembershipMember(context.Context, membership.AccessJoinAction) membership.AccessAPIResult {
	p.unbans++
	p.oldStatus = "left"
	return membership.AccessAPIResult{Outcome: membership.AccessSucceeded}
}

// authorize checks the actual acting administrator, recording it without impersonation.
func (p *entryProbe) authorize(_ context.Context, actor uint, _ string) error {
	p.actors = append(p.actors, actor)
	if p.denied[actor] {
		return membership.ErrConflict
	}
	return nil
}

// ListUserAccess gives a frozen projection; CanRequest must come from real recovery guards.
func (p *entryProbe) ListUserAccess(context.Context, uint, uint, int, time.Time) ([]membership.UserAccessGroup, error) {
	return []membership.UserAccessGroup{{TargetID: 2, BeneficiaryTelegramID: 42, State: "recovering"}}, nil
}

// entryFixture wires real SQLite stores and real membership services with only fake HTTP.
func entryFixture(t *testing.T) (*Website, *Runtime, *routerecovery.GroupRecovery, routerecovery.Request, *entryProbe) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:entry-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	entryMust(t, err)
	sqlDB, err := db.DB()
	entryMust(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { entryMust(t, sqlDB.Close()) })
	models := append(memberstore.AccessModels(), memberstore.LedgerModels()...)
	models = append(models, routerecovery.Models()...)
	models = append(models, &memberdomain.Reminder{}, &memberdomain.OrderItemSnapshot{})
	models = append(models, &users.User{}, &external.Identity{}, &groupdomain.Target{}, &groupdomain.Route{}, &groupdomain.RecoveryFence{}, &d.Instance{}, &d.Inbox{}, &d.Delivery{}, &recoverydomain.Task{}, &recoverydomain.ScopeSlot{})
	entryMust(t, db.AutoMigrate(models...))
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	entryMust(t, db.Create(&users.User{ID: 3, Email: "test@example.invalid", Status: constants.UserStatusActive}).Error)
	entryMust(t, db.Create(&external.Identity{ID: 1, UserID: 3, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "42"}).Error)
	entryMust(t, db.Create(&d.Instance{ID: 1, TelegramID: 100, Mode: "internal", Enabled: true, State: "running", Generation: 7}).Error)
	entryMust(t, db.Create(&groupdomain.Target{ID: 2, RecoveryTaskID: 3, Purpose: "membership", Enabled: true, Version: 9, RouteVersion: 5, BotInstanceID: 1, TelegramChatID: -20, OccupancySlot: "chat:-20", ObservationSeq: 1, Observation: groupdomain.Observation{Generation: 7, RouteVersion: 5, Sequence: 1, State: "ready", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, ObservedAt: &now}}).Error)
	entryMust(t, db.Create(&[]groupdomain.Route{{TargetID: 2, BotInstanceID: 1, TelegramChatID: -10, RouteVersion: 4, RetiredAt: &past}, {TargetID: 2, BotInstanceID: 1, TelegramChatID: -20, RouteVersion: 5}}).Error)
	entryMust(t, db.Create(&groupdomain.RecoveryFence{TaskID: 3, TargetID: 2, OldBotID: 1, NewBotID: 1, OldChatID: -10, NewChatID: -20, InitialRouteVersion: 4, ReplacedVersion: 9}).Error)
	snapshot := recoverydomain.Snapshot{Old: recoverydomain.Endpoint{TargetID: 2, BotID: 1, ChatID: -10}, New: recoverydomain.Endpoint{TargetID: 2, BotID: 1, ChatID: -20}, ExpectedOld: recoverydomain.Versions{Route: 4}, ExpectedNew: recoverydomain.Versions{Generation: 7}, Scope: recoverydomain.Scope{TargetIDs: []string{"2"}, BotIDs: []string{"1"}, ChatIDs: []string{"-10", "-20"}}}
	raw, err := json.Marshal(snapshot)
	entryMust(t, err)
	entryMust(t, db.Create(&recoverydomain.Task{ID: 3, Kind: recoverydomain.GroupReplace, State: recoverydomain.Recovering, ConfirmedActorID: 7, ConfirmedAt: &now, Version: 1, SnapshotJSON: string(raw)}).Error)
	for _, key := range []string{"target:2", "bot:1", "chat:-10", "chat:-20"} {
		entryMust(t, db.Create(&recoverydomain.ScopeSlot{Key: key, TaskID: 3}).Error)
	}
	entryMust(t, db.Create(&memberdomain.LedgerSource{ID: 1, TargetID: 2, AccountID: 3, OrderID: 4, OrderItemID: 5, Operation: "grant", BeneficiaryTelegramID: 42, PaidAt: past, Kind: memberdomain.TermMonths, Months: 2}).Error)
	entryMust(t, db.Create(&memberdomain.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 1}).Error)
	entryMust(t, db.Create(&memberdomain.AccessMember{TargetID: 2, ChatID: -10, TelegramID: 42, BotID: 1, RouteVersion: 4, SourceID: 1, Status: "member", IsMember: true, ObservedAt: past, ObservationSource: "query"}).Error)
	probe := &entryProbe{db: db, newStatus: "left", oldStatus: "member", denied: map[uint]bool{}}
	identity := Identity{Users: userstore.New(db), Bindings: externalstore.New(db)}
	parents := recoverystore.New(db)
	groups := groupstore.New(db)
	authority := &assembly.GroupAuthority{Parents: parents, Authorize: probe.authorize}
	store, err := routerecovery.New(db)
	entryMust(t, err)
	worker := &routerecovery.GroupRecovery{Store: store, Cipher: probe, Platforms: probe, Authority: authority, Actor: authority.Actor, Authorizer: identity}
	service, err := memberapp.NewAccess(memberstore.New(db), probe, identity, probe, probe)
	entryMust(t, err)
	recovery := &Recovery{Groups: groups, Parents: parents, Worker: worker}
	website := &Website{Identity: identity, Reader: probe, Access: service, Invites: memberstore.New(db), Groups: groups, Recovery: recovery}
	bridge := &Runtime{Service: service, Groups: groups, Routes: probe, Bots: runtimestore.New(db), Recovery: recovery}
	input := routerecovery.Request{TaskID: 3, ActorID: 8, TargetID: 2, BotID: 1, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, Generation: 7, Purpose: "membership"}
	return website, bridge, worker, input, probe
}

// entryMust fails immediately for synthetic fixture or service errors.
func entryMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryWebsiteInboxCleanup follows website -> durable inbox -> actual arrival -> cleanup.
func TestRecoveryWebsiteInboxCleanup(t *testing.T) {
	website, bridge, worker, input, probe := entryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var original memberdomain.LedgerSource
	entryMust(t, probe.db.First(&original, 1).Error)
	page, err := website.List(ctx, 3, 0, 20)
	entryMust(t, err)
	if !page.Items[0].CanRequest {
		t.Fatal("recovery website button unavailable")
	}
	entry, err := website.Request(ctx, 3, 2)
	entryMust(t, err)
	if entry.State != "processing" {
		t.Fatal(entry.State)
	}
	_, err = worker.RecoverRoute(ctx, input)
	entryMust(t, err)
	entry, err = website.Request(ctx, 3, 2)
	entryMust(t, err)
	if entry.InviteURL == "" {
		t.Fatal("website invitation absent")
	}
	bot := &Bot{Website: website}
	response, err := bot.Request(ctx, shop.Actor{BotID: 1, UserID: 3, TelegramID: 42, Generation: "1:3"}, 2)
	if err == nil || response.InviteURL != "" {
		t.Fatal("bot borrowed website secret")
	}
	update := runtime.Update{ID: 9, Type: "chat_join_request", ChatID: -20, ChatType: "supergroup", JoinRequest: &runtime.JoinRequest{User: runtime.UserReference{ID: 42}, Date: time.Now().Unix(), InviteLink: &runtime.InviteLink{InviteLink: entry.InviteURL, CreatesJoinRequest: true}}}
	leased := runtime.WithExecutionFence(ctx, probe)
	if _, err := bridge.Handle(leased, 1, update); err == nil {
		t.Fatal("unpersisted update accepted")
	}
	raw, err := json.Marshal(update)
	entryMust(t, err)
	queue := runtimestore.New(probe.db)
	entryMust(t, queue.Accept(ctx, 1, 7, []d.Inbox{{UpdateID: 9, PayloadCipher: string(raw), AvailableAt: time.Now()}}, 10))
	inbox, err := queue.ClaimInbox(ctx, 1, "test-processing", time.Now().Add(time.Minute))
	entryMust(t, err)
	if inbox == nil {
		t.Fatal("inbox not claimed")
	}
	forged := update
	if _, err := bridge.Handle(ctx, 1, update); err == nil {
		t.Fatal("unleased event accepted")
	}
	forged.JoinRequest = &runtime.JoinRequest{User: runtime.UserReference{ID: 43}, Date: update.JoinRequest.Date, InviteLink: update.JoinRequest.InviteLink}
	if _, err := bridge.Handle(leased, 1, forged); err == nil {
		t.Fatal("changed persisted payload accepted")
	}
	forged = update
	forged.ID = 10
	if _, err := bridge.Handle(leased, 1, forged); err == nil {
		t.Fatal("forged update accepted")
	}
	_, err = bridge.Handle(leased, 1, update)
	entryMust(t, err)
	entryMust(t, queue.FinishInbox(ctx, inbox.ID, inbox.Claim, "done", "", time.Now()))
	result, err := worker.RecoverRoute(ctx, input)
	entryMust(t, err)
	if probe.approves != 1 || probe.bans != 0 || result.Verified {
		t.Fatal("approval treated as membership")
	}
	probe.newStatus = "member"
	entryMemberEvent(t, ctx, bridge, probe, queue)
	for range 5 {
		result, err = worker.RecoverRoute(ctx, input)
		entryMust(t, err)
	}
	if !result.Verified || probe.bans != 1 || probe.creates != 1 {
		t.Fatalf("cleanup incomplete %+v", result)
	}
	var run routerecovery.Run
	entryMust(t, probe.db.First(&run, "task_id = ? AND target_id = ?", 3, 2).Error)
	if run.CompletionReceipt == "" {
		t.Fatal("missing receipt")
	}
	var current memberdomain.LedgerSource
	entryMust(t, probe.db.First(&current, 1).Error)
	if !reflect.DeepEqual(original, current) {
		t.Fatal("original paid source changed")
	}
}

// entryMemberEvent exercises the second native subscription on the frozen target.
func entryMemberEvent(t *testing.T, ctx context.Context, bridge *Runtime, probe *entryProbe, queue *runtimestore.Store) {
	t.Helper()
	update := runtime.Update{ID: 11, Type: "chat_member", ChatID: -20, ChatType: "supergroup", Member: &runtime.MemberUpdate{Subject: runtime.UserReference{ID: 42}, Date: time.Now().Unix(), New: runtime.ChatMember{User: runtime.UserReference{ID: 42}, Status: "member", IsMember: true}}}
	raw, err := json.Marshal(update)
	entryMust(t, err)
	entryMust(t, queue.Accept(ctx, 1, 7, []d.Inbox{{UpdateID: 11, PayloadCipher: string(raw), AvailableAt: time.Now()}}, 12))
	inbox, err := queue.ClaimInbox(ctx, 1, "member-event", time.Now().Add(time.Minute))
	entryMust(t, err)
	if inbox == nil {
		t.Fatal("member event unclaimed")
	}
	_, err = bridge.Handle(runtime.WithExecutionFence(ctx, probe), 1, update)
	entryMust(t, err)
	entryMust(t, queue.FinishInbox(ctx, inbox.ID, inbox.Claim, "done", "", time.Now()))
}

// TestRecoveryActorAndRouteIsolation distinguishes admin resume from sponsor-authorized system reads.
func TestRecoveryActorAndRouteIsolation(t *testing.T) {
	website, _, worker, input, probe := entryFixture(t)
	ctx := context.Background()
	probe.denied[7] = true
	if _, err := website.Request(ctx, 3, 2); err == nil {
		t.Fatal("revoked sponsor authorized system entry")
	}
	probe.actors = nil
	_, err := worker.RecoverRoute(ctx, input)
	entryMust(t, err)
	for _, actor := range probe.actors {
		if actor != 8 {
			t.Fatal("resume impersonated confirmer")
		}
	}
	probe.denied[8] = true
	probe.denied[7] = false
	if _, err := worker.RecoverRoute(ctx, input); err == nil {
		t.Fatal("revoked resume actor substituted")
	}
	input.ActorID = 0
	if _, err := worker.RecoverRoute(ctx, input); !errors.Is(err, membership.ErrInvalid) {
		t.Fatal("missing actor permitted")
	}
	entryMust(t, probe.db.Model(&recoverydomain.Task{}).Where("id=3").Update("kind", "bot_replace").Error)
	page, err := website.List(ctx, 3, 0, 20)
	entryMust(t, err)
	if page.Items[0].CanRequest {
		t.Fatal("cross-bot task enabled button")
	}
	if _, err := website.Request(ctx, 3, 2); err == nil {
		t.Fatal("cross-bot task bypassed fence")
	}
}

// MembershipBotAdmission supplies the same fake HTTP endpoints under dual fences.
func (p *entryProbe) MembershipBotAdmission(membership.BotRouteRecoveryRequest, membership.MembershipRecoveryAuthorizer) (membership.AccessPlatform, membership.LifecyclePlatform) {
	return p, p
}

// MembershipRecoveryPlatform provides legacy inventory effects without admission authority.
func (p *entryProbe) MembershipRecoveryPlatform(membership.BotRouteRecoveryRequest) membership.RecoveryAccessPlatform {
	return p
}

// crossEntryFixture replaces only persisted runtime/parent facts; no worker is started.
func crossEntryFixture(t *testing.T) (*Website, *Runtime, *routerecovery.BotRecovery, membership.BotRouteRecoveryRequest, *entryProbe) {
	t.Helper()
	website, bridge, group, input, probe := entryFixture(t)
	db := probe.db
	entryMust(t, db.AutoMigrate(&d.GenerationExit{}))
	entryMust(t, db.Model(&d.Instance{}).Where("id=1").Updates(map[string]any{"recovery_task_id": 3, "recovery_version": 5, "version": 5, "recovery_generation": 7, "state": "stopped"}).Error)
	entryMust(t, db.Create(&d.Instance{ID: 2, TelegramID: 200, Mode: "internal", State: "stopped", Version: 6, Generation: 2, RecoveryTaskID: 3, RecoveryVersion: 6, RecoveryGeneration: 2}).Error)
	now := time.Now().UTC()
	for bot, generations := range map[uint]uint64{1: 7, 2: 2} {
		for generation := uint64(1); generation <= generations; generation++ {
			entryMust(t, db.Create(&d.GenerationExit{BotID: bot, Generation: generation, JoinedAt: &now}).Error)
		}
	}
	entryMust(t, db.Model(&groupdomain.Target{}).Where("id=2").Updates(map[string]any{"bot_instance_id": 2, "obs_generation": 2}).Error)
	entryMust(t, db.Model(&groupdomain.Route{}).Where("target_id=2 AND route_version=5").Update("bot_instance_id", 2).Error)
	entryMust(t, db.Create(&recoverydomain.ScopeSlot{Key: "bot:2", TaskID: 3}).Error)
	snapshot := recoverydomain.Snapshot{Old: recoverydomain.Endpoint{TargetID: 2, BotID: 1, ChatID: -10}, New: recoverydomain.Endpoint{TargetID: 2, BotID: 2, ChatID: -20}, ExpectedOld: recoverydomain.Versions{Config: 5, Generation: 7}, ExpectedNew: recoverydomain.Versions{Config: 6, Generation: 2}, Scope: recoverydomain.Scope{TargetIDs: []string{"2"}, BotIDs: []string{"1", "2"}, ChatIDs: []string{"-10", "-20"}}, BotRoutes: []recoverydomain.BotRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4, Purpose: "membership", Enabled: true}}, BotHistory: []recoverydomain.BotRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4, Purpose: "membership"}}}
	raw, err := json.Marshal(snapshot)
	entryMust(t, err)
	entryMust(t, db.Model(&recoverydomain.Task{}).Where("id=3").Updates(map[string]any{"kind": "bot_replace", "snapshot_json": string(raw)}).Error)
	authority := &assembly.BotAuthority{Store: group.Store, Parents: recoverystore.New(db), Authorize: probe.authorize}
	worker := &routerecovery.BotRecovery{Store: group.Store, Cipher: probe, Platforms: probe, Admission: probe, Authority: authority, Authorizer: website.Identity, Sponsor: authority.Sponsor}
	website.Recovery.(*Recovery).BotWorker = worker
	request := membership.BotRouteRecoveryRequest{TaskID: 3, ActorID: input.ActorID, TargetID: 2, OldBotID: 1, NewBotID: 2, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, OldVersion: 5, NewVersion: 6, OldGeneration: 7, NewGeneration: 2}
	return website, bridge, worker, request, probe
}

// TestCrossBotWebsiteActualArrivalCleanup exercises real repositories on one SQLite
// connection. Stopped runtimes never approve or invent an inbox; manual admission is
// verified by a task-bound query before the old Bot's lifecycle removal.
func TestCrossBotWebsiteActualArrivalCleanup(t *testing.T) {
	website, bridge, worker, input, probe := crossEntryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var before memberdomain.LedgerSource
	entryMust(t, probe.db.First(&before, 1).Error)
	page, err := website.List(ctx, 3, 0, 20)
	entryMust(t, err)
	if !page.Items[0].CanRequest {
		t.Fatal("website recovery unavailable")
	}
	_, err = website.Request(ctx, 3, 2)
	entryMust(t, err)
	result, err := worker.RecoverBotRoute(ctx, input)
	entryMust(t, err)
	if result.Complete || probe.creates != 1 || probe.bans != 0 || probe.approves != 0 {
		t.Fatal("stopped runtime fabricated admission")
	}
	entry, err := website.Request(ctx, 3, 2)
	entryMust(t, err)
	if entry.InviteURL == "" {
		t.Fatal("website invitation missing")
	}
	update := runtime.Update{ID: 99, Type: "chat_join_request", ChatID: -20, ChatType: "supergroup", JoinRequest: &runtime.JoinRequest{User: runtime.UserReference{ID: 42}, Date: time.Now().Unix()}}
	if _, err := bridge.Handle(ctx, 2, update); err == nil {
		t.Fatal("stopped runtime accepted fabricated event")
	}
	for range 2 {
		result, err = worker.RecoverBotRoute(ctx, input)
		entryMust(t, err)
	}
	if result.Complete || probe.approves != 0 || probe.bans != 0 || probe.creates != 1 {
		t.Fatal("waiting phase replayed effects")
	}
	probe.newStatus = "member"
	for range 6 {
		result, err = worker.RecoverBotRoute(ctx, input)
		entryMust(t, err)
	}
	if !result.Complete || result.CompletionReceipt == "" || probe.bans != 1 || probe.approves != 0 {
		t.Fatalf("incomplete cleanup %+v", result)
	}
	var after memberdomain.LedgerSource
	inventory := membership.BotRecoveryInventory{Identity: input, Current: []membership.BotRecoveryRoute{{TargetID: 2, ChatID: -10, NewChatID: -20, RouteVersion: 4, Purpose: "membership"}}, History: []membership.BotRecoveryRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4, Purpose: "membership"}}}
	inventory.Identity.TargetID = 0
	inventory.Identity.OldChatID = 0
	inventory.Identity.NewChatID = 0
	inventory.Identity.OldRouteVersion = 0
	inventory.Identity.NewRouteVersion = 0
	proof, err := worker.Store.CompleteBotInventory(ctx, inventory)
	entryMust(t, err)
	if !proof.Complete || proof.CompletionReceipt == "" {
		t.Fatalf("module proof missing %+v", proof)
	}
	inventory.Identity.ActorID = 9
	replayed, err := worker.Store.CompleteBotInventory(ctx, inventory)
	entryMust(t, err)
	if replayed.CompletionReceipt != proof.CompletionReceipt {
		t.Fatal("actor rebound immutable inventory")
	}
	entryMust(t, probe.db.First(&after, 1).Error)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("paid source changed")
	}
	var bots []d.Instance
	entryMust(t, probe.db.Find(&bots).Error)
	for _, bot := range bots {
		if bot.State != "stopped" || bot.RecoveryTaskID != 3 {
			t.Fatal("runtime unlocked")
		}
	}
}

// TestCrossBotAdmissionRevocationAndExit proves system sponsor and admin remain separate.
func TestCrossBotAdmissionRevocationAndExit(t *testing.T) {
	website, _, worker, input, probe := crossEntryFixture(t)
	ctx := context.Background()
	probe.denied[7] = true
	if _, err := website.Request(ctx, 3, 2); err == nil {
		t.Fatal("revoked sponsor allowed")
	}
	probe.actors = nil
	_, err := worker.RecoverBotRoute(ctx, input)
	entryMust(t, err)
	for _, actor := range probe.actors {
		if actor != 8 {
			t.Fatal("resume impersonated sponsor")
		}
	}
	probe.denied[8] = true
	if _, err := worker.RecoverBotRoute(ctx, input); err == nil {
		t.Fatal("revoked actor allowed")
	}
	probe.denied[8] = false
	entryMust(t, probe.db.Model(&d.GenerationExit{}).Where("bot_id=1 AND generation=7").Update("joined_at", nil).Error)
	if _, err := worker.RecoverBotRoute(ctx, input); err == nil {
		t.Fatal("stopped state substituted actual exit")
	}
}

// TestCrossBotUnknownAndAdministratorRemainBlocked never replays uncertain effects.
func TestCrossBotUnknownAndAdministratorRemainBlocked(t *testing.T) {
	for _, scenario := range []string{"create_unknown", "ban_unknown", "administrator"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, worker, input, probe := crossEntryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if scenario == "create_unknown" {
				probe.unknownCreate = true
			} else {
				probe.newStatus = "member"
				if scenario == "ban_unknown" {
					probe.unknownBan = true
				} else {
					probe.oldStatus = "administrator"
				}
			}
			var result membership.BotRouteRecoveryResult
			for range 5 {
				var err error
				result, err = worker.RecoverBotRoute(ctx, input)
				entryMust(t, err)
			}
			if result.Complete || result.CompletionReceipt != "" {
				t.Fatal("uncertain/admin cleanup marked complete")
			}
			if scenario == "create_unknown" && probe.creates != 1 || scenario == "ban_unknown" && probe.bans != 1 || scenario == "administrator" && probe.bans != 0 {
				t.Fatal("unsafe effect replay")
			}
		})
	}
}

// TestCrossBotCleanupReadRecheck exercises real parent/RBAC/full inventory guards
// and both stopped runtime fences. No new management endpoint or fake success is used.
func TestCrossBotCleanupReadRecheck(t *testing.T) {
	for _, scenario := range []string{"failed", "administrator", "unknown", "kicked", "permission", "scope", "source", "active_lease"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, worker, input, probe := crossEntryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			probe.newStatus, probe.failedBan = "member", true
			if scenario == "administrator" {
				probe.oldStatus = "administrator"
			}
			if scenario == "unknown" {
				probe.failedBan, probe.unknownBan = false, true
			}
			for range 3 {
				_, err := worker.RecoverBotRoute(ctx, input)
				entryMust(t, err)
			}
			var operation memberdomain.AccessOperation
			entryMust(t, probe.db.Where("kind = ?", "lifecycle").First(&operation).Error)
			bans := probe.bans
			probe.oldStatus = "left"
			switch scenario {
			case "kicked":
				probe.oldStatus = "kicked"
			case "permission":
				probe.denied[input.ActorID] = true
			case "scope":
				entryMust(t, probe.db.Exec("DELETE FROM telegram_recovery_scope_slots WHERE key = 'chat:-10'").Error)
			case "source":
				entryMust(t, probe.db.Model(&memberdomain.AccessMember{}).Where("chat_id = ?", -10).Update("source_id", 999).Error)
			case "active_lease":
				entryMust(t, probe.db.Model(&operation).Updates(map[string]any{"state": "creating", "lease_token": "live", "lease_until": time.Now().Add(time.Hour)}).Error)
			}
			result, err := worker.RecoverBotRoute(ctx, input)
			want := scenario == "failed" || scenario == "administrator"
			if want {
				entryMust(t, err)
			}
			if result.Complete != want || probe.bans != bans || probe.unbans != 0 {
				t.Fatalf("cross Bot recheck escaped: %+v err=%v bans=%d unbans=%d", result, err, probe.bans, probe.unbans)
			}
			if want {
				entryMust(t, probe.db.First(&operation, operation.ID).Error)
				if operation.State != "succeeded" || operation.LifecyclePhase != "done" || operation.BanConfirmed {
					t.Fatal("missing real lifecycle verify")
				}
				result, err = worker.RecoverBotRoute(ctx, input)
				entryMust(t, err)
				if !result.Complete || probe.bans != bans || probe.unbans != 0 {
					t.Fatal("repeat changed effect")
				}
			}
		})
	}
}

// TestCrossBotPerRouteMappings uses two immutable destinations with no legacy endpoint.
// Website, authority and module continuation must agree, including complete history.
func TestCrossBotPerRouteMappings(t *testing.T) {
	website, _, worker, input, probe := crossEntryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parents := recoverystore.New(probe.db)
	detail, err := parents.Get(ctx, 3)
	entryMust(t, err)
	snapshot := detail.Snapshot
	snapshot.Old.TargetID = 0
	snapshot.Old.ChatID = 0
	snapshot.New.TargetID = 0
	snapshot.New.ChatID = 0
	snapshot.BotRoutes[0].NewChatID = -20
	snapshot.BotRoutes = append(snapshot.BotRoutes, recoverydomain.BotRoute{TargetID: 4, ChatID: -30, NewChatID: -40, RouteVersion: 4, Purpose: "membership", Enabled: true})
	snapshot.BotHistory = append(snapshot.BotHistory, recoverydomain.BotRoute{TargetID: 4, ChatID: -30, RouteVersion: 4, Purpose: "membership"})
	snapshot.Scope.TargetIDs = append(snapshot.Scope.TargetIDs, "4")
	snapshot.Scope.ChatIDs = append(snapshot.Scope.ChatIDs, "-30", "-40")
	var target groupdomain.Target
	entryMust(t, probe.db.First(&target, 2).Error)
	target.ID = 4
	target.TelegramChatID = -40
	target.OccupancySlot = "chat:-40"
	entryMust(t, probe.db.Create(&target).Error)
	past := time.Now().Add(-time.Hour)
	entryMust(t, probe.db.Create(&[]groupdomain.Route{{TargetID: 4, BotInstanceID: 1, TelegramChatID: -30, RouteVersion: 4, RetiredAt: &past}, {TargetID: 4, BotInstanceID: 2, TelegramChatID: -40, RouteVersion: 5}}).Error)
	for _, key := range []string{"target:4", "chat:-30", "chat:-40"} {
		entryMust(t, probe.db.Create(&recoverydomain.ScopeSlot{Key: key, TaskID: 3}).Error)
	}
	var source memberdomain.LedgerSource
	entryMust(t, probe.db.First(&source, 1).Error)
	source.ID = 2
	source.TargetID = 4
	source.OrderItemID = 6
	entryMust(t, probe.db.Create(&source).Error)
	entryMust(t, probe.db.Create(&memberdomain.LedgerProjection{TargetID: 4, BeneficiaryTelegramID: 42, Version: 1}).Error)
	save := func() {
		raw, err := json.Marshal(snapshot)
		entryMust(t, err)
		entryMust(t, probe.db.Model(&recoverydomain.Task{}).Where("id=3").Update("snapshot_json", string(raw)).Error)
	}
	save()
	for _, targetID := range []uint{2, 4} {
		_, err := website.Request(ctx, 3, targetID)
		entryMust(t, err)
	}
	probe.newStatus = "member"
	probe.oldStatus = "left"
	modules := assembly.Modules{Store: worker.Store, Recovery: worker}
	proofs, err := modules.ContinueBot(ctx, botreplacement.ModuleRequest{TaskID: 3, ActorID: 8, Snapshot: snapshot})
	entryMust(t, err)
	if len(proofs) != 1 || !proofs[0].LocalComplete || proofs[0].Receipt == "" {
		t.Fatalf("multi-target proof incomplete %+v", proofs)
	}
	var runs []routerecovery.Run
	entryMust(t, probe.db.Order("target_id").Find(&runs).Error)
	if len(runs) != 2 || runs[0].NewChatID != -20 || runs[1].NewChatID != -40 {
		t.Fatal("destinations collapsed")
	}
	forged := input
	forged.NewChatID = -40
	if _, err := worker.RecoverBotRoute(ctx, forged); err == nil {
		t.Fatal("cross-target mapping accepted")
	}
	snapshot.Old.TargetID = 2
	snapshot.Old.ChatID = -10
	snapshot.New.TargetID = 2
	snapshot.New.ChatID = -99
	save()
	if _, err := website.Request(ctx, 3, 2); err == nil {
		t.Fatal("website accepted conflicting endpoint")
	}
	if _, err := modules.ContinueBot(ctx, botreplacement.ModuleRequest{TaskID: 3, ActorID: 8, Snapshot: snapshot}); err == nil {
		t.Fatal("module accepted conflicting endpoint")
	}
	snapshot.Old.TargetID = 0
	snapshot.New.TargetID = 0
	snapshot.Old.ChatID = 0
	snapshot.New.ChatID = 0
	snapshot.BotRoutes[1].NewChatID = -99
	save()
	if _, err := website.Request(ctx, 3, 4); err == nil {
		t.Fatal("website guessed mutable destination")
	}
	snapshot.BotRoutes[1].NewChatID = -40
	snapshot.BotHistory = snapshot.BotHistory[:1]
	save()
	if _, err := worker.RecoverBotRoute(ctx, input); err == nil {
		t.Fatal("omitted history accepted")
	}
}

// existingDeliveryProbe provides read-only queue facts for panel retrieval tests.
type existingDeliveryProbe struct{ state string }

// InviteDelivery reports the configured queue result without enqueueing a notification.
func (probe *existingDeliveryProbe) InviteDelivery(context.Context, uint, uint) (runtime.InviteDeliveryStatus, error) {
	return runtime.InviteDeliveryStatus{State: probe.state}, nil
}

// TestBotExistingInvitation verifies long-term reads and unknown delivery non-replay.
func TestBotExistingInvitation(t *testing.T) {
	website, _, _, _, probe := entryFixture(t)
	entryMust(t, probe.db.Table("telegram_group_targets").Where("id = ?", 2).Update("recovery_task_id", 0).Error)
	routes := eventRoutes{route: membership.AccessRoute{TargetID: 2, BotID: 1, ChatID: -20, RouteVersion: 5, Generation: 7}}
	service, err := memberapp.NewAccess(memberstore.New(probe.db), routes, website.Identity, probe, probe)
	entryMust(t, err)
	website.Access = service
	delivery := &existingDeliveryProbe{state: "not_requested"}
	bot := &Bot{Website: website, Deliveries: delivery, DeliveryGuard: service}
	actor := shop.Actor{BotID: 1, UserID: 3, TelegramID: 42, Generation: "1:3"}
	ctx := runtime.WithExecutionFence(context.Background(), probe)
	before, err := bot.GetExisting(ctx, actor, 2)
	entryMust(t, err)
	if before.InviteURL != "" || probe.creates != 0 {
		t.Fatal("read created invitation")
	}
	_, err = service.RequestMembershipInvite(ctx, membership.AccessIdentity{AccountID: 3, TelegramID: 42, BotID: 1}, 2)
	entryMust(t, err)
	_, err = service.RunAccess(ctx, 1, 1)
	entryMust(t, err)
	for attempt := 0; attempt < 2; attempt++ {
		value, err := bot.GetExisting(ctx, actor, 2)
		entryMust(t, err)
		if value.State != "available" || value.InviteURL == "" || !value.Permanent || value.ExpiresAt != nil || value.InviteDelivery == nil {
			t.Fatal("long-term invitation not projected safely")
		}
	}
	var count int64
	entryMust(t, probe.db.Model(&memberdomain.AccessOperation{}).Count(&count).Error)
	if count != 1 || probe.creates != 1 {
		t.Fatal("read replayed platform work")
	}
	delivery.state = "unknown"
	uncertain, err := bot.GetExisting(ctx, actor, 2)
	entryMust(t, err)
	if uncertain.State != "unknown" || uncertain.InviteURL != "" {
		t.Fatal("unknown send was replayed")
	}
	actor.Generation = "old:3"
	if _, err := bot.GetExisting(ctx, actor, 2); err == nil {
		t.Fatal("old identity generation authorized")
	}
}

// TestRuntimeSubscriptionsAndRecoveryRemainIndependent verifies explicit subscriptions and bounded recovery.
func TestRuntimeSubscriptionsAndRecoveryRemainIndependent(t *testing.T) {
	bridge, sink := runtimeFixture()
	updates, err := bridge.AllowedUpdates(context.Background(), 2)
	if err != nil || len(updates) != 2 || updates[0] != "chat_join_request" || updates[1] != "chat_member" {
		t.Fatalf("missing membership subscriptions: %v %v", updates, err)
	}
	if err := bridge.Reconcile(context.Background(), 2); err != nil || sink.runs != 25 || sink.reconcileRuns != 25 || sink.scanLimit != 100 || sink.lifecycleLimit != 25 {
		t.Fatal("membership recovery was not dispatched with a bounded batch")
	}
}
