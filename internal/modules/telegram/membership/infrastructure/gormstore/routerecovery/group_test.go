package routerecovery

import (
	"context"
	"errors"
	"github.com/XManCodeing/XMall/internal/constants"
	external "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	users "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	base "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"gorm.io/gorm"
	"testing"
	"time"
)

// groupProbe is an isolated platform/authority with no Telegram connection.
type groupProbe struct {
	db                               *gorm.DB
	deny                             bool
	newStatus, oldStatus, banOutcome string
	creates, approves, bans, unbans  int
	afterCreate                      func()
}

// AuthorizeMembershipGroupRecovery rejects withdrawn synthetic authority.
func (p *groupProbe) AuthorizeMembershipGroupRecovery(context.Context, uint, uint, c.AccessRoute) error {
	if p.deny {
		return c.ErrConflict
	}
	return nil
}

// AuthorizeMembership validates the synthetic bound website account.
func (p *groupProbe) AuthorizeMembership(_ context.Context, id c.AccessIdentity, _ uint) error {
	if id.AccountID != 3 || id.TelegramID != 42 || id.BotID != 1 {
		return c.ErrUntrustedIdentity
	}
	return nil
}

// MembershipGroupPlatform returns fake API capabilities only within the test.
func (p *groupProbe) MembershipGroupPlatform(uint, uint, c.GroupRecoveryAuthorization) (c.AccessPlatform, c.LifecyclePlatform) {
	return p, p
}

// CreateMembershipInvite returns an approval-only invitation substitute.
func (p *groupProbe) CreateMembershipInvite(context.Context, c.AccessCreateInvite) c.AccessInviteResult {
	p.creates++
	if p.afterCreate != nil {
		p.afterCreate()
	}
	return c.AccessInviteResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessSucceeded}, Link: "https://t.me/+abcdefghijk"}
}

// ApproveMembershipJoin acknowledges approval but intentionally does not join the member.
func (p *groupProbe) ApproveMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	p.approves++
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// DeclineMembershipJoin returns definite synthetic success.
func (p *groupProbe) DeclineMembershipJoin(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// RevokeMembershipInvite returns definite synthetic success.
func (p *groupProbe) RevokeMembershipInvite(context.Context, c.AccessRoute, string) c.AccessAPIResult {
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// GetMembershipMember tests that the single SQL connection is free during HTTP.
func (p *groupProbe) GetMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessMemberResult {
	var count int64
	if err := p.db.WithContext(ctx).Model(&d.LedgerSource{}).Count(&count).Error; err != nil {
		return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessUnknown}}
	}
	status := p.newStatus
	if input.Route.ChatID == -10 {
		status = p.oldStatus
	}
	return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessSucceeded}, Status: status, IsMember: status == "member" || status == "administrator"}
}

// BanMembershipMember records order and simulates true or uncertain platform effect.
func (p *groupProbe) BanMembershipMember(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	p.bans++
	if p.banOutcome == c.AccessSucceeded {
		p.oldStatus = "kicked"
	}
	return c.AccessAPIResult{Outcome: p.banOutcome}
}

