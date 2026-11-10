package application

import (
	"context"
	"errors"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

const navigationLimit = 16

// pageState is a bounded read-only location, never a replayable business action.
// Selection and list context are retained; payment keys, quotes and credentials are not.
type pageState struct {
	Kind         string
	ID, Owner    uint
	Page         int
	Value        string
	Selection    c.Selection
	Category     uint
	ProductPage  int
	Filter       string
	MoreQuantity bool
}

// currentPage projects legacy Current into the explicit read-only page vocabulary.
// Unknown or mutating locations fall back to home instead of invoking perform.
func (v *view) currentPage() pageState {
	st := v.state
	current := st.Current
	page := pageState{Kind: current.Kind, ID: current.ID, Owner: current.Owner, Page: current.Page, Value: current.Value,
		Selection: st.Selection, Category: st.Category, ProductPage: st.ProductPage, Filter: st.Filter, MoreQuantity: st.MoreQuantity}
	switch page.Kind {
	case "home", "catalog", "products", "purchase", "details", "orders", "history_orders", "order", "memberships", "membership", "help", "account", "bind", "history", "history_account", "history_refunds", "language", "channels":
	case "pay":
		page.Kind, page.ID, page.Owner = "payment", st.OrderID, st.OrderOwner
	case "cancel_prompt":
		page.ID, page.Owner = st.OrderID, st.OrderOwner
	default:
		page = pageState{Kind: "home"}
	}
	return page
}

// navigate runs explicit user intent once, then records only its read-only result.
// Pagination, filters and refresh replace the current location; home clears history.
func (v *view) navigate(ctx context.Context, act action) error {
	if act.Kind == "back" {
		return v.goBack(ctx)
	}
	before := v.currentPage()
	if err := v.perform(ctx, act); err != nil {
		return err
	}
	after := v.currentPage()
	if after.Kind == "home" {
		v.state.Pages = nil
		return nil
	}
	if act.Kind == "language" || act.Kind == "locale" || act.Kind == "label" || act.Kind == "check_payment" || act.Kind == "cancel_confirm" || act.Kind == "resend_delivery" || samePage(before, after) {
		return nil
	}
	v.state.Pages = append(v.state.Pages, before)
	if len(v.state.Pages) > navigationLimit {
		v.state.Pages = v.state.Pages[len(v.state.Pages)-navigationLimit:]
	}
	return nil
}

// samePage identifies in-place state changes without losing the latest list context.
func samePage(previous, next pageState) bool {
	if previous.Kind != next.Kind {
		return false
	}
	switch next.Kind {
	case "catalog", "products", "orders", "history_orders", "memberships", "details", "purchase":
		return previous.Owner == next.Owner && (next.Kind != "purchase" || previous.Selection.ProductID == next.Selection.ProductID)
	default:
		return previous.ID == next.ID && previous.Owner == next.Owner && previous.Value == next.Value
	}
}

// goBack pops one read-only location; the language overlay does not consume history.
// ctx is used for fresh authorized reads. No business action is replayed.
func (v *view) goBack(ctx context.Context) error {
	if v.state.Current.Kind == "language" && v.state.ReturnView != nil {
		page := *v.state.ReturnView
		v.state.ReturnView = nil
		return v.renderPage(ctx, page)
	}
	if len(v.state.Pages) == 0 {
		return v.home(ctx)
	}
	last := len(v.state.Pages) - 1
	page := v.state.Pages[last]
	v.state.Pages = v.state.Pages[:last]
	return v.renderPage(ctx, page)
}

// renderPage restores context through a closed read-only dispatcher, not perform.
// Every private read revalidates ownership; guest restoration never provisions accounts.
func (v *view) renderPage(ctx context.Context, page pageState) (result error) {
	// Stale/deauthorized pages fall back centrally; transient infrastructure errors remain visible.
	defer func() {
		if page.Kind != "home" && (errors.Is(result, c.ErrDenied) || errors.Is(result, c.ErrUnavailable)) {
			v.messages = nil
			v.state.Pages, v.state.ReturnView = nil, nil
			result = v.home(ctx)
		}
	}()
	st := v.state
	if st.Selection != page.Selection {
		st.Quote, st.Operation = nil, ""
	}
	st.Selection, st.Category, st.ProductPage = page.Selection, page.Category, page.ProductPage
	st.Filter, st.MoreQuantity, st.Step = page.Filter, page.MoreQuantity, ""
	if v.actor.UserID == 0 {
		switch page.Kind {
		case "home", "catalog", "products", "purchase", "details", "help", "bind", "language":
		default:
			return v.home(ctx)
		}
	}
	location := action{Kind: page.Kind, ID: page.ID, Owner: page.Owner, Page: page.Page, Value: page.Value}
	switch page.Kind {
	case "home":
		return v.home(ctx)
	case "catalog", "products":
		location.Value = "" // Back must not re-enter automatic single-product shortcuts.
		return v.catalog(ctx, location)
	case "purchase":
		return v.purchase(ctx, st.Quote == nil)
	case "details":
		return v.productDetails(ctx, page.Page)
	case "orders", "history_orders":
		st.Current = location
		return v.orders(ctx, chooseOwner(page.Owner, v.actor.UserID), page.Page)
	case "channels":
		return v.channels(ctx, page.ID, false)
	case "payment", "cancel_prompt":
		return v.order(ctx, page.ID, chooseOwner(page.Owner, v.actor.UserID))
	case "order":
		return v.order(ctx, page.ID, page.Owner)
	case "memberships":
		return v.memberships(ctx, location)
	case "membership":
		return v.existingMembership(ctx, location)
	case "help":
		return v.help(location)
	case "account":
		return v.accountPanel(ctx, location)
	case "history", "history_account", "history_refunds":
		return v.history(ctx, location)
	case "bind", "language":
		return v.performSettings(ctx, location)
	default:
		return v.home(ctx)
	}
}
