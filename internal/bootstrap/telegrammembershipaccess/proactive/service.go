package proactive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/constants"
	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"log/slog"
	"time"
)

// Access exposes existing invitation preparation and its separate background guard.
// Request only persists work; external create remains owned by the access worker.
type Access interface {
	m.AccessReader
	PrepareInviteDelivery(context.Context, m.AccessIdentity, uint) (*r.InviteDeliveryRef, error)
	AuthorizeProactiveInviteDelivery(context.Context, uint, r.Message) error
}

// Service orchestrates local-only per-source notifications without panel/session state.
// Now is injectable; Reconcile must run independently of network-heavy workers.
type Service struct {
	Store  m.ProactiveRepository
	Access Access
	Queue  r.Store
	Routes m.AccessRoutes
	Cipher m.AccessCipher
	Now    func() time.Time
}

// now returns the injectable trusted UTC clock.
func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

// Reconcile derives and consumes bounded local work, persisting transitions by lease-owner CAS.
// Errors postpone only their own responsibility and are logged without sensitive details.
func (service *Service) Reconcile(ctx context.Context, bot uint) error {
	if service.Store == nil || service.Access == nil || service.Queue == nil || service.Routes == nil || service.Cipher == nil {
		return m.ErrCapability
	}
	// A broken historical source must not stall already-derived responsibilities
	// for unrelated groups. Preserve the scan error while draining existing work.
	_, scanErr := service.Store.ScanProactive(ctx, bot, 25, service.now())
	failures := []error{scanErr}
	for index := 0; index < 25; index++ {
		token := make([]byte, 24)
		if _, err := rand.Read(token); err != nil {
			return errors.Join(append(failures, err)...)
		}
		row, err := service.Store.ClaimProactive(ctx, bot, hex.EncodeToString(token), service.now(), 30*time.Second)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if row == nil {
			break
		}
		err = service.process(ctx, row)
		row.AvailableAt = service.now().Add(2 * time.Second)
		if err != nil {
			row.AvailableAt = service.now().Add(5 * time.Second)
			if errors.Is(err, m.ErrUntrustedIdentity) || errors.Is(err, m.ErrConflict) {
				row.State = "superseded"
			}
			slog.WarnContext(ctx, "membership_proactive_reconcile_failed", "task_id", row.ID)
		}
		failures = append(failures, err, service.Store.SaveProactive(ctx, row, service.now()))
	}
	return errors.Join(failures...)
}

// process keeps entitlement, invitation and actual delivery independent; unknown never requests replay.
func (service *Service) process(ctx context.Context, row *d.ProactiveDelivery) error {
	if row.State == "queued" {
		return service.receipt(ctx, row)
	}
	previous := row.Generation
	row.Generation = 0
	if err := service.validate(ctx, row); err != nil {
		return err
	}
	if row.Stage == "" && (row.InviteState == "not_requested" || row.InviteState == "pending" || row.InviteState == "creating") {
		if err := service.enqueue(ctx, row, "preparing", nil); err != nil {
			return err
		}
		if row.State == "unreachable" {
			return nil
		}
	}
	if previous != row.Generation {
		return nil
	}
	identity := m.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}
	groups, err := service.Access.ListMemberships(ctx, identity, row.TargetID-1, 1)
	if err != nil {
		return err
	}
	if len(groups) > 0 && groups[0].TargetID == row.TargetID && (groups[0].MemberStatus == "member" || groups[0].MemberStatus == "administrator" || groups[0].MemberStatus == "creator") {
		return service.enqueue(ctx, row, "renewed", nil)
	}
	entry, err := service.Access.RequestMembershipInvite(ctx, identity, row.TargetID)
	if err != nil {
		return err
	}
	if entry == nil {
		return m.ErrCapability
	}
	previousInvite, previousState := row.InviteID, row.InviteState
	row.InviteID, row.InviteState = entry.InviteID, entry.InviteState
	if previousInvite != row.InviteID || previousState != row.InviteState {
		return nil
	}
	switch entry.InviteState {
	case "active":
		return service.enqueue(ctx, row, "ready", entry)
	case "unknown", "needs_review":
		return service.enqueue(ctx, row, "unknown", nil)
	case "failed":
		return service.enqueue(ctx, row, "failed", nil)
	}
	if !service.now().Before(row.CreatedAt.Add(15 * time.Second)) {
		return service.enqueue(ctx, row, "slow", nil)
	}
	return nil
}