// UnbanMembershipMember models only-if-banned cleanup.
func (p *groupProbe) UnbanMembershipMember(context.Context, c.AccessJoinAction) c.AccessAPIResult {
	p.unbans++
	p.oldStatus = "left"
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// groupFixture creates real membership tables and bindings with no contact table.
func groupFixture(t *testing.T) (*GroupRecovery, *gorm.DB, Request, *groupProbe) {
	t.Helper()
	store, db, input := fixture(t)
	input.ActorID = 7
	for _, sql := range []string{
		"ALTER TABLE telegram_bot_instances ADD COLUMN recovery_task_id integer DEFAULT 0",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_generation integer DEFAULT 7",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_route_version integer DEFAULT 5",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_sequence integer DEFAULT 1",
		"ALTER TABLE telegram_group_targets ADD COLUMN observation_seq integer DEFAULT 1",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_state text DEFAULT 'ready'",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_error_class text DEFAULT ''",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_is_member boolean DEFAULT 1",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_can_invite_users boolean DEFAULT 1",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_can_restrict_members boolean DEFAULT 1",
		"ALTER TABLE telegram_group_targets ADD COLUMN obs_observed_at datetime",
		"CREATE TABLE telegram_recovery_tasks (id integer PRIMARY KEY,kind text,confirmed_actor_id integer,confirmed_at datetime,state text,version integer)",
		"INSERT INTO telegram_recovery_tasks VALUES (3,'group_replace',7,'2026-01-01','recovering',1)",
		"CREATE TABLE telegram_recovery_scope_slots (key text PRIMARY KEY,task_id integer)",
		"INSERT INTO telegram_recovery_scope_slots VALUES ('target:2',3),('bot:1',3),('chat:-10',3),('chat:-20',3)",
		"CREATE TABLE telegram_group_recovery_target_fences (task_id integer,target_id integer,old_bot_id integer,new_bot_id integer,old_chat_id integer,new_chat_id integer,initial_route_version integer,released_version integer,replaced_version integer)",
		"INSERT INTO telegram_group_recovery_target_fences VALUES (3,2,1,1,-10,-20,4,0,9)",
	} {
		must(t, db.Exec(sql).Error)
	}
	must(t, db.Table("telegram_group_targets").Where("id = 2").Update("obs_observed_at", time.Now().UTC()).Error)
	must(t, db.AutoMigrate(&users.User{}, &external.Identity{}, &d.AccessSlot{}))
	must(t, db.Create(&users.User{ID: 3, Email: "recovery@example.invalid", Status: constants.UserStatusActive}).Error)
	must(t, db.Create(&external.Identity{UserID: 3, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "42"}).Error)
	paid := time.Now().UTC().Add(-time.Hour)
	must(t, db.Create(&d.LedgerSource{ID: 1, TargetID: 2, AccountID: 3, OrderID: 4, OrderItemID: 5, Operation: "grant", BeneficiaryTelegramID: 42, PaidAt: paid, Kind: d.TermMonths, Months: 2}).Error)
	must(t, db.Create(&d.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 1}).Error)
	must(t, db.Create(&d.AccessMember{TargetID: 2, ChatID: -10, TelegramID: 42, BotID: 1, RouteVersion: 4, SourceID: 1, Status: "member", IsMember: true, ObservedAt: paid, ObservationSource: "query"}).Error)
	probe := &groupProbe{db: db, newStatus: "left", oldStatus: "member", banOutcome: c.AccessSucceeded}
	worker := &GroupRecovery{Store: store, Cipher: recoveryCipher{}, Platforms: probe, Authority: probe, Actor: func(context.Context, uint) (uint, error) { return 7, nil }, Authorizer: probe}
	return worker, db, input, probe
}

// TestGroupAdmissionThenCleanup uses original invitation and lifecycle state machines.
func TestGroupAdmissionThenCleanup(t *testing.T) {
	worker, db, input, probe := groupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var original d.LedgerSource
	must(t, db.First(&original, 1).Error)
	result, err := worker.RecoverRoute(ctx, input)
	must(t, err)
	if result.Verified || probe.creates != 1 || probe.bans != 0 {
		t.Fatalf("premature cleanup %+v creates=%d bans=%d", result, probe.creates, probe.bans)
	}
	entry, err := worker.Entry(ctx, input, c.AccessIdentity{AccountID: 3, TelegramID: 42, BotID: 1})
	must(t, err)
	if entry.Link == "" {
		t.Fatal("website recovery invite missing")
	}
	must(t, worker.ReceiveJoin(ctx, input, c.AccessJoinRequest{BotID: 1, ChatID: -20, TelegramID: 42, UpdateID: 9, InviteLink: entry.Link, ReceivedAt: time.Now().UTC()}))
	result, err = worker.RecoverRoute(ctx, input)
	must(t, err)
	if probe.approves != 1 || probe.bans != 0 || result.Verified {
		t.Fatal("approval incorrectly treated as membership")
	}
	probe.newStatus = "member"
	for range 5 {
		result, err = worker.RecoverRoute(ctx, input)
		must(t, err)
	}
	if !result.Verified || probe.bans != 1 || probe.unbans != 1 {
		var evidence []Evidence
		must(t, db.Where("state = ?", "unknown").Find(&evidence).Error)
		t.Logf("unknown: %+v", evidence)
		t.Fatalf("cleanup not complete: %+v bans=%d unbans=%d", result, probe.bans, probe.unbans)
	}
	result, err = worker.RecoverRoute(ctx, input)
	must(t, err)
	if !result.Verified || probe.creates != 1 || probe.bans != 1 {
		t.Fatal("resume repeated mutation")
	}
	var saved d.LedgerSource
	must(t, db.First(&saved, 1).Error)
	if saved != original {
		t.Fatal("ledger/paid time mutated")
	}
}

