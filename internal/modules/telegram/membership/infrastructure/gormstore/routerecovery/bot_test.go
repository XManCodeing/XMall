package routerecovery

import (
	"context"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// recoveryCipher is synthetic and never opens a real credential or invitation.
type recoveryCipher struct{}

// Open returns the synthetic invite value unchanged.
func (recoveryCipher) Open(value string) (string, error) { return value, nil }

// Seal returns the synthetic ciphertext unchanged.
func (recoveryCipher) Seal(value string) (string, error) { return value, nil }

// recoveryProbe counts actions safely and checks SQL is available during each HTTP substitute.
type recoveryProbe struct {
	mu               sync.Mutex
	db               *gorm.DB
	revokes, queries int
	outcome          string
	memberStatus     string
}

// MembershipRecoveryPlatform returns a restricted fake for the supplied synthetic task.
func (probe *recoveryProbe) MembershipRecoveryPlatform(c.BotRouteRecoveryRequest) c.RecoveryAccessPlatform {
	return probe
}

// RevokeMembershipInvite simulates an acknowledged/unknown platform response outside SQL transactions.
func (probe *recoveryProbe) RevokeMembershipInvite(ctx context.Context, _ c.AccessRoute, _ string) c.AccessAPIResult {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.revokes++
	var count int64
	if err := probe.db.WithContext(ctx).Table("telegram_bot_instances").Count(&count).Error; err != nil {
		return c.AccessAPIResult{Outcome: c.AccessUnknown}
	}
	return c.AccessAPIResult{Outcome: probe.outcome}
}

// GetMembershipMember returns a genuine-query substitute, not inferred admission.
func (probe *recoveryProbe) GetMembershipMember(ctx context.Context, _ c.AccessJoinAction) c.AccessMemberResult {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.queries++
	var count int64
	if err := probe.db.WithContext(ctx).Table("telegram_bot_instances").Count(&count).Error; err != nil {
		return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessUnknown}}
	}
	return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessSucceeded}, Status: probe.memberStatus, IsMember: probe.memberStatus == "member"}
}

// botFixture adds two persistent fences and real synthetic generation join receipts.
func botFixture(t *testing.T, sameChat bool) (*BotRecovery, *gorm.DB, c.BotRouteRecoveryRequest, *recoveryProbe) {
	t.Helper()
	store, db, _ := fixture(t)
	for _, sql := range []string{
		"ALTER TABLE telegram_bot_instances ADD COLUMN version integer DEFAULT 4",
		"ALTER TABLE telegram_bot_instances ADD COLUMN recovery_task_id integer DEFAULT 3",
		"ALTER TABLE telegram_bot_instances ADD COLUMN recovery_version integer DEFAULT 4",
		"ALTER TABLE telegram_bot_instances ADD COLUMN recovery_generation integer DEFAULT 7",
		"CREATE TABLE telegram_bot_generation_exits (bot_id integer, generation integer, joined_at datetime, PRIMARY KEY (bot_id,generation))",
		"INSERT INTO telegram_bot_instances (id,generation,mode,enabled,state,version,recovery_task_id,recovery_version,recovery_generation) VALUES (9,0,'internal',0,'stopped',5,3,5,0)",
		"UPDATE telegram_group_targets SET bot_instance_id = 9",
		"UPDATE telegram_group_routes SET bot_instance_id = 9 WHERE retired_at IS NULL",
	} {
		must(t, db.Exec(sql).Error)
	}
	for generation := 1; generation <= 7; generation++ {
		must(t, db.Exec("INSERT INTO telegram_bot_generation_exits VALUES (1,?,?)", generation, time.Now().UTC()).Error)
	}
	input := c.BotRouteRecoveryRequest{TaskID: 3, TargetID: 2, OldBotID: 1, NewBotID: 9, OldChatID: -10, NewChatID: -20, OldRouteVersion: 4, NewRouteVersion: 5, OldVersion: 4, NewVersion: 5, OldGeneration: 7}
	if sameChat {
		input.NewChatID = -10
		must(t, db.Exec("UPDATE telegram_group_targets SET telegram_chat_id = -10").Error)
		must(t, db.Exec("UPDATE telegram_group_routes SET telegram_chat_id = -10 WHERE retired_at IS NULL").Error)
	}
	probe := &recoveryProbe{db: db, outcome: c.AccessSucceeded, memberStatus: "member"}
	return &BotRecovery{Store: store, Cipher: recoveryCipher{}, Platforms: probe}, db, input, probe
}