// validate checks immutable source ownership, binding, rights, physical routing and execution lease.
func (service *Service) validate(ctx context.Context, row *d.ProactiveDelivery) error {
	if err := service.Store.ValidateProactive(ctx, row, service.now()); err != nil {
		return err
	}
	route, err := service.Routes.MembershipRoute(ctx, row.TargetID)
	if err != nil {
		return err
	}
	if route.BotID != row.BotID || route.ChatID != row.ChatID || route.RouteVersion != row.RouteVersion {
		return m.ErrConflict
	}
	if row.Generation != 0 && row.Generation != route.Generation {
		return m.ErrConflict
	}
	row.Generation = route.Generation
	return r.CheckExecutionFence(ctx, row.BotID, route.Generation)
}

// receipt records only exact runtime evidence; missing positive message IDs are unknown.
func (service *Service) receipt(ctx context.Context, row *d.ProactiveDelivery) error {
	reader, ok := service.Queue.(r.BusinessDeliveryReader)
	if !ok {
		return m.ErrCapability
	}
	result, err := reader.BusinessDelivery(ctx, row.BotID, row.BusinessKey)
	if err != nil {
		return err
	}
	switch result.State {
	case "sent":
		if result.MessageID <= 0 {
			row.State = "unknown"
			return nil
		}
		row.MessageID = result.MessageID
		row.State = "sent"
		if row.Stage == "unknown" || row.Stage == "failed" {
			row.State = row.Stage
		}
	case "unknown", "failed":
		state, stage, key := result.State, row.Stage, row.BusinessKey
		if err := service.enqueue(ctx, row, "send_"+state, nil); err != nil {
			return err
		}
		row.State, row.Stage, row.BusinessKey = state, stage, key
	case "unreachable", "superseded":
		row.State = result.State
	case "not_requested":
		row.State = "preparing"
	}
	return nil
}

// enqueue inserts one encrypted independent message per stable task/stage key, never a panel edit.
// Slow feedback remains independently idempotent and does not stop invitation preparation.
func (service *Service) enqueue(ctx context.Context, row *d.ProactiveDelivery, stage string, entry *m.AccessEntry) error {
	reader, ok := service.Queue.(r.BusinessDeliveryReader)
	if !ok {
		return m.ErrCapability
	}
	key := businessKey(row.ID, row.InviteID, stage)
	receipt, err := reader.BusinessDelivery(ctx, row.BotID, key)
	if err != nil {
		return err
	}
	if receipt.State != "not_requested" {
		if stage != "preparing" && stage != "slow" && stage != "send_failed" && stage != "send_unknown" {
			row.Stage, row.BusinessKey, row.State = stage, key, "queued"
		}
		return nil
	}
	contact, err := service.Queue.Contact(ctx, row.BotID, row.TelegramID)
	if errors.Is(err, r.ErrNotFound) {
		row.State = "unreachable"
		return nil
	}
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.ChatID != row.TelegramID || contact.UserID != row.AccountID {
		row.State = "unreachable"
		return nil
	}
	text, label := feedbackText(contact.Locale, stage, row.TargetID)
	if stage == "ready" || stage == "renewed" {
		text, err = service.membershipFeedback(ctx, row, contact.Locale, text)
		if err != nil {
			return err
		}
	}
	message := r.Message{ProactiveDelivery: &r.ProactiveDeliveryRef{ID: row.ID, Stage: stage}, TargetType: r.TargetShopPrivate, Purpose: "membership_proactive", Operation: "send", BusinessRef: key, OwnerUserID: row.AccountID, ChatID: row.TelegramID, Text: text, IdentityStamp: fmt.Sprintf("%d:%d", row.BindingVersion, row.AccountID)}
	if entry != nil {
		message.InviteDelivery, err = service.Access.PrepareInviteDelivery(ctx, m.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}, entry.InviteID)
		if err != nil {
			return err
		}
		message.Buttons = [][]r.Button{{{Text: label, URL: entry.Link}}}
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	cipher, err := service.Cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	if err := service.Queue.Enqueue(ctx, &rd.Delivery{BotID: row.BotID, Key: key, ChatID: row.TelegramID, TelegramUserID: row.TelegramID, UserID: row.AccountID, PayloadCipher: cipher, TargetType: string(r.TargetShopPrivate), Purpose: message.Purpose, Operation: "send", Priority: 100, Status: "pending", AvailableAt: service.now()}); err != nil {
		return err
	}
	if stage != "preparing" && stage != "slow" && stage != "send_failed" && stage != "send_unknown" {
		row.Stage, row.BusinessKey, row.State = stage, key, "queued"
	}
	return nil
}