// TestGroupAdministratorAndUnknownBanRemainReview never demotes admins or replays uncertainty.
func TestGroupAdministratorAndUnknownBanRemainReview(t *testing.T) {
	for _, mode := range []string{"administrator", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			worker, _, input, probe := groupFixture(t)
			probe.newStatus = "member"
			if mode == "administrator" {
				probe.oldStatus = "administrator"
			} else {
				probe.banOutcome = c.AccessUnknown
			}
			for range 5 {
				result, err := worker.RecoverRoute(context.Background(), input)
				must(t, err)
				if result.Verified {
					t.Fatal("review was completed")
				}
			}
			if mode == "administrator" && probe.bans != 0 {
				t.Fatal("administrator kicked")
			}
			if mode == "unknown" && probe.bans != 1 {
				t.Fatal("unknown ban repeated")
			}
			if probe.unbans != 0 {
				t.Fatal("unattributed ban cleared")
			}
		})
	}
}

// TestCleanupLostReceiptDoesNotClaimBan simulates a platform-side ban with its
// response lost. A later kicked query cannot authorize unban or close the attempt.
func TestCleanupLostReceiptDoesNotClaimBan(t *testing.T) {
	worker, db, input, probe := groupFixture(t)
	probe.newStatus, probe.banOutcome = "member", c.AccessUnknown
	for range 2 {
		_, err := worker.RecoverRoute(context.Background(), input)
		must(t, err)
	}
	if probe.bans != 1 {
		t.Fatalf("expected original ban, got %d", probe.bans)
	}
	probe.oldStatus = "kicked" // Actual platform effect, not an application receipt.
	for range 3 {
		result, err := worker.RecoverRoute(context.Background(), input)
		must(t, err)
		if result.Verified || result.Unknown == 0 || probe.bans != 1 || probe.unbans != 0 {
			t.Fatalf("query claimed unknown ban: %+v", result)
		}
	}
	var operation d.AccessOperation
	must(t, db.Where("kind = ?", "lifecycle").First(&operation).Error)
	if operation.State != "needs_review" || operation.LifecyclePhase != "unknown_ban" {
		t.Fatalf("unknown ban review lost: %+v", operation)
	}
	var receipts int64
	must(t, db.Model(&d.AccessResult{}).Where("operation_id = ? AND action = ? AND outcome = ?", operation.ID, "ban", c.AccessUnknown).Count(&receipts).Error)
	if receipts != 1 {
		t.Fatal("original lost ban receipt overwritten")
	}
}

// TestCleanupDefiniteFailureRemainsVisible retains failed cleanup without an
// automatic mutation retry or any entitlement/identity change on continuation.
func TestCleanupDefiniteFailureRemainsVisible(t *testing.T) {
	worker, db, input, probe := groupFixture(t)
	probe.newStatus, probe.banOutcome = "member", c.AccessFailed
	var original d.LedgerSource
	must(t, db.First(&original, 1).Error)
	for range 5 {
		result, err := worker.RecoverRoute(context.Background(), input)
		must(t, err)
		if result.Verified {
			t.Fatal("failed old cleanup passed")
		}
	}
	if probe.bans != 1 || probe.unbans != 0 {
		t.Fatal("definite failed cleanup blindly repeated")
	}
	var saved d.LedgerSource
	must(t, db.First(&saved, 1).Error)
	if saved != original {
		t.Fatal("cleanup failure modified entitlement")
	}
}

