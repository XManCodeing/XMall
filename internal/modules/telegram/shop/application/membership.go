package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// SetMembershipAccess installs the optional access port before serving updates.
// Passing nil disables access without changing the existing constructor or sales gate.
func (s *Service) SetMembershipAccess(access c.MembershipAccess) { s.membership = access }

// membershipCommand identifies the read-only command without interpreting user IDs.
func membershipCommand(input string) bool {
	parts := strings.Fields(input)
	return len(parts) > 0 && strings.Split(parts[0], "@")[0] == "/memberships"
}

// membershipIdentityCreation preserves ordinary shopping registration but never
// creates an account while querying or restoring a membership panel, even via text.
func (s *Service) membershipIdentityCreation(ctx context.Context, botID uint, update rt.Update, isolated bool) (bool, error) {
	if isolated || update.CallbackData != "" || membershipCommand(update.Text) {
		return false, nil
	}
	if strings.HasPrefix(strings.TrimSpace(update.Text), "/") {
		return true, nil
	}
	row, err := s.d.Store.GetSession(ctx, botID, update.UserID, update.ChatID)
	if errors.Is(err, rt.ErrNotFound) || (err == nil && row == nil) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if row.DataCipher == "" {
		return false, nil
	}
	plain, err := crypto.Decrypt(s.key, row.DataCipher)
	if err != nil {
		return false, err
	}
	var previous state
	if err := json.Unmarshal([]byte(plain), &previous); err != nil {
		return false, err
	}
	return previous.Current.Kind != "memberships" && previous.Current.Kind != "membership_request", nil
}

// membershipButton exposes current-account access independently of notifications.
func (v *view) membershipButton() {
	v.button(text(v.actor.Locale, "memberships"), action{Kind: "memberships"})
}

// membershipID validates canonical positive IDs; no floats, signs or leading zeros.
func membershipID(raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != raw {
		return 0, c.ErrDenied
	}
	return uint(id), nil
}

// membershipState fails closed for new or unrecognized backend states.
func membershipState(value string) string {
	switch value {
	case "processing", "available", "recovering", "unknown", "expired", "revoked", "identity_mismatch", "unavailable", "member":
		return value
	default:
		return "unknown"
	}
}

// membershipPage validates a bounded projection before any title or action is exposed.
// Actor identity is rechecked by the port, and a mismatching beneficiary is never displayed.
func (v *view) membershipPage(ctx context.Context, cursor uint) (c.MembershipPage, error) {
	if v.service.membership == nil || v.actor.UserID == 0 || v.actor.TelegramID <= 0 {
		return c.MembershipPage{}, c.ErrUnavailable
	}
	page, err := v.service.membership.List(ctx, v.actor, cursor, 5)
	if err != nil {
		return c.MembershipPage{}, err
	}
	if len(page.Items) > 5 {
		return c.MembershipPage{}, c.ErrDenied
	}
	previous := cursor
	for index := range page.Items {
		item := &page.Items[index]
		target, err := membershipID(item.TargetID)
		beneficiary, idErr := strconv.ParseInt(item.BeneficiaryTelegramID, 10, 64)
		if err != nil || target <= previous || idErr != nil || beneficiary <= 0 || strconv.FormatInt(beneficiary, 10) != item.BeneficiaryTelegramID || beneficiary != v.actor.TelegramID {
			return c.MembershipPage{}, c.ErrDenied
		}
		previous = target
		item.State = membershipState(item.State)
		if item.Permanent {
			item.ExpiresAt = nil
		} else if item.State == "available" && item.ExpiresAt != nil && !item.ExpiresAt.After(v.service.now()) {
			item.State = "expired"
		}
		item.CanRequest = item.CanRequest && item.State == "available" && (item.Permanent || (item.ExpiresAt != nil && item.ExpiresAt.After(v.service.now())))
	}
	if page.NextCursor != "" {
		next, err := membershipID(page.NextCursor)
		if err != nil || len(page.Items) == 0 || next != previous {
			return c.MembershipPage{}, c.ErrDenied
		}
	}
	return page, nil
}