// AuthorizeProactiveDelivery requires an exact durable grant responsibility, not a panel permission.
// Dependency failures defer pre-send only; definitive revocations supersede without any external effect.
func (service *Service) AuthorizeProactiveDelivery(ctx context.Context, bot uint, message r.Message) (result error) {
	defer func() {
		if result == nil {
			return
		}
		var invalidated r.DeliveryInvalidated
		if errors.As(result, &invalidated) {
			return
		}
		if errors.Is(result, m.ErrInvalid) || errors.Is(result, m.ErrUntrustedIdentity) || errors.Is(result, m.ErrConflict) {
			result = r.DeliveryInvalidated{State: "superseded"}
			return
		}
		result = r.DeliveryDeferred{AvailableAt: time.Now().UTC().Add(5 * time.Second), Transient: true}
	}()
	ref := message.ProactiveDelivery
	if ref == nil || message.Purpose != "membership_proactive" || message.Operation != "send" || message.PanelEpoch != "" || message.PanelRevision != 0 || message.TargetMessageID != 0 || message.PublicReply != nil || message.ReminderDelivery != nil || message.Transfer != nil {
		return m.ErrUntrustedIdentity
	}
	row, err := service.Store.Proactive(ctx, ref.ID)
	if err != nil {
		return err
	}
	if row.BotID != bot || row.AccountID != message.OwnerUserID || row.TelegramID != message.ChatID || message.IdentityStamp != fmt.Sprintf("%d:%d", row.BindingVersion, row.AccountID) || message.BusinessRef != businessKey(row.ID, row.InviteID, ref.Stage) {
		return m.ErrUntrustedIdentity
	}
	if row.State != "preparing" && row.State != "queued" && !(ref.Stage == "send_unknown" && row.State == "unknown") && !(ref.Stage == "send_failed" && row.State == "failed") {
		return r.DeliveryInvalidated{State: "superseded"}
	}
	if err := service.validate(ctx, row); err != nil {
		return err
	}
	contact, err := service.Queue.Contact(ctx, bot, row.TelegramID)
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.ChatID != row.TelegramID || contact.UserID != row.AccountID {
		return r.DeliveryInvalidated{State: "unreachable"}
	}
	if (ref.Stage == "preparing" || ref.Stage == "slow") && row.Stage != "" {
		return r.DeliveryInvalidated{State: "superseded"}
	}
	if ref.Stage == "ready" && row.InviteState != "active" {
		return m.ErrConflict
	}
	if err := service.authorizeStage(ctx, row, ref.Stage); err != nil {
		return err
	}
	switch ref.Stage {
	case "ready":
		if message.InviteDelivery == nil || message.InviteDelivery.InviteID != row.InviteID || message.InviteDelivery.TargetID != row.TargetID {
			return m.ErrUntrustedIdentity
		}
		return service.Access.AuthorizeProactiveInviteDelivery(ctx, bot, message)
	case "preparing", "slow", "unknown", "failed", "renewed", "send_unknown", "send_failed":
		if message.InviteDelivery != nil || len(message.Buttons) != 0 {
			return m.ErrUntrustedIdentity
		}
	default:
		return m.ErrInvalid
	}
	return nil
}

// authorizeStage validates the feedback fact independently of message-provided stage labels.
// It never requests an invite or replays a send; renewed relies on current persisted membership observation.
func (service *Service) authorizeStage(ctx context.Context, row *d.ProactiveDelivery, stage string) error {
	switch stage {
	case "preparing":
		if row.InviteState != "not_requested" && row.InviteState != "pending" && row.InviteState != "creating" {
			return m.ErrConflict
		}
	case "slow":
		if service.now().Before(row.CreatedAt.Add(15 * time.Second)) {
			return m.ErrConflict
		}
	case "unknown":
		if row.InviteState != "unknown" && row.InviteState != "needs_review" {
			return m.ErrConflict
		}
	case "failed":
		if row.InviteState != "failed" {
			return m.ErrConflict
		}
	case "renewed":
		groups, err := service.Access.ListMemberships(ctx, m.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}, row.TargetID-1, 1)
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].TargetID != row.TargetID || (groups[0].MemberStatus != "member" && groups[0].MemberStatus != "administrator" && groups[0].MemberStatus != "creator") {
			return m.ErrConflict
		}
	case "send_failed", "send_unknown":
		reader, ok := service.Queue.(r.BusinessDeliveryReader)
		if !ok {
			return m.ErrCapability
		}
		receipt, err := reader.BusinessDelivery(ctx, row.BotID, row.BusinessKey)
		if err != nil {
			return err
		}
		if "send_"+receipt.State != stage {
			return m.ErrConflict
		}
	}
	return nil
}