// TestOrdinaryWorkersCannotClaimFrozenGroup keeps recovery queues isolated.
func TestOrdinaryWorkersCannotClaimFrozenGroup(t *testing.T) {
	_, db, _, _ := groupFixture(t)
	store := base.New(db)
	now := time.Now().UTC()
	for _, kind := range []string{"create", "lifecycle"} {
		must(t, db.Create(&d.AccessOperation{BusinessKey: "frozen:" + kind, Kind: kind, TargetID: 2, BotID: 1, State: "pending", Version: 1, NextAttemptAt: now}).Error)
	}
	if _, err := store.ClaimAccess(context.Background(), 1, "abcdefghijklmnop", now, time.Minute); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("ordinary access claimed: %v", err)
	}
	if _, err := store.ClaimLifecycle(context.Background(), 1, "abcdefghijklmnop", now, time.Minute); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("ordinary lifecycle claimed: %v", err)
	}
}

// TestGroupAuthorityLostAfterCreateKeepsUnknown proves post-HTTP revoke cannot replay.
func TestGroupAuthorityLostAfterCreateKeepsUnknown(t *testing.T) {
	worker, db, input, probe := groupFixture(t)
	probe.afterCreate = func() { probe.deny = true }
	if _, err := worker.RecoverRoute(context.Background(), input); err == nil {
		t.Fatal("mid-call revocation ignored")
	}
	probe.deny = false
	probe.afterCreate = nil
	result, err := worker.RecoverRoute(context.Background(), input)
	must(t, err)
	if probe.creates != 1 || result.Verified || result.Unknown == 0 {
		t.Fatalf("unknown invite repeated: %+v %d", result, probe.creates)
	}
	var invite d.AccessInvite
	must(t, db.First(&invite).Error)
	if invite.State != "unknown" {
		t.Fatal(invite.State)
	}
}

// TestGroupPersistentGuards rejects scope loss, stale capabilities and undrained ordinary work.
func TestGroupPersistentGuards(t *testing.T) {
	for name, statement := range map[string]string{
		"scope":      "DELETE FROM telegram_recovery_scope_slots WHERE key='chat:-10'",
		"parent":     "UPDATE telegram_recovery_tasks SET state='completed'",
		"permission": "UPDATE telegram_group_targets SET obs_can_restrict_members=0",
		"receipt":    "UPDATE telegram_group_recovery_target_fences SET released_version=10",
		"runtime":    "UPDATE telegram_bot_instances SET recovery_task_id=99",
	} {
		t.Run(name, func(t *testing.T) {
			worker, db, input, probe := groupFixture(t)
			must(t, db.Exec(statement).Error)
			if _, err := worker.RecoverRoute(context.Background(), input); err == nil {
				t.Fatal("lost fence accepted")
			}
			if probe.creates != 0 || probe.bans != 0 {
				t.Fatal("side effect outside fence")
			}
		})
	}
	t.Run("drain", func(t *testing.T) {
		worker, db, input, probe := groupFixture(t)
		must(t, db.Create(&d.AccessOperation{BusinessKey: "ordinary-inflight", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "creating", Kind: "approve", LeaseUntil: time.Now().Add(time.Minute)}).Error)
		if _, err := worker.RecoverRoute(context.Background(), input); err == nil {
			t.Fatal("undrained old operation accepted")
		}
		if probe.creates != 0 {
			t.Fatal("created before drain")
		}
	})
}

// TestGroupRevokedAuthorityAndCrashRetainFence proves authorization and interrupted HTTP fail closed.
func TestGroupRevokedAuthorityAndCrashRetainFence(t *testing.T) {
	worker, db, input, probe := groupFixture(t)
	probe.deny = true
	if _, err := worker.RecoverRoute(context.Background(), input); err == nil {
		t.Fatal("withdrawn authority allowed")
	}
	if probe.creates != 0 || probe.bans != 0 {
		t.Fatal("withdrawn authority performed API")
	}
	probe.deny = false
	must(t, db.Create(&Action{TaskID: 3, TargetID: 2, Kind: "group_revoke", Reference: "99", State: "creating", Version: 1}).Error)
	result, err := worker.RecoverRoute(context.Background(), input)
	must(t, err)
	if result.Verified || result.Unknown == 0 {
		t.Fatal("crash intent disappeared")
	}
}