// seedBotLiabilities creates only immutable synthetic ledger facts and known access records.
func seedBotLiabilities(t *testing.T, db *gorm.DB) {
	t.Helper()
	paid := time.Now().UTC().Add(-24 * time.Hour)
	must(t, db.Create(&d.LedgerSource{ID: 1, TargetID: 2, AccountID: 3, OrderID: 4, OrderItemID: 5, Operation: "grant", BeneficiaryTelegramID: 42, PaidAt: paid, Kind: d.TermMonths, Months: 2}).Error)
	must(t, db.Create(&d.LedgerProjection{TargetID: 2, BeneficiaryTelegramID: 42, Version: 1}).Error)
	must(t, db.Create(&d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "active", LinkCipher: "synthetic-link", ExpiresAt: paid.Add(48 * time.Hour)}).Error)
	must(t, db.Create(&d.AccessMember{TargetID: 2, ChatID: -10, TelegramID: 42, BotID: 1, RouteVersion: 4, SourceID: 1, Status: "member", IsMember: true, ObservedAt: paid, ObservationSource: "query"}).Error)
}

// TestBotSameChatDisposition verifies actual revocation, member query and immutable sources.
func TestBotSameChatDisposition(t *testing.T) {
	worker, db, input, probe := botFixture(t, true)
	seedBotLiabilities(t, db)
	var original d.LedgerSource
	must(t, db.First(&original, 1).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := worker.RecoverBotRoute(ctx, input)
	must(t, err)
	if !result.Complete || result.CompletionReceipt == "" || probe.revokes != 1 || probe.queries != 1 {
		t.Fatalf("missing execution receipt: %+v %+v", result, probe)
	}
	var member d.AccessMember
	must(t, db.First(&member).Error)
	if member.BotID != 9 || member.RouteVersion != 5 || !member.IsMember {
		t.Fatalf("not transferred by query: %+v", member)
	}
	var saved d.LedgerSource
	must(t, db.First(&saved, 1).Error)
	if saved != original {
		t.Fatal("historical ledger mutated")
	}
	result, err = worker.RecoverBotRoute(ctx, input)
	must(t, err)
	if !result.Complete || probe.revokes != 1 || probe.queries != 1 {
		t.Fatal("restart repeated completed action")
	}
}

// TestBotUnknownRevokeNeverReplays proves expiry and successful queries cannot hide unknown HTTP.
func TestBotUnknownRevokeNeverReplays(t *testing.T) {
	worker, db, input, probe := botFixture(t, true)
	seedBotLiabilities(t, db)
	probe.outcome = c.AccessUnknown
	for attempt := 0; attempt < 2; attempt++ {
		result, err := worker.RecoverBotRoute(context.Background(), input)
		must(t, err)
		if result.Complete || result.CompletionReceipt != "" || result.Unknown == 0 {
			t.Fatal(result)
		}
	}
	if probe.revokes != 1 {
		t.Fatal("unknown mutation replayed")
	}
	must(t, db.Model(&d.AccessInvite{}).Where("id = ?", 1).Updates(map[string]any{"expiry_trusted": true, "expires_at": time.Now().Add(-time.Hour)}).Error)
	result, err := worker.RecoverBotRoute(context.Background(), input)
	must(t, err)
	if result.Complete || result.Unknown == 0 {
		t.Fatal("expiry erased unknown action")
	}
}

// TestBotFenceRequiresBothOwnersAndEveryExit rejects stopped/partial generations without HTTP.
func TestBotFenceRequiresBothOwnersAndEveryExit(t *testing.T) {
	for name, sql := range map[string]string{
		"old owner":        "UPDATE telegram_bot_instances SET recovery_task_id=4 WHERE id=1",
		"new owner":        "UPDATE telegram_bot_instances SET recovery_task_id=4 WHERE id=9",
		"missing exit":     "DELETE FROM telegram_bot_generation_exits WHERE generation=2",
		"unfinished exit":  "UPDATE telegram_bot_generation_exits SET joined_at=NULL WHERE generation=2",
		"generation drift": "UPDATE telegram_bot_instances SET generation=8 WHERE id=1",
		"target drift":     "UPDATE telegram_group_targets SET bot_instance_id=1",
	} {
		t.Run(name, func(t *testing.T) {
			worker, db, input, probe := botFixture(t, true)
			seedBotLiabilities(t, db)
			must(t, db.Exec(sql).Error)
			result, err := worker.RecoverBotRoute(context.Background(), input)
			if err == nil || result.Complete || probe.revokes != 0 || probe.queries != 0 {
				t.Fatalf("fence escaped: %+v %v", result, err)
			}
		})
	}
}

// TestBotDifferentChatPreservesKnownMembers leaves cleanup explicit, then resumes real absence.
func TestBotDifferentChatPreservesKnownMembers(t *testing.T) {
	worker, db, input, probe := botFixture(t, false)
	seedBotLiabilities(t, db)
	result, err := worker.RecoverBotRoute(context.Background(), input)
	must(t, err)
	if result.Complete || result.Remaining != 2 {
		t.Fatal(result)
	}
	probe.memberStatus = "left"
	result, err = worker.RecoverBotRoute(context.Background(), input)
	must(t, err)
	if result.Complete || result.CompletionReceipt != "" || result.Remaining != 1 {
		t.Fatal("absence cannot substitute destination admission", result)
	}
}

// TestBotConcurrentResumeClaimsMutationOnce verifies task-scoped CAS with one SQL connection.
func TestBotConcurrentResumeClaimsMutationOnce(t *testing.T) {
	worker, db, input, probe := botFixture(t, true)
	must(t, db.Create(&d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "active", LinkCipher: "synthetic-link", ExpiresAt: time.Now().Add(time.Hour)}).Error)
	var wait sync.WaitGroup
	for index := 0; index < 5; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := worker.RecoverBotRoute(context.Background(), input)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if probe.revokes != 1 {
		t.Fatalf("duplicate mutations: %d", probe.revokes)
	}
}

