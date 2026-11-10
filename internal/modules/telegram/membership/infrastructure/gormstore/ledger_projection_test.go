package gormstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	pd "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	adapter "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/botapi"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TestJoinAbuseEvidenceSingleConnection rejects uncertainty, expiry and rebinding.
func TestJoinAbuseEvidenceSingleConnection(t *testing.T) {
	for _, scenario := range []string{"confirmed", "unknown", "expired", "rebound", "owner"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry := accessActive(t, h, ctx)
			applicant := int64(88)
			if scenario == "owner" {
				applicant = h.identity.TelegramID
			}
			if scenario == "expired" {
				h.now = h.now.Add(31 * 24 * time.Hour)
			}
			input := c.AccessJoinRequest{BotID: 9, UpdateID: 42, ChatID: -1001, TelegramID: applicant, InviteLink: entry.Link, ReceivedAt: h.now}
			for count := 0; count < 2; count++ {
				if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unknown" {
				h.outcome = c.AccessUnknown
			}
			if _, err := h.service.RunAccess(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			if scenario == "rebound" {
				if err := h.db.Exec("UPDATE user_oauth_identities SET provider_user_id = ? WHERE user_id = ?", "99", 7).Error; err != nil {
					t.Fatal(err)
				}
			}
			rows, err := h.store.JoinAbuses(ctx, 9, 0, 25, h.now)
			if err != nil {
				t.Fatal(err)
			}
			valid := 0
			for _, row := range rows {
				if row.InviteID == 0 {
					continue
				}
				valid++
				if row.TelegramID != h.identity.TelegramID || row.AccountID != 7 || row.TelegramID == applicant {
					t.Fatalf("wrong owner: %+v", row)
				}
				if _, err := h.store.JoinAbuse(ctx, row.ID, h.now); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "confirmed" && (valid != 1 || h.declines != 1) {
				t.Fatalf("valid=%d declines=%d", valid, h.declines)
			}
			if scenario != "confirmed" && valid != 0 {
				t.Fatalf("false abuse report: %s", scenario)
			}
			var count int64
			if err := h.db.Model(&d.AccessJoin{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("duplicate join: %d %v", count, err)
			}
		})
	}
}

// TestMigrationBenefitLifecycle exercises mapped admission, renewal/refund and expiry on one connection.
func TestMigrationBenefitLifecycle(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(s.LifecycleModels()...); err != nil {
		t.Fatal(err)
	}
	old := harness.identity.TelegramID
	current := old + 2
	if err := harness.db.Exec("UPDATE user_oauth_identities SET provider_user_id=?", fmt.Sprint(current)).Error; err != nil {
		t.Fatal(err)
	}
	mapping := d.IdentityMigration{AccountID: 7, TargetID: 1, OldTelegramID: old, NewTelegramID: current, BotID: 9, ChatID: -1001, RouteVersion: 1, State: "awaiting_member", Version: 1, CreatedAt: harness.now}
	if err := harness.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	newActor := c.AccessIdentity{AccountID: 7, TelegramID: current, BotID: 9}
	before, err := harness.store.Eligibility(ctx, newActor, 1, harness.now)
	if err != nil || !before.Active {
		t.Fatalf("new admission: %+v %v", before, err)
	}
	oldEligible, err := harness.store.Eligibility(ctx, c.AccessIdentity{TelegramID: old}, 1, harness.now)
	if err != nil || !oldEligible.Active {
		t.Fatalf("old removed before admission: %+v %v", oldEligible, err)
	}
	for _, identity := range []int64{old, current} {
		_, err := harness.store.ObserveAccess(ctx, c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: identity, Status: "member", ObservedAt: harness.now.Add(time.Second), Source: "query"})
		if err != nil {
			t.Fatal(err)
		}
	}
	oldEligible, err = harness.store.Eligibility(ctx, c.AccessIdentity{TelegramID: old}, 1, harness.now.Add(time.Second))
	if err != nil || oldEligible.Active {
		t.Fatalf("old still entitled: %+v %v", oldEligible, err)
	}
	if _, err := harness.store.ScanLifecycle(ctx, 9, 100, harness.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var tasks []d.AccessOperation
	if err := harness.db.Where("kind = ?", "lifecycle").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TelegramID != old {
		t.Fatalf("cleanup targeted wrong identity: %+v", tasks)
	}
	renewal := d.LedgerSource{OrderItemID: 2, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: current, AccountID: 7, PaidAt: harness.now, Kind: d.TermMonths, Months: 1}
	if err := harness.db.Create(&renewal).Error; err != nil {
		t.Fatal(err)
	}
	after, err := harness.store.Eligibility(ctx, newActor, 1, harness.now.Add(time.Second))
	if err != nil || after.ExpiresAt == nil || !after.ExpiresAt.After(*before.ExpiresAt) {
		t.Fatalf("renewal not stacked: %+v %v", after, err)
	}
	if err := harness.store.RevokeSource(ctx, d.SourceKey{EntitlementScope: d.EntitlementScope{BeneficiaryTelegramID: old, TargetID: 1}, OrderItemID: 1}, harness.now); err != nil {
		t.Fatal(err)
	}
	after, err = harness.store.Eligibility(ctx, newActor, 1, harness.now.Add(time.Second))
	expected := harness.now.AddDate(0, 1, 0)
	if err != nil || after.ExpiresAt == nil || !after.ExpiresAt.Equal(expected) {
		t.Fatalf("refund changed term: %+v %v", after, err)
	}
	expired, err := harness.store.Eligibility(ctx, c.AccessIdentity{TelegramID: current}, 1, expected.Add(time.Second))
	if err != nil || expired.Active {
		t.Fatalf("expiry missed: %+v %v", expired, err)
	}
	if err := harness.db.Exec("UPDATE user_oauth_identities SET provider_user_id='123'").Error; err != nil {
		t.Fatal(err)
	}
	stable, err := harness.store.Eligibility(ctx, c.AccessIdentity{TelegramID: current}, 1, harness.now.Add(time.Second))
	if err != nil || !stable.Active {
		t.Fatalf("binding moved benefit: %+v %v", stable, err)
	}
	if _, err := harness.store.Eligibility(ctx, c.AccessIdentity{AccountID: 7, TelegramID: 123}, 1, harness.now); err == nil {
		t.Fatal("website binding inherited benefit")
	}
	var historical d.LedgerSource
	if err := harness.db.First(&historical, "order_item_id = ?", 1).Error; err != nil || historical.BeneficiaryTelegramID != old {
		t.Fatal("provenance changed", err)
	}
}

// TestInviteDeliveryReusesAndRevalidates proves website claims do not send or create again.
func TestInviteDeliveryReusesAndRevalidates(t *testing.T) {
	h, ctx := accessSetup(t)
	entry := accessActive(t, h, ctx)
	ref, err := h.service.PrepareInviteDelivery(ctx, h.identity, entry.InviteID)
	if err != nil {
		t.Fatal(err)
	}
	message := r.Message{InviteDelivery: ref, OwnerUserID: 7, ChatID: h.identity.TelegramID, Purpose: "panel", Buttons: [][]r.Button{{{URL: entry.Link}}}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := h.service.AuthorizeInviteDelivery(ctx, 9, message); err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.RequestMembershipInvite(ctx, h.identity, 1); err != nil {
			t.Fatal(err)
		}
	}
	if h.calls != 1 {
		t.Fatal("resend created another invite")
	}
	h.route.RouteVersion++
	if h.service.AuthorizeInviteDelivery(ctx, 9, message) == nil {
		t.Fatal("old route sent")
	}
	h.route.RouteVersion--
	h.route.Generation++
	if h.service.AuthorizeInviteDelivery(ctx, 9, message) == nil {
		t.Fatal("old generation sent")
	}
	h.route.Generation--
	if err := h.db.Model(&d.LedgerProjection{}).Where("target_id = ?", 1).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if h.service.AuthorizeInviteDelivery(ctx, 9, message) == nil {
		t.Fatal("old entitlement version sent")
	}
	h.deny = true
	if h.service.AuthorizeInviteDelivery(ctx, 9, message) == nil {
		t.Fatal("old binding sent")
	}
}

// TestAdminAccessRecovery verifies definite retries, immutable unknown effects, bounded expiry and audits.
func TestAdminAccessRecovery(t *testing.T) {
	for _, scenario := range []string{"failed", "unknown", "expired", "missing_expiry", "binding", "route"} {
		t.Run(scenario, func(t *testing.T) {
			harness, ctx := accessSetup(t)
			harness.outcome = c.AccessFailed
			if scenario != "failed" && scenario != "binding" && scenario != "route" {
				harness.outcome = c.AccessUnknown
			}
			if _, err := harness.service.RequestMembershipInvite(ctx, harness.identity, 1); err != nil {
				t.Fatal(err)
			}
			if scenario == "expired" || scenario == "missing_expiry" {
				setLegacyLimitedInvite(t, harness)
			}
			if _, err := harness.service.RunAccess(ctx, 9, 1); err != nil {
				t.Fatal(err)
			}
			rows, err := harness.service.ListAccessTasks(ctx, 9, 0, 20)
			if err != nil || len(rows) != 1 {
				t.Fatalf("list: %+v %v", rows, err)
			}
			row := rows[0]
			input := c.AccessRecoveryInput{ExpectedVersion: row.Version, ExpectedRouteVersion: row.RouteVersion, Action: "retry", Reason: "operator_verified"}
			if scenario == "expired" || scenario == "missing_expiry" {
				harness.now = harness.now.Add(25 * time.Hour)
				input.Action = "rebuild"
			}
			if scenario == "missing_expiry" {
				if err := harness.db.Model(&d.AccessInvite{}).Where("id = ?", row.InviteID).Update("expires_at", time.Time{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "binding" {
				if err := harness.db.Model(&d.AccessOperation{}).Where("id = ?", row.ID).Update("binding_version", 99).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "route" {
				harness.route.RouteVersion++
			}
			err = harness.service.RecoverAccess(ctx, 9, row.ID, 42, input)
			accepted := scenario == "failed" || scenario == "expired"
			if (err == nil) != accepted {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
			var operation d.AccessOperation
			if err := harness.db.First(&operation, row.ID).Error; err != nil {
				t.Fatal(err)
			}
			if operation.Version != row.Version+1 {
				t.Fatal("audit CAS did not advance")
			}
			if harness.outcome == c.AccessUnknown && operation.State != c.AccessUnknown {
				t.Fatal("unknown rewritten")
			}
			var receipt d.AccessResult
			if err := harness.db.Where("operation_id = ? AND actor_admin_id = ?", row.ID, 42).First(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			if (receipt.Outcome == "accepted") != accepted {
				t.Fatalf("wrong audit: %+v", receipt)
			}
			if harness.calls != 1 {
				t.Fatal("admin executed external effect")
			}
			page, err := harness.service.ListAccessTasks(ctx, 9, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired" && len(page) != 2 {
				t.Fatal("new entry not queued")
			}
			encoded, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"link_cipher", "link_hash", "lease_token", "https://"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("leak: %s", secret)
				}
			}
			if err := harness.service.RecoverAccess(ctx, 9, row.ID, 42, input); err == nil {
				t.Fatal("stale version accepted")
			}
		})
	}
}

// TestAdminRetryActuallyExecutes uses the original fenced worker after admin recovery, without a second grant.
func TestAdminRetryActuallyExecutes(t *testing.T) {
	harness, ctx := accessSetup(t)
	harness.outcome = c.AccessFailed
	if _, err := harness.service.RequestMembershipInvite(ctx, harness.identity, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := harness.service.ListAccessTasks(ctx, 9, 0, 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("tasks: %+v %v", rows, err)
	}
	input := c.AccessRecoveryInput{ExpectedVersion: rows[0].Version, ExpectedRouteVersion: rows[0].RouteVersion, Action: "retry", Reason: "operator_verified"}
	if err := harness.service.RecoverAccess(ctx, 9, rows[0].ID, 42, input); err != nil {
		t.Fatal(err)
	}
	harness.outcome = c.AccessSucceeded
	if _, err := harness.service.RunAccess(ctx, 9, 1); err != nil {
		t.Fatal(err)
	}
	entry, err := harness.service.RequestMembershipInvite(ctx, harness.identity, 1)
	if err != nil || entry.InviteState != "active" || entry.Link == "" || harness.calls != 2 {
		t.Fatalf("not executable: %+v %v", entry, err)
	}
}

// TestAdminUnknownJoinReview retains manual-only needs_review and never creates platform approval.
func TestAdminUnknownJoinReview(t *testing.T) {
	harness, ctx := accessSetup(t)
	request := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: 77, InviteLink: "https://t.me/+unknown", ReceivedAt: harness.now}
	if _, err := harness.store.EnqueueJoin(ctx, request, nil, "needs_review", harness.now); err != nil {
		t.Fatal(err)
	}
	rows, err := harness.service.ListAccessReviews(ctx, 9, 0, 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("reviews: %+v %v", rows, err)
	}
	input := c.AccessRecoveryInput{Action: "review_join", Reason: "operator_verified"}
	if err := harness.service.RecoverAccess(ctx, 9, rows[0].ID, 42, input); err != nil {
		t.Fatal(err)
	}
	rows, err = harness.service.ListAccessReviews(ctx, 9, 0, 20)
	if err != nil || rows[0].State != "needs_review" || rows[0].ActorAdminID != 42 || rows[0].ReviewedAt == nil {
		t.Fatalf("review: %+v %v", rows, err)
	}
	if harness.approvals != 0 {
		t.Fatal("unknown approved")
	}
}

// recoveryAPI exercises the real adapter while replacing only Telegram transport and runtime lookup.
// Embedded contracts panic on any unexpected side effect, including automatic removal.
type recoveryAPI struct {
	r.Store
	r.MembershipAccessAPI
	r.MembershipQueryAPI
	harness     *accessHarness
	mode        string
	tokenCipher string
}

// Get returns the current synthetic runtime instance for the requested bot.
func (api *recoveryAPI) Get(context.Context, uint) (*rd.Instance, error) {
	return &rd.Instance{ID: 9, TelegramID: 123, Generation: api.harness.route.Generation, Enabled: true, Mode: "internal", State: "running", TokenCipher: api.tokenCipher}, nil
}

// CreateChatInviteLink returns malformed structured responses or a transport timeout for input.
func (api *recoveryAPI) CreateChatInviteLink(_ context.Context, _ string, input r.CreateChatInviteLinkRequest) (r.InviteLink, error) {
	api.harness.calls++
	mode := api.mode
	if api.harness.afterCall != nil {
		api.harness.afterCall()
	}
	invite := r.InviteLink{InviteLink: "https://t.me/+adapter-secret-link", Creator: r.UserReference{ID: 123}, CreatesJoinRequest: true, ExpireDate: input.ExpireDate}
	switch mode {
	case "zero":
		invite.ExpireDate = 0
	case "later":
		invite.ExpireDate++
	case "direct":
		invite.CreatesJoinRequest = false
	case "timeout":
		return r.InviteLink{}, context.DeadlineExceeded
	}
	return invite, nil
}

// GetChatMember returns the exact requested identity and current simulated platform state.
func (api *recoveryAPI) GetChatMember(_ context.Context, _ string, _ int64, user int64) (r.ChatMember, error) {
	status := api.harness.memberStatus
	if status == "" {
		status = "member"
	}
	return r.ChatMember{User: r.UserReference{ID: user}, Status: status}, nil
}

// ApproveChatJoinRequest simulates an uncertain approval once without claiming membership.
func (api *recoveryAPI) ApproveChatJoinRequest(context.Context, string, int64, int64) error {
	api.harness.approvals++
	return context.DeadlineExceeded
}

// recoveryAdapter installs the actual botapi adapter using encrypted synthetic credentials.
func recoveryAdapter(t *testing.T, harness *accessHarness, mode string) *recoveryAPI {
	t.Helper()
	cipher, err := harness.Seal("123:adapter-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	api := &recoveryAPI{harness: harness, mode: mode, tokenCipher: cipher}
	harness.service.Platform = &adapter.Access{Store: api, Cipher: harness, API: api, Query: api, Routes: harness}
	return api
}

// TestInvalidInviteAdapterRecovery follows adapter -> worker -> database -> admin for malformed invites.
// Timeout remains rebuildable; malformed responses cannot trust local expiration even after postflight loss.
func TestInvalidInviteAdapterRecovery(t *testing.T) {
	for _, mode := range []string{"zero", "later", "direct", "timeout", "postflight"} {
		t.Run(mode, func(t *testing.T) {
			h, ctx := accessSetup(t)
			h.now = time.Now().UTC()
			api := recoveryAdapter(t, h, mode)
			if mode == "postflight" {
				api.mode = "zero"
				h.afterCall = func() { h.fence = false }
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			setLegacyLimitedInvite(t, h)
			if count, err := h.service.RunAccess(ctx, 9, 1); err != nil || count != 1 {
				t.Fatalf("worker=%d err=%v", count, err)
			}
			h.fence = true
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil || invite.State != "unknown" || invite.ExpiryTrusted != (mode == "timeout") || invite.LinkCipher != "" || invite.LinkHash != "" {
				t.Fatal("invalid expiry or secret persisted", err)
			}
			rows, err := h.service.ListAccessTasks(ctx, 9, 0, 20)
			if err != nil || len(rows) != 1 {
				t.Fatal("missing task", err)
			}
			row := rows[0]
			if mode != "timeout" && row.ErrorClass != "invalid_response" {
				t.Fatal("invalid response evidence lost")
			}
			h.now = invite.ExpiresAt.Add(6 * time.Minute)
			input := c.AccessRecoveryInput{ExpectedVersion: row.Version, ExpectedRouteVersion: row.RouteVersion, Action: "rebuild", Reason: "operator_verified"}
			err = h.service.RecoverAccess(ctx, 9, row.ID, 42, input)
			if (err == nil) != (mode == "timeout") {
				t.Fatalf("rebuild mode=%s err=%v", mode, err)
			}
			if mode != "timeout" && !errors.Is(err, c.ErrConflict) {
				t.Fatal(err)
			}
			var receipt d.AccessResult
			if err := h.db.Where("operation_id = ? AND actor_admin_id = ?", row.ID, 42).First(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			if (receipt.Outcome == "accepted") != (mode == "timeout") {
				t.Fatal("wrong audit outcome")
			}
			if err := h.service.RecoverAccess(ctx, 9, row.ID, 42, input); !errors.Is(err, c.ErrConflict) {
				t.Fatal("stale CAS accepted", err)
			}
			if mode != "timeout" {
				if count, err := h.service.RunAccess(ctx, 9, 10); err != nil || count != 0 {
					t.Fatal("invalid response replayed", err)
				}
				again, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
				if err != nil || again.InviteID != invite.ID {
					t.Fatal("untrusted slot released", err)
				}
			}
			if h.calls != 1 {
				t.Fatal("unexpected API replay")
			}
			rows, err = h.service.ListAccessTasks(ctx, 9, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"adapter-secret", "link_cipher", "link_hash", "lease_token"} {
				if strings.Contains(string(encoded), secret) || strings.Contains(logs.String(), secret) {
					t.Fatal("secret escaped")
				}
			}
		})
	}
}

// TestInvalidInviteLateEvidence exercises real adapter completions after lease expiry and replacement.
// Late negative evidence must commit even though completion CAS cannot change the current owner.
func TestInvalidInviteLateEvidence(t *testing.T) {
	for _, path := range []string{"expired", "scanned", "new_owner"} {
		t.Run(path, func(t *testing.T) {
			h, ctx := accessSetup(t)
			h.now = time.Now().UTC()
			api := recoveryAdapter(t, h, "zero")
			entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			setLegacyLimitedInvite(t, h)
			var original d.AccessOperation
			var replacement *d.AccessOperation
			h.afterCall = func() {
				original, replacement = replaceAccessEvidenceOwner(t, h, ctx, entry.InviteID, path)
			}
			if _, err := h.service.RunAccess(ctx, 9, 1); !errors.Is(err, c.ErrConflict) {
				t.Fatal("late completion CAS accepted", err)
			}
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil || invite.ExpiryTrusted {
				t.Fatal("negative evidence lost with CAS", err)
			}
			var receipt d.AccessResult
			if err := h.db.First(&receipt, "operation_id = ? AND lease_version = ?", original.ID, original.Version).Error; err != nil || receipt.ErrorClass != "invalid_response" || receipt.Outcome != "unknown" {
				t.Fatal("missing safe late receipt", err)
			}
			var current d.AccessOperation
			if err := h.db.First(&current, original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if replacement != nil {
				if current.State != "creating" || current.Version != replacement.Version || current.LeaseToken != replacement.LeaseToken {
					t.Fatal("old owner changed replacement")
				}
				h.afterCall, api.mode = nil, "valid"
				result := h.service.Platform.CreateMembershipInvite(ctx, c.AccessCreateInvite{Route: h.route, Name: "replacement", ExpiresAt: invite.ExpiresAt})
				if result.Outcome != c.AccessSucceeded {
					t.Fatal("replacement adapter failed")
				}
				cipher, err := h.Seal(result.Link)
				if err != nil {
					t.Fatal(err)
				}
				if err := h.store.FinishAccess(ctx, replacement, result.AccessAPIResult, cipher, strings.Repeat("d", 64), h.now); err != nil {
					t.Fatal(err)
				}
				invite, err = h.store.Invite(ctx, invite.ID)
				if err != nil || invite.ExpiryTrusted || invite.State != "active" {
					t.Fatal("new owner restored untrusted expiry", err)
				}
				if err := h.store.FinishAccess(ctx, &original, c.AccessAPIResult{Outcome: c.AccessUnknown, ErrorClass: "invalid_response"}, "", "", h.now); !errors.Is(err, c.ErrConflict) {
					t.Fatal("late replay overwrote success", err)
				}
			} else if path == "expired" {
				if current.State != "creating" || current.Version != original.Version {
					t.Fatal("old owner completed expired task")
				}
				if _, err := h.store.ClaimAccess(ctx, 9, "scan-after-evidence", h.now, time.Minute); !errors.Is(err, c.ErrNotFound) {
					t.Fatal(err)
				}
			}
			h.now = invite.ExpiresAt.Add(6 * time.Minute)
			if err := h.db.First(&current, original.ID).Error; err != nil {
				t.Fatal(err)
			}
			input := c.AccessRecoveryInput{ExpectedVersion: current.Version, ExpectedRouteVersion: current.RouteVersion, Action: "rebuild", Reason: "operator_verified"}
			if err := h.service.RecoverAccess(ctx, 9, current.ID, 42, input); err == nil {
				t.Fatal("admin rebuilt untrusted expiry")
			}
			var audit d.AccessResult
			if err := h.db.First(&audit, "operation_id = ? AND actor_admin_id = ?", current.ID, 42).Error; err != nil || audit.Outcome != "blocked" {
				t.Fatal("missing blocked audit", err)
			}
			if ledgerCount(t, h.db, &d.AccessInvite{}) != 1 {
				t.Fatal("second invite created")
			}
		})
	}
}

// replaceAccessEvidenceOwner expires the in-flight lease for inviteID and optionally claims a queued retry.
// Returns the old operation and replacement owner; path selects the simulated concurrency boundary.
func replaceAccessEvidenceOwner(t *testing.T, h *accessHarness, ctx context.Context, inviteID uint, path string) (d.AccessOperation, *d.AccessOperation) {
	t.Helper()
	var original d.AccessOperation
	if err := h.db.First(&original, "invite_id = ? AND kind = ?", inviteID, "create").Error; err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(2 * time.Minute)
	if path == "expired" {
		return original, nil
	}
	if _, err := h.store.ClaimAccess(ctx, 9, "scan-expired-lease", h.now, time.Minute); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
	if path != "new_owner" {
		return original, nil
	}
	// Model a previously queued retry; production must never automatically retry an unknown create.
	if err := h.db.Model(&d.AccessOperation{}).Where("id = ?", original.ID).Updates(map[string]any{"state": "retryable", "next_attempt_at": h.now}).Error; err != nil {
		t.Fatal(err)
	}
	replacement, err := h.store.ClaimAccess(ctx, 9, "replacement-owner-token", h.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return original, replacement
}

// TestInvalidInviteEvidenceScope rejects mismatched operation/invite provenance before recording evidence.
func TestInvalidInviteEvidenceScope(t *testing.T) {
	for _, field := range []string{"invite", "bot", "kind", "token", "missing"} {
		t.Run(field, func(t *testing.T) {
			h, ctx := accessSetup(t)
			entry, err := h.service.RequestMembershipInvite(ctx, h.identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			operation, err := h.store.ClaimAccess(ctx, 9, "real-owner-token", h.now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "invite":
				operation.InviteID++
			case "bot":
				operation.BotID++
			case "kind":
				operation.Kind = "revoke"
			case "token":
				operation.LeaseToken = "wrong-owner-token"
			case "missing":
				operation.ID++
			}
			if err := h.store.FinishAccess(ctx, operation, c.AccessAPIResult{Outcome: c.AccessUnknown, ErrorClass: "invalid_response"}, "", "", h.now); err == nil {
				t.Fatal("wrong scope accepted")
			}
			invite, err := h.store.Invite(ctx, entry.InviteID)
			if err != nil || !invite.ExpiryTrusted || ledgerCount(t, h.db, &d.AccessResult{}) != 0 {
				t.Fatal("wrong scope changed evidence", err)
			}
		})
	}
}

// TestAdminReconcileNeedsReview verifies manual platform intervention can be observed by requeuing a read.
// The original unknown approval, prior review reason and receipts survive; no automatic removal occurs.
func TestAdminReconcileNeedsReview(t *testing.T) {
	h, ctx := accessSetup(t)
	h.now = time.Now().UTC()
	recoveryAdapter(t, h, "valid")
	entry := accessActive(t, h, ctx)
	input := c.AccessJoinRequest{BotID: 9, UpdateID: 1, ChatID: -1001, TelegramID: h.identity.TelegramID, InviteLink: entry.Link, ReceivedAt: h.now}
	if err := h.service.ReceiveMembershipJoin(ctx, input); err != nil {
		t.Fatal(err)
	}
	if count, err := h.service.RunAccess(ctx, 9, 1); err != nil || count != 1 {
		t.Fatal("approve worker failed", err)
	}
	var parent d.AccessOperation
	if err := h.db.First(&parent, "kind = ?", "approve").Error; err != nil || parent.State != "unknown" {
		t.Fatal("approval not unknown", err)
	}
	var original d.AccessResult
	if err := h.db.First(&original, "operation_id = ?", parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&d.LedgerRevocation{OrderItemID: 1, TargetID: 1, BeneficiaryTelegramID: h.identity.TelegramID, RevokedAt: h.now}).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := h.service.RunAccessReconcile(ctx, 9, 1); err != nil || count != 1 {
		t.Fatal("initial read failed", err)
	}
	var read d.AccessOperation
	if err := h.db.First(&read, "kind = ?", "reconcile").Error; err != nil || read.State != "needs_review" || read.ReviewReason != "member_without_entitlement" || read.MemberStatus != "member" {
		t.Fatal("missing manual review", err)
	}
	previousObservation := *read.ObservedAt
	h.now = h.now.Add(time.Minute)
	h.memberStatus = "left" // Only the external administrator changes platform membership.
	recovery := c.AccessRecoveryInput{ExpectedVersion: parent.Version, ExpectedRouteVersion: parent.RouteVersion, Action: "reconcile", Reason: "operator_verified"}
	if err := h.service.RecoverAccess(ctx, 9, parent.ID, 42, recovery); err != nil {
		t.Fatal(err)
	}
	if err := h.service.RecoverAccess(ctx, 9, parent.ID, 42, recovery); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale recovery accepted", err)
	}
	if err := h.db.First(&read, read.ID).Error; err != nil || read.State != "pending" || read.ReviewReason != "member_without_entitlement" || !read.ObservedAt.Equal(previousObservation) {
		t.Fatal("review evidence cleared", err)
	}
	if count, err := h.service.RunAccessReconcile(ctx, 9, 1); err != nil || count != 1 {
		t.Fatal("manual reread failed", err)
	}
	if err := h.db.First(&read, read.ID).Error; err != nil || read.State != "succeeded" || read.MemberStatus != "left" || read.MemberIsMember || read.ReviewReason != "member_without_entitlement" {
		t.Fatal("reread did not preserve history", err)
	}
	var member d.AccessMember
	if err := h.db.First(&member).Error; err != nil || member.Status != "left" || member.IsMember {
		t.Fatal("left observation missing", err)
	}
	if err := h.db.First(&parent, parent.ID).Error; err != nil || parent.State != "unknown" || parent.Version != recovery.ExpectedVersion+1 {
		t.Fatal("original effect changed", err)
	}
	var saved d.AccessResult
	if err := h.db.First(&saved, "operation_id = ? AND lease_version = ?", original.OperationID, original.LeaseVersion).Error; err != nil || saved != original {
		t.Fatal("original receipt changed", err)
	}
	var receipt d.AccessResult
	if err := h.db.Where("operation_id = ? AND actor_admin_id = ?", parent.ID, 42).First(&receipt).Error; err != nil || receipt.Action != "reconcile" || receipt.Outcome != "accepted" {
		t.Fatal("missing CAS audit", err)
	}
	if h.calls != 1 || h.approvals != 1 || h.declines != 0 {
		t.Fatal("read caused a platform mutation")
	}
}

// addPermanentSnapshot 为 db 增加第二个实际会员项，返回稳定来源键；不读取或修改商品配置。
func addPermanentSnapshot(t *testing.T, db *gorm.DB) d.SourceKey {
	t.Helper()
	var snapshot d.OrderItemSnapshot
	if err := db.First(&snapshot, "order_item_id = 11").Error; err != nil {
		t.Fatal(err)
	}
	snapshot.OrderItemID = 12
	snapshot.Kind = d.TermPermanent
	snapshot.Months = 0
	snapshot.DurationSeconds = 0
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	return d.SourceKey{EntitlementScope: d.EntitlementScope{BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, TargetID: 1}, OrderItemID: 12}
}

// TestLedgerPermanentAndRevocation 验证永久覆盖旧限时到期，撤销后按其余来源自然流逝重算。
func TestLedgerPermanentAndRevocation(t *testing.T) {
	store, db, input := ledgerSetup(t)
	key := addPermanentSnapshot(t, db)
	ctx := context.Background()
	if err := store.GrantPaid(ctx, input); err != nil {
		t.Fatal(err)
	}
	late := input.PaidAt.Add(120 * 24 * time.Hour)
	projection, err := store.Recalculate(ctx, key.EntitlementScope, late)
	if err != nil || !projection.Active || projection.Kind != d.TermPermanent || projection.ExpiresAt != nil {
		t.Fatalf("permanent lost: %+v %v", projection, err)
	}
	if err := store.RevokeSource(ctx, key, late); err != nil {
		t.Fatal(err)
	}
	projection, err = store.Recalculate(ctx, key.EntitlementScope, late)
	if err != nil || projection.Active {
		t.Fatalf("expired source revived: %+v %v", projection, err)
	}
	projection, err = store.Recalculate(ctx, key.EntitlementScope, input.PaidAt.Add(time.Hour))
	if err != nil || !projection.Active || projection.Kind != d.TermMonths {
		t.Fatalf("limited source lost: %+v %v", projection, err)
	}
	if err := store.GrantPaid(ctx, input); err != nil {
		t.Fatal(err)
	}
	projection, err = store.Recalculate(ctx, key.EntitlementScope, late)
	if err != nil || projection.Active || ledgerCount(t, db, &d.LedgerRevocation{}) != 1 {
		t.Fatalf("replay resurrected: %+v %v", projection, err)
	}
}

// TestLedgerTombstoneBeforeGrant 验证先撤后授不会复活，且错误受益人的墓碑不能绕过。
func TestLedgerTombstoneBeforeGrant(t *testing.T) {
	store, db, input := ledgerSetup(t)
	key := d.SourceKey{EntitlementScope: d.EntitlementScope{BeneficiaryTelegramID: 9007199254740993, TargetID: 1}, OrderItemID: 11}
	if err := store.RevokeSource(context.Background(), key, input.PaidAt); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	projection, err := store.Recalculate(context.Background(), key.EntitlementScope, input.PaidAt.Add(time.Hour))
	if err != nil || projection.Active {
		t.Fatalf("late grant revived: %+v %v", projection, err)
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 || ledgerCount(t, db, &d.LedgerRevocation{}) != 1 {
		t.Fatal("facts lost")
	}
	key.BeneficiaryTelegramID++
	if err := store.RevokeSource(context.Background(), key, input.PaidAt); !errors.Is(err, d.ErrEntitlementScope) {
		t.Fatalf("scope conflict: %v", err)
	}
}

// TestLedgerPermanentOverlapIsolation 验证全覆盖/部分覆盖明确区分，其他用户及群不会串账。
func TestLedgerPermanentOverlapIsolation(t *testing.T) {
	store, db, input := ledgerSetup(t)
	key := addPermanentSnapshot(t, db)
	if err := store.GrantPaid(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	scenarios := []struct {
		beneficiary int64
		targets     []d.SnapshotTarget
		expected    error
	}{
		{key.BeneficiaryTelegramID, []d.SnapshotTarget{{TargetID: 1}}, c.ErrPermanentFullyCovered},
		{key.BeneficiaryTelegramID, []d.SnapshotTarget{{TargetID: 2}, {TargetID: 1}}, c.ErrPermanentPartiallyCovered},
		{key.BeneficiaryTelegramID, []d.SnapshotTarget{{TargetID: 2}}, nil},
		{key.BeneficiaryTelegramID + 1, []d.SnapshotTarget{{TargetID: 1}}, nil},
	}
	for _, scenario := range scenarios {
		err := store.CheckPermanentOverlap(context.Background(), 7, scenario.beneficiary, scenario.targets)
		if !errors.Is(err, scenario.expected) {
			t.Fatalf("overlap = %v want %v", err, scenario.expected)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bound, err := s.InTransaction(tx)
		if err != nil {
			return err
		}
		if _, err := s.CheckoutWithLedgerInTransaction(tx); err != nil {
			return err
		}
		return bound.CheckPermanentOverlap(ctx, 7, key.BeneficiaryTelegramID+1, []d.SnapshotTarget{{TargetID: 1}})
	})
	if err != nil {
		t.Fatalf("single connection transaction: %v", err)
	}
}

// TestLedgerOutboxFailureRollsBack 验证任务持久化失败时来源/投影不能先成功。
func TestLedgerOutboxFailureRollsBack(t *testing.T) {
	store, db, input := ledgerSetup(t)
	if err := db.Migrator().DropTable(&d.LedgerOutbox{}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err == nil {
		t.Fatal("missing outbox accepted")
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 0 || ledgerCount(t, db, &d.LedgerProjection{}) != 0 {
		t.Fatal("partial state leaked")
	}
}

// TestLedgerMultipleItemsAtomic 验证同批后一个项非法时前一个项也不得成功。
func TestLedgerMultipleItemsAtomic(t *testing.T) {
	store, db, input := ledgerSetup(t)
	addPermanentSnapshot(t, db)
	if err := db.Model(&od.OrderItem{}).Where("id = 12").Update("quantity", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); err == nil {
		t.Fatal("invalid item accepted")
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 0 {
		t.Fatal("partial items committed")
	}
}

// TestLedgerOutOfOrderRenewal 验证旧付款事件迟到仍按真实 PaidAt 排序，并保留续费余期。
func TestLedgerOutOfOrderRenewal(t *testing.T) {
	store, db, first := ledgerSetup(t)
	later := first.PaidAt.Add(20 * 24 * time.Hour)
	amount := money.FromDecimal(decimal.NewFromInt(10))
	order := od.Order{ID: 20, OrderNo: "renewal", UserID: 7, Status: constants.OrderStatusPaid, PaidAt: &later, Currency: "CNY", TotalAmount: amount, OnlinePaidAmount: amount}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	item := od.OrderItem{ID: 21, OrderID: 20, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "renewal"}}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot d.OrderItemSnapshot
	if err := db.First(&snapshot, "order_item_id = 11").Error; err != nil {
		t.Fatal(err)
	}
	snapshot.OrderItemID = 21
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	payment := pd.Payment{ID: 2, OrderID: 20, Status: constants.PaymentStatusSuccess, PaidAt: &later, Currency: "CNY", Amount: amount}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	for _, input := range []c.PaidGrant{{OrderID: 20, PaymentID: 2, PaidAt: later}, first, {OrderID: 20, PaymentID: 2, PaidAt: later}} {
		if err := store.GrantPaid(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := store.Recalculate(context.Background(), d.EntitlementScope{BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, TargetID: 1}, later)
	if err != nil || projection.ExpiresAt == nil || !projection.ExpiresAt.Equal(first.PaidAt.Add(120*24*time.Hour)) {
		t.Fatalf("out of order expiry: %+v %v", projection, err)
	}
}

// TestLedgerMultiConnectionFirstProjection 验证 SQLite 多连接下首次投影竞争不丢来源。
func TestLedgerMultiConnectionFirstProjection(t *testing.T) {
	store, db, input := ledgerSetup(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	results := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- store.GrantPaid(ctx, input) }()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 || ledgerCount(t, db, &d.LedgerOutbox{}) != 1 {
		t.Fatal("concurrent duplicate")
	}
}

// TestLedgerSnapshotTargetMutation 验证修改已有快照全部目标也无法偷偷追加新的权益。
func TestLedgerSnapshotTargetMutation(t *testing.T) {
	store, db, input := ledgerSetup(t)
	if err := store.GrantPaid(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var snapshot d.OrderItemSnapshot
	if err := db.First(&snapshot, "order_item_id = 11").Error; err != nil {
		t.Fatal(err)
	}
	snapshot.Targets[0].TargetID = 2
	if err := db.Save(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.GrantPaid(context.Background(), input); !errors.Is(err, d.ErrSourceConflict) {
		t.Fatalf("history changed: %v", err)
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 || ledgerCount(t, db, &d.LedgerProjection{}) != 1 {
		t.Fatal("new target leaked")
	}
}