// memberships renders read-only facts and explicit actions; ID is the exclusive cursor.
func (v *view) memberships(ctx context.Context, act action) error {
	v.state.Current = action{Kind: "memberships", ID: act.ID}
	v.state.ReplyOwner = v.actor.UserID
	page, err := v.membershipPage(ctx, act.ID)
	if err != nil {
		if !errors.Is(err, c.ErrUnavailable) && !errors.Is(err, c.ErrDenied) {
			return err
		}
		v.say(text(v.actor.Locale, "membership_unavailable"))
		v.membershipFooter(act.ID)
		return nil
	}
	if len(page.Items) == 1 && act.ID == 0 && page.NextCursor == "" {
		if _, ok := v.service.membership.(c.MembershipReader); ok {
			target, _ := membershipID(page.Items[0].TargetID)
			v.html("<b>" + html.EscapeString(short(page.Items[0].Title, 80)) + "</b>")
			return v.existingMembership(ctx, action{Kind: "membership", ID: target, Owner: v.actor.UserID})
		}
	}
	v.html("<b>" + html.EscapeString(text(v.actor.Locale, "memberships")) + "</b>")
	if len(page.Items) == 0 {
		v.say(text(v.actor.Locale, "membership_empty"))
	}
	for _, item := range page.Items {
		v.membershipItem(item, act.ID)
	}
	if page.NextCursor != "" {
		next, _ := membershipID(page.NextCursor) // Already validated above.
		v.button(text(v.actor.Locale, "page_next"), action{Kind: "memberships", ID: next})
	}
	if act.ID > 0 {
		v.button(text(v.actor.Locale, "back_list"), action{Kind: "memberships"})
	}
	v.membershipFooter(act.ID)
	return nil
}

// membershipItem escapes public names and shows no deadline for permanent rights.
func (v *view) membershipItem(item c.MembershipGroup, cursor uint) {
	title := short(item.Title, 80)
	v.html("<b>" + html.EscapeString(title) + "</b>")
	v.say(text(v.actor.Locale, "membership_"+item.State))
	if item.Permanent {
		v.say(text(v.actor.Locale, "membership_permanent"))
	} else if item.ExpiresAt != nil {
		v.say(text(v.actor.Locale, "expires") + v.clock(*item.ExpiresAt))
	}
	if _, ok := v.service.membership.(c.MembershipReader); ok {
		target, _ := membershipID(item.TargetID)
		v.button(title, action{Kind: "membership", ID: target, Owner: v.actor.UserID})
	} else if item.CanRequest {
		target, _ := membershipID(item.TargetID) // Validated by membershipPage.
		v.button(text(v.actor.Locale, "membership_apply")+" · "+title, action{Kind: "membership_request", ID: target, Owner: v.actor.UserID, Value: strconv.FormatUint(uint64(cursor), 10)})
	}
}

// membershipFooter keeps recovery read-only and renewal within the existing closed-sales catalog.
func (v *view) membershipFooter(cursor uint) {
	v.button(text(v.actor.Locale, "refresh"), action{Kind: "memberships", ID: cursor})
	v.button(text(v.actor.Locale, "membership_renew"), action{Kind: "catalog"})
	v.urlButton(text(v.actor.Locale, "support"), v.content.SupportURL)
	v.navRow(text(v.actor.Locale, "account"), action{Kind: "account"})
}

// requestMembership accepts only a current panel action and rechecks the same page.
// A newly unknown state or binding change cannot trigger the side-effecting port.
func (v *view) requestMembership(ctx context.Context, act action) error {
	if act.ID == 0 || act.Owner != v.actor.UserID || v.update.CallbackData == "" {
		return c.ErrDenied
	}
	cursor, err := strconv.ParseUint(act.Value, 10, strconv.IntSize)
	if err != nil || strconv.FormatUint(cursor, 10) != act.Value {
		return c.ErrDenied
	}
	v.state.Current = action{Kind: "memberships", ID: uint(cursor)}
	page, err := v.membershipPage(ctx, uint(cursor))
	if err != nil {
		return err
	}
	allowed := false
	for _, item := range page.Items {
		allowed = allowed || (item.TargetID == strconv.FormatUint(uint64(act.ID), 10) && item.CanRequest)
	}
	if !allowed {
		return v.memberships(ctx, v.state.Current)
	}
	rt.AnswerCallback(ctx, text(v.actor.Locale, "processing"))
	result, err := v.service.membership.Request(ctx, v.actor, act.ID)
	if err != nil {
		return err
	}
	if result.State == "processing" || result.State == "recovering" {
		v.trackMembershipPanel(act.ID)
	}
	if err := v.renderInvitation(result, act.ID); err != nil {
		return err
	}
	v.membershipFooter(uint(cursor))
	return nil
}