// TestBotSameChatAcknowledgedApproval uses an actual original success plus a new
// trusted observation; transferring the member must not demand absence in that chat.
func TestBotSameChatAcknowledgedApproval(t *testing.T) {
	for _, state := range []string{"succeeded", "unknown"} {
		t.Run(state, func(t *testing.T) {
			worker, db, input, probe := botFixture(t, true)
			seedBotLiabilities(t, db)
			before := time.Now().UTC().Add(-time.Minute)
			must(t, db.Create(&d.AccessOperation{ID: 11, BusinessKey: "approval", Kind: "approve", TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, TelegramID: 42, State: state, UpdatedAt: before}).Error)
			must(t, db.Create(&d.AccessResult{OperationID: 11, LeaseVersion: 1, Outcome: state, Action: "approve", ReceivedAt: before}).Error)
			must(t, db.Create(&d.AccessJoin{ID: 12, BotID: 1, ChatID: -10, TelegramID: 42, UpdateID: 12, State: "approved", UpdatedAt: before}).Error)
			result, err := worker.RecoverBotRoute(context.Background(), input)
			must(t, err)
			if result.Complete != (state == "succeeded") || probe.queries != 1 {
				t.Fatalf("approval attribution escaped: %+v", result)
			}
			var operation d.AccessOperation
			must(t, db.First(&operation, 11).Error)
			if operation.State != state {
				t.Fatal("observation rewrote original approval")
			}
			result, err = worker.RecoverBotRoute(context.Background(), input)
			must(t, err)
			if result.Complete != (state == "succeeded") || probe.queries != 1 || probe.revokes != 1 {
				t.Fatalf("duplicate continuation changed proof: %+v", result)
			}
		})
	}
}

// TestBotRevokeInterruptedBeforeCall retains claimed intent even without HTTP;
// expiry and duplicate continuation cannot prove whether the process sent it.
func TestBotRevokeInterruptedBeforeCall(t *testing.T) {
	worker, db, input, probe := botFixture(t, true)
	seedBotLiabilities(t, db)
	must(t, worker.Store.beginBotRecovery(context.Background(), input))
	action, err := worker.Store.claimAction(context.Background(), input, "revoke", "1")
	must(t, err)
	if action == nil {
		t.Fatal("missing durable intent")
	}
	must(t, db.Model(&d.AccessInvite{}).Where("id = ?", 1).Updates(map[string]any{"expiry_trusted": true, "expires_at": time.Now().Add(-time.Hour)}).Error)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := worker.RecoverBotRoute(context.Background(), input)
		must(t, err)
		if result.Complete || result.Unknown == 0 || probe.revokes != 0 {
			t.Fatalf("uncertain intent was retried or erased: %+v", result)
		}
	}
}

// TestBotMultipleTargetsAndImmutableBinding uses one parent task with independent
// receipts and rejects rebinding a completed target's runtime snapshot.
func TestBotMultipleTargetsAndImmutableBinding(t *testing.T) {
	worker, db, input, _ := botFixture(t, false)
	first, err := worker.RecoverBotRoute(context.Background(), input)
	must(t, err)
	if !first.Complete || first.CompletionReceipt == "" {
		t.Fatal(first)
	}
	must(t, db.Exec("INSERT INTO telegram_group_targets VALUES (6,3,'membership',1,9,-40,5,9)").Error)
	must(t, db.Exec("INSERT INTO telegram_group_routes VALUES (6,1,-30,4,'2026-01-01'),(6,9,-40,5,NULL)").Error)
	secondInput := input
	secondInput.TargetID, secondInput.OldChatID, secondInput.NewChatID = 6, -30, -40
	second, err := worker.RecoverBotRoute(context.Background(), secondInput)
	must(t, err)
	if !second.Complete || second.CompletionReceipt == first.CompletionReceipt {
		t.Fatal(second)
	}
	var count int64
	must(t, db.Model(&Run{}).Where("task_id = ?", input.TaskID).Count(&count).Error)
	if count != 2 {
		t.Fatalf("parent task lost target receipt: %d", count)
	}
	input.OldVersion++
	must(t, db.Exec("UPDATE telegram_bot_instances SET version = 5, recovery_version = 5 WHERE id = 1").Error)
	result, err := worker.RecoverBotRoute(context.Background(), input)
	if err == nil || result.Complete {
		t.Fatal("completed identity rebound")
	}
}
