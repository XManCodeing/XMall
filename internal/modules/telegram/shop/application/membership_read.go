package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"strconv"
	"strings"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// membershipPanelPending stores encrypted intent plus an outbox repair payload.
type membershipPanelPending struct {
	TargetID      uint
	Location      action
	Epoch         string
	MessageID     int64
	ReadyRevision uint64
	ReadyMessage  *rt.Message
}

// trackMembershipPanel accepts only explicit intent on a receipt-backed panel.
func (v *view) trackMembershipPanel(target uint) {
	if v.session.PanelState != "ready" || v.session.PanelMessageID <= 0 || v.session.PanelMessageEpoch != v.state.PanelEpoch {
		return
	}
	v.state.MembershipPending = &membershipPanelPending{TargetID: target, Location: v.state.Current, Epoch: v.state.PanelEpoch, MessageID: v.session.PanelMessageID}
}

// retainMembershipPending cancels completion after navigation or panel replacement.
func (v *view) retainMembershipPending() {
	pending := v.state.MembershipPending
	if pending != nil && (pending.Location != v.state.Current || pending.Epoch != v.state.PanelEpoch || v.replace) {
		v.state.MembershipPending = nil
		return
	}
	// A user refresh may supersede an untouched queued completion before it
	// sends. Re-read readiness later; GetExisting still blocks unknown delivery.
	if pending != nil && pending.ReadyRevision != 0 && pending.ReadyRevision <= v.state.PanelRevision {
		for _, message := range v.messages {
			if message.Purpose == "panel" {
				pending.ReadyRevision, pending.ReadyMessage = 0, nil
				break
			}
		}
	}
}

// captureMembershipCompletion preserves the payload across later no-op callbacks.
func (v *view) captureMembershipCompletion() {
	pending := v.state.MembershipPending
	if pending == nil || pending.ReadyRevision != v.state.PanelRevision || pending.ReadyMessage != nil {
		return
	}
	for _, message := range v.messages {
		if message.Purpose == "panel" {
			copy := message
			pending.ReadyMessage = &copy
			return
		}
	}
}