// membershipFeedback appends an authorized group name and the entitlement term, never the link term.
// ctx scopes a fresh identity-checked read; locale selects supported labels; returns safe text or a read error.
func (service *Service) membershipFeedback(ctx context.Context, row *d.ProactiveDelivery, locale, text string) (string, error) {
	groups, err := service.Access.ListMemberships(ctx, m.AccessIdentity{AccountID: row.AccountID, TelegramID: row.TelegramID, BotID: row.BotID}, row.TargetID-1, 1)
	if err != nil {
		return "", err
	}
	if len(groups) != 1 || groups[0].TargetID != row.TargetID || !groups[0].Active {
		return "", m.ErrUntrustedIdentity
	}
	group := groups[0]
	groupLabel, termLabel, permanent := "会员群", "权益期限", "永久"
	if locale == "zh-TW" || locale == "zh-HK" || locale == "zh-Hant" {
		groupLabel, termLabel = "會員群", "權益期限"
	} else if locale == "en" || locale == "en-US" {
		groupLabel, termLabel, permanent = "Group", "Membership term", "Permanent"
	}
	title := group.Title
	if title == "" {
		title = fmt.Sprintf("#%d", group.TargetID)
	}
	term := permanent
	if group.Kind != string(d.TermPermanent) {
		if group.ExpiresAt == nil {
			return "", m.ErrConflict
		}
		term = constants.DisplayTime(*group.ExpiresAt, "2006-01-02 15:04 UTC+8")
	}
	return fmt.Sprintf("%s\n%s：%s\n%s：%s", text, groupLabel, title, termLabel, term), nil
}

// businessKey preserves the existing invite receipt namespace only for actual invitation messages.
func businessKey(id, invite uint, stage string) string {
	if stage == "ready" {
		return fmt.Sprintf("membership-invite:%d:proactive:%d:ready", invite, id)
	}
	return fmt.Sprintf("membership-proactive:%d:%s", id, stage)
}

// feedbackText renders three-language stage feedback without claiming actual admission or fake delivery.
func feedbackText(locale, stage string, target uint) (string, string) {
	texts := map[string][3]string{
		"preparing":    {"会员 #%d 权益已生效，正在准备入群入口，完成后会通知你。", "會員 #%d 權益已生效，正在準備入群入口，完成後會通知你。", "Membership #%d is active. We are preparing your invitation and will notify you when ready."},
		"ready":        {"会员 #%d 权益已生效。入群入口已准备好，可按需申请加入。", "會員 #%d 權益已生效。入群入口已準備好，可按需申請加入。", "Membership #%d is active. Your invitation is ready; joining is optional."},
		"renewed":      {"会员 #%d 权益已更新，你已在群内，无需重新加入。", "會員 #%d 權益已更新，你已在群內，無需重新加入。", "Membership #%d has been updated. You are already a member; no need to rejoin."},
		"slow":         {"会员 #%d 权益已生效，入群入口仍在准备，完成后会通知你。", "會員 #%d 權益已生效，入群入口仍在準備，完成後會通知你。", "Membership #%d is active. Your invitation is taking longer to prepare; we will notify you when ready."},
		"failed":       {"会员 #%d 权益已生效，但入群入口准备失败。请通过 /memberships 查看或联系客服。", "會員 #%d 權益已生效，但入群入口準備失敗。請透過 /memberships 查看或聯絡客服。", "Membership #%d is active, but invitation preparation failed. Check /memberships or contact support."},
		"unknown":      {"会员 #%d 权益已生效，入群入口结果待核实。不会重复创建，请通过 /memberships 查看或联系客服。", "會員 #%d 權益已生效，入群入口結果待核實。不會重複建立，請透過 /memberships 查看或聯絡客服。", "Membership #%d is active. The invitation result needs verification and will not be retried blindly. Check /memberships or contact support."},
		"send_unknown": {"会员 #%d 权益已生效，邀请消息是否送达待核实。不会重复发送，请通过 /memberships 查看。", "會員 #%d 權益已生效，邀請訊息是否送達待核實。不會重複傳送，請透過 /memberships 查看。", "Membership #%d is active. Invitation delivery is unconfirmed and will not be retried blindly. Check /memberships."},
		"send_failed":  {"会员 #%d 权益已生效，邀请消息未能送达。请通过 /memberships 查看入口。", "會員 #%d 權益已生效，邀請訊息未能送達。請透過 /memberships 查看入口。", "Membership #%d is active, but invitation delivery failed. Check /memberships for your invitation."},
	}
	index, label := 0, "申请入群"
	if locale == "zh-TW" || locale == "zh-HK" || locale == "zh-Hant" {
		index, label = 1, "申請入群"
	}
	if locale == "en" || locale == "en-US" {
		index, label = 2, "Request to join"
	}
	return fmt.Sprintf(texts[stage][index], target), label
}