// AuthorizeInviteDelivery checks membership-specific scope in addition to runtime identity stamps.
func (s *Service) AuthorizeInviteDelivery(ctx context.Context, bot uint, message rt.Message) error {
	guard, ok := s.membership.(rt.InviteDeliveryAuthorizer)
	if !ok {
		return c.ErrDenied
	}
	return guard.AuthorizeInviteDelivery(ctx, bot, message)
}

// validMembershipURL accepts only Telegram join-request links, never fallback help pages.
func validMembershipURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "t.me" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	path := parsed.Path
	token := ""
	switch {
	case strings.HasPrefix(path, "/+"):
		token = strings.TrimPrefix(path, "/+")
	case strings.HasPrefix(path, "/joinchat/"):
		token = strings.TrimPrefix(path, "/joinchat/")
	}
	if token == "" || parsed.RawPath != "" {
		return false
	}
	for _, char := range token {
		if !(char >= 'A' && char <= 'Z') && !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

// membershipTermLabel renders a persisted SKU term in the actor locale; no duration is inferred from SKU names.
func membershipTermLabel(locale string, term *c.MembershipTerm) string {
	if term == nil {
		return ""
	}
	if term.Kind == "permanent" && term.Months == 0 {
		return text(locale, "membership_permanent")
	}
	if term.Kind == "months" && term.Months > 0 {
		return fmt.Sprintf(text(locale, "membership_months"), term.Months)
	}
	return ""
}

// selectMembershipTerm accepts only a configured SKU of product and returns its read-only panel.
func (v *view) selectMembershipTerm(product *c.Product, skuID uint) error {
	for _, sku := range product.SKUs {
		if sku.ID == skuID && membershipTermLabel(v.actor.Locale, sku.MembershipTerm) != "" {
			v.state.Selection.SKUID = skuID
			return v.membershipPurchase(product)
		}
	}
	return c.ErrDenied
}

// membershipPurchase renders only existing configured packages and always clears payable state.
// product must already have passed public visibility and shared-config validation.
func (v *view) membershipPurchase(product *c.Product) error {
	v.state.Current = action{Kind: "purchase"}
	v.state.Selection.ProductID = product.ID
	v.state.Selection.Quantity = 1
	v.state.Step, v.state.Operation = "", ""
	v.state.Quote, v.state.Payment = nil, nil
	v.state.OrderID = 0
	v.state.MoreQuantity, v.state.PaymentUncertain = false, false
	configured := make([]c.SKU, 0, len(product.SKUs))
	selected := false
	for _, sku := range product.SKUs {
		if membershipTermLabel(v.actor.Locale, sku.MembershipTerm) == "" {
			continue
		}
		configured = append(configured, sku)
		selected = selected || sku.ID == v.state.Selection.SKUID
	}
	if !selected {
		v.state.Selection.SKUID = 0
	}
	if len(configured) == 1 {
		v.state.Selection.SKUID = configured[0].ID
	}
	v.panel().PhotoURL = product.Image
	v.html("<b>" + html.EscapeString(short(product.Title, 100)) + "</b>")
	v.say(text(v.actor.Locale, "membership_closed"))
	for _, name := range product.GroupNames {
		v.say(name)
	}
	v.say(text(v.actor.Locale, "membership_choose"))
	start := len(v.panel().Buttons)
	for _, sku := range configured {
		label := membershipTermLabel(v.actor.Locale, sku.MembershipTerm)
		if sku.ID == v.state.Selection.SKUID {
			v.styled(label, stylePrimary, action{Kind: "sku", ID: sku.ID})
		} else {
			v.button(label, action{Kind: "sku", ID: sku.ID})
		}
	}
	v.columns(2, start)
	v.button(text(v.actor.Locale, "product_details"), action{Kind: "details", Page: 1})
	v.navRow(text(v.actor.Locale, "back_list"), action{Kind: "products", ID: v.state.Category, Page: max(1, v.state.ProductPage)})
	return nil
}