// ReconcileMembershipPanels scans one bounded page in the existing leased loop.
// Cursors provide fairness only; restart recovery uses durable encrypted sessions.
func (s *Service) ReconcileMembershipPanels(ctx context.Context, bot uint) error {
	reader, ok := s.d.Store.(rt.PanelSessionReader)
	queue, queued := s.d.Store.(interface {
		Enqueue(context.Context, *rd.Delivery) error
	})
	if !ok || !queued || s.membership == nil {
		return nil
	}
	s.panelScanMu.Lock()
	after := s.panelCursors[bot]
	s.panelScanMu.Unlock()
	rows, err := reader.PanelSessionsAfter(ctx, bot, after, 25, s.now())
	if err != nil {
		return err
	}
	var failures []error
	var next uint
	for _, row := range rows {
		next = row.ID
		messages, err := s.RefreshMembershipPanel(ctx, bot, row.TelegramUserID)
		if errors.Is(err, rt.ErrConflict) || errors.Is(err, rt.ErrNotFound) || errors.Is(err, c.ErrDenied) || errors.Is(err, c.ErrUnavailable) {
			continue
		}
		if err == nil {
			for _, message := range messages {
				err = s.enqueueMembershipPanel(ctx, queue, bot, row.TelegramUserID, message)
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(rows) < 25 {
		next = 0
	}
	s.panelScanMu.Lock()
	if s.panelCursors == nil {
		s.panelCursors = map[uint]uint{}
	}
	s.panelCursors[bot] = next
	s.panelScanMu.Unlock()
	return errors.Join(failures...)
}

// enqueueMembershipPanel reuses the same revision/key on persistence retries.
// Invite keys preserve the existing queued/unknown-delivery exclusion guard.
func (s *Service) enqueueMembershipPanel(ctx context.Context, queue interface {
	Enqueue(context.Context, *rd.Delivery) error
}, bot uint, tg int64, message rt.Message) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	cipher, err := crypto.Encrypt(s.key, string(raw))
	if err != nil {
		return err
	}
	key := fmt.Sprintf("membership-panel:%d:%s:%d", tg, message.PanelEpoch, message.PanelRevision)
	if message.InviteDelivery != nil {
		key = fmt.Sprintf("membership-invite:%d:panel:%s:%d", message.InviteDelivery.InviteID, message.PanelEpoch, message.PanelRevision)
	}
	return queue.Enqueue(ctx, &rd.Delivery{BotID: bot, Key: key, ChatID: tg, TelegramUserID: tg, UserID: message.OwnerUserID, PayloadCipher: cipher, Purpose: "panel", Operation: "edit", PanelEpoch: message.PanelEpoch, PanelRevision: message.PanelRevision, TargetMessageID: message.TargetMessageID, Priority: 100, Status: "pending", AvailableAt: s.now()})
}

// RefreshMembershipPanel reads completion without Request, creating only an edit
// to the original text panel. Identity, physical epoch and navigation stay fenced.
func (s *Service) RefreshMembershipPanel(ctx context.Context, bot uint, tg int64) ([]rt.Message, error) {
	row, err := s.d.Store.GetSession(ctx, bot, tg, tg)
	if err != nil {
		return nil, err
	}
	if row == nil || !row.ExpiresAt.After(s.now()) || row.DataCipher == "" {
		return nil, nil
	}
	plain, err := crypto.Decrypt(s.key, row.DataCipher)
	if err != nil {
		return nil, err
	}
	var st state
	if err = json.Unmarshal([]byte(plain), &st); err != nil {
		return nil, err
	}
	pending := st.MembershipPending
	if pending == nil || pending.Location != st.Current || pending.Epoch != st.PanelEpoch || row.PanelEpoch != st.PanelEpoch || row.PanelMessageEpoch != pending.Epoch || row.PanelState != "ready" || row.PanelMessageID != pending.MessageID || row.PanelKind != "text" {
		return nil, nil
	}
	stamp, err := s.DeliveryStamp(ctx, bot, tg)
	if err != nil {
		return nil, err
	}
	uid, err := s.d.Identity.Resolve(ctx, c.Actor{BotID: bot, TelegramID: tg}, false)
	if err != nil {
		return nil, err
	}
	if uid == 0 || uid != st.UserID || stamp != st.Generation {
		return nil, c.ErrDenied
	}
	if err = s.AuthorizeDelivery(ctx, bot, tg, uid); err != nil {
		return nil, err
	}
	if pending.ReadyRevision != 0 {
		if pending.ReadyRevision != st.PanelRevision || row.PanelDeliveredRevision >= pending.ReadyRevision || pending.ReadyMessage == nil {
			return nil, nil
		}
		return []rt.Message{*pending.ReadyMessage}, nil
	}
	reader, ok := s.membership.(c.MembershipReader)
	if !ok {
		return nil, nil
	}
	actor := c.Actor{BotID: bot, TelegramID: tg, UserID: uid, Generation: stamp, Locale: locale(st.Locale)}
	invitation, err := reader.GetExisting(ctx, actor, pending.TargetID)
	if err != nil {
		return nil, err
	}
	if invitation.State == "processing" || invitation.State == "recovering" || invitation.State == "unknown" {
		return nil, nil
	}
	v := &view{service: s, session: row, state: &st, actor: actor, update: rt.Update{UserID: tg, ChatID: tg, Private: true}}
	if s.d.Content != nil {
		v.content, err = s.d.Content.Content(ctx, actor)
		if err != nil {
			return nil, err
		}
	}
	st.Actions = map[string]action{}
	st.PanelTarget = pending.MessageID
	st.Ref, err = nonce()
	if err != nil {
		return nil, err
	}
	if st.Current.Kind == "memberships" {
		group, readErr := v.scopedMembership(ctx, action{ID: pending.TargetID, Owner: uid})
		if readErr != nil {
			return nil, readErr
		}
		if err = v.renderMembershipInvitation(*group, invitation, pending.TargetID); err == nil {
			v.membershipFooter(st.Current.ID)
		}
	} else {
		err = v.renderCurrent(ctx)
	}
	if err != nil {
		return nil, err
	}
	if st.Current != pending.Location || (invitation.State == "available" && (v.panel().InviteDelivery == nil || v.panel().InviteDelivery.TargetID != pending.TargetID)) {
		return nil, nil
	}
	pending.ReadyRevision = st.PanelRevision + 1
	return v.save(ctx)
}

// membershipRequestable validates the projected right; Request rechecks the
// binding, entitlement and current route before scheduling access work.
func (v *view) membershipRequestable(group c.MembershipGroup) bool {
	return group.CanRequest && membershipState(group.State) == "available" &&
		(group.Permanent || (group.ExpiresAt != nil && group.ExpiresAt.After(v.service.now())))
}

// orderMembershipGroups reads targets only after fresh exact order ownership.
func (v *view) orderMembershipGroups(ctx context.Context, orderID, owner uint) ([]c.MembershipGroup, error) {
	if owner == 0 || owner != v.actor.UserID || orderID == 0 {
		return nil, c.ErrDenied
	}
	order, err := v.service.d.Orders.Detail(ctx, v.actor, orderID, owner)
	if err != nil {
		return nil, err
	}
	if order == nil || order.ID != orderID || order.UserID != owner {
		return nil, c.ErrDenied
	}
	reader, ok := v.service.membership.(c.OrderMembershipReader)
	if !ok {
		reader, ok = v.service.d.Orders.(c.OrderMembershipReader)
	}
	if !ok {
		return nil, c.ErrUnavailable
	}
	groups, err := reader.Targets(ctx, v.actor, orderID, owner)
	if err != nil {
		return nil, err
	}
	if len(groups) > 100 {
		return nil, c.ErrDenied
	}
	seen := map[uint]bool{}
	for _, group := range groups {
		target, err := membershipID(group.TargetID)
		if err != nil || seen[target] || group.BeneficiaryTelegramID != strconv.FormatInt(v.actor.TelegramID, 10) {
			return nil, c.ErrDenied
		}
		seen[target] = true
	}
	return groups, nil
}

// scopedMembership validates an order target or one exclusive-cursor account target.
func (v *view) scopedMembership(ctx context.Context, location action) (*c.MembershipGroup, error) {
	if location.ID == 0 || location.Owner != v.actor.UserID {
		return nil, c.ErrDenied
	}
	var groups []c.MembershipGroup
	if location.Value != "" {
		orderID, err := membershipID(location.Value)
		if err != nil {
			return nil, err
		}
		groups, err = v.orderMembershipGroups(ctx, orderID, location.Owner)
		if err != nil {
			return nil, err
		}
	} else {
		page, err := v.membershipPage(ctx, location.ID-1)
		if err != nil {
			return nil, err
		}
		groups = page.Items
	}
	for _, group := range groups {
		if group.TargetID == strconv.FormatUint(uint64(location.ID), 10) {
			return &group, nil
		}
	}
	return nil, c.ErrDenied
}

// replacementFacts displays historical chat IDs, never invented titles or bans.
func (v *view) replacementFacts(group c.MembershipGroup) {
	if !group.Replaced {
		return
	}
	for _, chat := range group.HistoricalChats {
		chatID, err := strconv.ParseInt(chat, 10, 64)
		if err == nil && chatID < 0 && strconv.FormatInt(chatID, 10) == chat {
			v.say(fmt.Sprintf(text(v.actor.Locale, "membership_old_chat"), chat))
		}
	}
	v.say(text(v.actor.Locale, "membership_replaced") + " · " + short(group.Title, 80))
}

// membershipApplyButton emits only a normal callback when no URL is present.
func (v *view) membershipApplyButton(group c.MembershipGroup, location action) {
	if v.panel().InviteDelivery != nil || !v.membershipRequestable(group) {
		return
	}
	location.Kind = "membership_apply"
	v.button(text(v.actor.Locale, "membership_apply")+" · "+short(group.Title, 80), location)
}

// applyScopedMembership rechecks signed action scope before the explicit Request.
// Unknown delivery never retries; existing processing repairs a failed session save.
func (v *view) applyScopedMembership(ctx context.Context, location action) error {
	if v.update.CallbackData == "" || v.actor.UserID == 0 {
		return c.ErrDenied
	}
	group, err := v.scopedMembership(ctx, location)
	if err != nil {
		return err
	}
	reader, ok := v.service.membership.(c.MembershipReader)
	if !ok {
		return c.ErrUnavailable
	}
	invitation, err := reader.GetExisting(ctx, v.actor, location.ID)
	if err != nil {
		return err
	}
	if invitation.State == "unknown" {
		return v.renderCurrent(ctx)
	}
	if invitation.State == "processing" || invitation.State == "recovering" {
		v.trackMembershipPanel(location.ID)
		return v.renderCurrent(ctx)
	}
	if !v.membershipRequestable(*group) || usableMembershipInvitation(invitation, v) {
		return v.renderCurrent(ctx)
	}
	result, err := v.service.membership.Request(ctx, v.actor, location.ID)
	if err != nil {
		return err
	}
	if result.State == "processing" || result.State == "recovering" {
		v.trackMembershipPanel(location.ID)
	}
	if err := v.renderCurrent(ctx); err != nil {
		return err
	}
	if v.panel().InviteDelivery == nil && usableMembershipInvitation(result, v) {
		current, err := v.scopedMembership(ctx, location)
		if err != nil {
			return err
		}
		return v.renderMembershipInvitation(*current, result, location.ID)
	}
	return nil
}

// removeMembershipApply replaces a callback with a URL. Keep its key allocated
// as read-only so later buttons cannot collide with another existing action key.
func (v *view) removeMembershipApply(target uint) {
	removed := map[string]bool{}
	for key, act := range v.state.Actions {
		if act.Kind == "membership_apply" && act.ID == target {
			removed[key] = true
			act.Kind = "membership"
			v.state.Actions[key] = act
		}
	}
	rows := v.panel().Buttons[:0]
	for _, row := range v.panel().Buttons {
		buttons := row[:0]
		for _, button := range row {
			parts := strings.Split(button.Data, ":")
			if len(parts) != 4 || !removed[parts[3]] {
				buttons = append(buttons, button)
			}
		}
		if len(buttons) > 0 {
			rows = append(rows, buttons)
		}
	}
	v.panel().Buttons = rows
}

// usableMembershipInvitation checks link shape and lifetime without exposing it.
func usableMembershipInvitation(invitation c.MembershipInvitation, v *view) bool {
	return invitation.State == "available" && validMembershipURL(strings.TrimSpace(invitation.InviteURL)) &&
		((invitation.Permanent && invitation.ExpiresAt == nil) || (invitation.ExpiresAt != nil && invitation.ExpiresAt.After(v.service.now())))
}

// existingMembership reads one target without Request. The reader must revalidate
// the actor's current beneficiary and bot route before returning any invitation.
func (v *view) existingMembership(ctx context.Context, location action) error {
	if location.ID == 0 || location.Owner != v.actor.UserID || v.actor.UserID == 0 {
		return c.ErrDenied
	}
	reader, ok := v.service.membership.(c.MembershipReader)
	if !ok {
		return c.ErrUnavailable
	}
	v.state.Current = action{Kind: "membership", ID: location.ID, Owner: location.Owner, Value: location.Value}
	v.state.ReplyOwner = v.actor.UserID
	group, err := v.scopedMembership(ctx, location)
	if err != nil {
		return err
	}
	v.replacementFacts(*group)
	invitation, err := reader.GetExisting(ctx, v.actor, location.ID)
	if err != nil {
		return err
	}
	if err := v.renderMembershipInvitation(*group, invitation, location.ID); err != nil {
		return err
	}
	v.membershipApplyButton(*group, v.state.Current)
	v.button(text(v.actor.Locale, "refresh"), v.state.Current)
	v.homeButton()
	return nil
}

// renderMembershipInvitation combines current group facts with an independently read invitation.
// group and invitation must belong to target; returns a rendering error without requesting access.
func (v *view) renderMembershipInvitation(group c.MembershipGroup, invitation c.MembershipInvitation, target uint) error {
	status := membershipState(group.State)
	if status == "available" && !group.Permanent && (group.ExpiresAt == nil || !group.ExpiresAt.After(v.service.now())) {
		status = "expired"
	}
	// Historical invitation states cannot override current entitlement, member or recovery facts.
	// A newly unusable group never discloses a link obtained from another read snapshot.
	if status != "available" {
		v.say(text(v.actor.Locale, "membership_"+status))
		return nil
	}
	if invitation.State == "expired" || invitation.State == "revoked" || invitation.State == "unavailable" {
		v.say(text(v.actor.Locale, "membership_available"))
		if invitation.State == "unavailable" {
			return nil
		}
	}
	return v.renderInvitation(invitation, target)
}

// renderInvitation attaches exactly one scoped invitation to the current panel.
// A missing expiry is accepted only with an explicit permanent-link marker.
func (v *view) renderInvitation(invitation c.MembershipInvitation, target uint) error {
	status := membershipState(invitation.State)
	key := "membership_" + status
	if status == "expired" || status == "revoked" {
		key = "membership_invite_" + status
	}
	v.say(text(v.actor.Locale, key))
	live := invitation.ExpiresAt != nil && invitation.ExpiresAt.After(v.service.now())
	permanent := invitation.Permanent && invitation.ExpiresAt == nil
	if status != "available" || !(live || permanent) || !validMembershipURL(invitation.InviteURL) {
		return nil
	}
	ref := invitation.InviteDelivery
	if ref == nil || ref.InviteID == 0 || ref.TargetID != target || v.panel().InviteDelivery != nil {
		return c.ErrDenied
	}
	v.panel().InviteDelivery = ref
	v.removeMembershipApply(target)
	if permanent {
		v.say(text(v.actor.Locale, "membership_permanent"))
	}
	v.urlButton(text(v.actor.Locale, "membership_open"), invitation.InviteURL)
	return nil
}

// membershipRenewalButtons renders one immutable item selector per product/SKU.
// Multi-group purchases stay one purchase; the order is freshly authorized on click.
func (v *view) membershipRenewalButtons(order *c.Order) {
	seen := map[c.Selection]bool{}
	for index, item := range order.Items {
		selection := c.Selection{ProductID: item.ProductID, SKUID: item.SKUID}
		if item.ProductID == 0 || seen[selection] {
			continue
		}
		seen[selection] = true
		label := text(v.actor.Locale, "rebuy")
		if len(order.Items) > 1 {
			label += " · " + short(item.Title+" "+item.SKUName, 45)
		}
		v.button(label, action{Kind: "membership_renew", ID: order.ID, Owner: order.UserID, Page: index + 1})
	}
}

// renewMembership opens the existing read-only purchase page after fresh ownership
// and catalog validation. It never creates an order, pays, or requests an invite.
func (v *view) renewMembership(ctx context.Context, location action) error {
	if v.actor.UserID == 0 || location.Owner != v.actor.UserID || location.ID == 0 || location.Page < 1 {
		return c.ErrDenied
	}
	order, err := v.service.d.Orders.Detail(ctx, v.actor, location.ID, location.Owner)
	if err != nil {
		return err
	}
	if order == nil || order.UserID != v.actor.UserID || location.Page > len(order.Items) {
		return c.ErrDenied
	}
	item := order.Items[location.Page-1]
	product, err := v.service.d.Catalog.Product(ctx, v.actor, item.ProductID)
	if err != nil && !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrUnavailable) {
		return err
	}
	available := err == nil && product != nil && product.ID == item.ProductID && product.Available && !product.Unsupported
	if available && item.SKUID != 0 && len(product.SKUs) == 0 {
		available = false
	}
	if available && len(product.SKUs) > 0 {
		available = false
		for _, sku := range product.SKUs {
			if sku.ID == item.SKUID && sku.Available {
				available = true
			}
		}
	}
	if !available {
		v.say(text(v.actor.Locale, "sold_out"))
		v.state.Current = action{Kind: "order", ID: order.ID, Owner: order.UserID}
		v.homeButton()
		return nil
	}
	v.state.Selection = c.Selection{ProductID: item.ProductID, SKUID: item.SKUID, Quantity: max(1, product.Min)}
	v.state.Quote, v.state.Payment = nil, nil
	v.state.Operation, v.state.Step = "", ""
	v.state.PaymentUncertain, v.state.MoreQuantity = false, false
	return v.purchase(ctx, true)
}

// orderMemberships projects only snapshot-linked targets. One target can disclose
// one invitation; multiple targets render navigation buttons, never multiple links.
func (v *view) orderMemberships(ctx context.Context, order *c.Order) error {
	reader, ok := v.service.d.Orders.(c.OrderMembershipReader)
	if direct, available := v.service.membership.(c.OrderMembershipReader); available {
		reader, ok = direct, true
	}
	if !ok || order.UserID != v.actor.UserID {
		return nil
	}
	groups, err := reader.Targets(ctx, v.actor, order.ID, order.UserID)
	if err != nil {
		return err
	}
	if len(groups) > 100 {
		return c.ErrDenied
	}
	if len(groups) > 0 {
		v.membershipRenewalButtons(order)
	}
	seen := map[uint]bool{}
	for _, group := range groups {
		target, err := membershipID(group.TargetID)
		if err != nil || seen[target] || group.BeneficiaryTelegramID != strconv.FormatInt(v.actor.TelegramID, 10) {
			return c.ErrDenied
		}
		seen[target] = true
		v.replacementFacts(group)
		location := action{Kind: "membership", ID: target, Owner: v.actor.UserID, Value: strconv.FormatUint(uint64(order.ID), 10)}
		v.button(short(group.Title, 80), location)
		if len(groups) > 1 {
			v.membershipApplyButton(group, location)
		}
	}
	if len(groups) == 1 || (len(groups) > 1 && v.state.MembershipPending != nil && v.state.MembershipPending.Location == v.state.Current) {
		if invitations, ok := v.service.membership.(c.MembershipReader); ok {
			selected := groups[0]
			if len(groups) > 1 {
				selected = c.MembershipGroup{}
				for _, group := range groups {
					if group.TargetID == strconv.FormatUint(uint64(v.state.MembershipPending.TargetID), 10) {
						selected = group
					}
				}
			}
			target, err := membershipID(selected.TargetID)
			if err != nil {
				return c.ErrDenied
			}
			invitation, err := invitations.GetExisting(ctx, v.actor, target)
			if err != nil {
				return err
			}
			if err := v.renderMembershipInvitation(selected, invitation, target); err != nil {
				return err
			}
			v.membershipApplyButton(selected, action{ID: target, Owner: v.actor.UserID, Value: strconv.FormatUint(uint64(order.ID), 10)})
			return nil
		}
	}
	return nil
}
