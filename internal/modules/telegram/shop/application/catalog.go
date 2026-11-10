package application

import (
	"context"
	"errors"
	"fmt"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"html"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
)

var productStartCode = regexp.MustCompile(`^[A-Za-z0-9]{12}$`)

// productStartID resolves one exact short code through the persisted mapping; no writes occur.
func productStartID(ctx context.Context, access c.ProductLinkAccess, payloads []string) (uint, error) {
	if len(payloads) != 1 || !productStartCode.MatchString(payloads[0]) {
		return 0, c.ErrDenied
	}
	if access == nil {
		return 0, c.ErrUnavailable
	}
	return access.ResolveProductLink(ctx, payloads[0])
}

// resetProductStart discards all purchase/history state while preserving identity and update fences.
// resolveAction already rotated Ref/PanelEpoch for the new command, invalidating old callbacks.
func (v *view) resetProductStart() {
	previous := v.state
	*previous = state{
		UserID: previous.UserID, Generation: previous.Generation, PublicGeneration: previous.PublicGeneration,
		IdentityFailure: previous.IdentityFailure, Locale: previous.Locale,
		Ref: previous.Ref, PanelEpoch: previous.PanelEpoch, PanelRevision: previous.PanelRevision,
		LastUpdate: previous.LastUpdate, LegacyInputThrough: previous.LegacyInputThrough,
		Current: action{Kind: "home"}, Actions: map[string]action{},
	}
}

// startProduct validates the payload and current bot relation before reusing the purchase panel.
// ctx carries cancellation; failures render a safe shop entry, never create accounts or orders.
func (v *view) startProduct(ctx context.Context, payloads []string) error {
	v.resetProductStart()
	productID, err := productStartID(ctx, v.service.d.ProductLinks, payloads)
	if err == nil {
		if v.service.d.ProductLinks == nil {
			err = c.ErrUnavailable
		} else {
			err = v.service.d.ProductLinks.AuthorizeProductLink(ctx, v.actor, productID)
		}
	}
	if err == nil {
		var product *c.Product
		product, err = v.service.d.Catalog.Product(ctx, v.actor, productID)
		if err == nil && (product == nil || product.ID != productID || !product.Membership || !product.Available || product.Unsupported) {
			err = c.ErrUnavailable
		}
	}
	if err == nil {
		v.state.LinkProductID = productID
		err = v.navigate(ctx, action{Kind: "product", ID: productID})
	}
	if err == nil {
		return nil
	}
	if !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrUnavailable) {
		slog.Error("telegram product deep link failed", "bot_id", v.actor.BotID, "product_id", productID)
		return err
	}
	// A partial render must not keep buttons or a quote from a failed product load.
	v.messages = nil
	v.resetProductStart()
	v.say(productLinkUnavailable(v.actor.Locale))
	v.button(text(v.actor.Locale, "catalog"), action{Kind: "catalog"})
	v.homeButton()
	return nil
}

// linkProduct reloads the current relation for deep-linked selection on every panel/checkout action.
// Ordinary catalog selections retain the existing catalog behavior; ctx and ID feed authoritative reads.
func (v *view) linkProduct(ctx context.Context, productID uint) (*c.Product, error) {
	if productID == v.state.LinkProductID && productID != 0 {
		if v.service.d.ProductLinks == nil {
			return nil, c.ErrUnavailable
		}
		if err := v.service.d.ProductLinks.AuthorizeProductLink(ctx, v.actor, productID); err != nil {
			return nil, err
		}
	}
	return v.service.d.Catalog.Product(ctx, v.actor, productID)
}

// productLinkUnavailable returns localized safe feedback, without leaking association details.
func productLinkUnavailable(language string) string {
	switch language {
	case "en-US":
		return "This product cannot be purchased here. Please return to the shop."
	case "zh-TW":
		return "此商品目前無法在此機器人購買，請返回商城。"
	default:
		return "此商品目前无法在此机器人购买，请返回商城。"
	}
}

func (v *view) catalog(ctx context.Context, a action) error {
	v.state.Step = ""
	v.state.OrderID = 0
	v.state.Current = a
	cats, e := v.service.d.Catalog.Categories(ctx, v.actor)
	if e != nil {
		return e
	}
	if a.Kind == "catalog" {
		if len(cats) > 0 && len(cats) <= 6 {
			return v.catalog(ctx, action{Kind: "products", ID: cats[0].ID, Page: 1, Value: "shortcut"})
		}
		if a.Page < 1 {
			a.Page = 1
		}
		v.html("<b>" + html.EscapeString(fmt.Sprintf(text(v.actor.Locale, "catalog_page"), text(v.actor.Locale, "catalog_all"), a.Page)) + "</b>")
		start := min((a.Page-1)*8, len(cats))
		end := min(start+8, len(cats))
		for _, cat := range cats[start:end] {
			v.button(cat.Name, action{Kind: "products", ID: cat.ID, Page: 1, Value: "shortcut"})
		}
		v.columns(2, 0)
		v.pages(a, end < len(cats))
		v.homeButton()
		return nil
	}
	if a.Page < 1 {
		a.Page = 1
	}
	v.state.Category = a.ID
	v.state.ProductPage = a.Page
	products, more, e := v.service.d.Catalog.Products(ctx, v.actor, a.ID, a.Page)
	if e != nil {
		return e
	}
	if len(products) == 1 && !more && a.Page == 1 && a.Value == "shortcut" {
		return v.catalogAction(ctx, action{Kind: "product", ID: products[0].ID})
	}
	title := text(v.actor.Locale, "catalog_all")
	for _, cat := range cats {
		if cat.ID == a.ID {
			title = cat.Name
			break
		}
	}
	v.html("<b>" + html.EscapeString(fmt.Sprintf(text(v.actor.Locale, "catalog_page"), title, a.Page)) + "</b>")
	tabs := len(cats) > 1 && len(cats) <= 6
	if tabs {
		for _, cat := range cats {
			if cat.ID == a.ID {
				v.styled("✓ "+cat.Name, stylePrimary, action{Kind: "label", Value: fmt.Sprintf(text(v.actor.Locale, "category_current"), cat.Name)})
				continue
			}
			v.button(cat.Name, action{Kind: "products", ID: cat.ID, Page: 1})
		}
		v.columns(3, 0)
	}
	if len(products) == 0 {
		v.say(text(v.actor.Locale, "catalog_empty"))
	} else {
		if tabs {
			v.section(text(v.actor.Locale, "section_products"), text(v.actor.Locale, "section_products_hint"))
		} else {
			v.say(text(v.actor.Locale, "section_products_hint"))
		}
		for _, p := range products {
			label := short(p.Title, 28) + " · " + money(p.Currency, p.Price)
			if !p.Available {
				label = short(p.Title, 28) + " · " + text(v.actor.Locale, "sold_out")
			}
			v.button(label, action{Kind: "product", ID: p.ID})
		}
	}
	a.Value = ""
	v.pages(a, more)
	v.homeButton()
	return nil
}
func (v *view) catalogAction(ctx context.Context, a action) error {
	switch a.Kind {
	case "delta", "quantity", "more_quantity", "quantity_input", "noop":
		product, err := v.linkProduct(ctx, v.state.Selection.ProductID)
		if err != nil {
			return err
		}
		if product == nil {
			return c.ErrDenied
		}
		if product.Membership && !product.Available {
			return v.membershipPurchase(product)
		}
	}
	switch a.Kind {
	case "product":
		p, e := v.linkProduct(ctx, a.ID)
		if e != nil {
			return e
		}
		if p == nil {
			return c.ErrDenied
		}
		v.state.Selection = c.Selection{ProductID: p.ID, Quantity: max(1, p.Min)}
		v.state.Quote = nil
		v.state.Operation = ""
		v.state.MoreQuantity = false
		v.state.PaymentUncertain = false
		available := []c.SKU{}
		for _, sku := range p.SKUs {
			if sku.Available {
				available = append(available, sku)
			}
		}
		if len(available) == 1 {
			v.state.Selection.SKUID = available[0].ID
		}
		return v.purchase(ctx, true)
	case "sku":
		p, e := v.linkProduct(ctx, v.state.Selection.ProductID)
		if e != nil {
			return e
		}
		if p == nil {
			return c.ErrDenied
		}
		if p.Membership && !p.Available {
			return v.selectMembershipTerm(p, a.ID)
		}
		if p.Unsupported {
			return c.ErrDenied
		}
		for _, sku := range p.SKUs {
			if sku.ID == a.ID {
				if !sku.Available {
					rt.AnswerCallback(ctx, text(v.actor.Locale, "sold_out"))
					return v.purchase(ctx, false)
				}
				v.state.Selection.SKUID = a.ID
				v.state.Selection.Quantity = max(1, p.Min)
				rt.AnswerCallback(ctx, fmt.Sprintf(text(v.actor.Locale, "selected"), sku.Name))
				return v.purchase(ctx, true)
			}
		}
		return c.ErrDenied
	case "delta":
		n, e := strconv.Atoi(a.Value)
		if e != nil || (n != 1 && n != -1 && n != 10 && n != -10 && n != 100 && n != -100 && n != 1000 && n != -1000) {
			return c.ErrDenied
		}
		return v.changeQuantity(ctx, v.state.Selection.Quantity+n)
	case "quantity":
		n, e := strconv.Atoi(a.Value)
		if e != nil {
			return c.ErrDenied
		}
		return v.changeQuantity(ctx, n)
	case "more_quantity":
		v.state.MoreQuantity = a.Value != "back"
		return v.purchase(ctx, false)
	case "quantity_input":
		// Compatibility for old buttons: restore click-only quantity controls.
		v.state.Step = ""
		v.state.MoreQuantity = true
		return v.purchase(ctx, false)
	case "details":
		return v.productDetails(ctx, a.Page)
	case "purchase":
		v.state.Step = ""
		return v.purchase(ctx, false)
	case "noop":
		rt.AnswerCallback(ctx, fmt.Sprintf(text(v.actor.Locale, "quantity_selected"), v.state.Selection.Quantity))
		return v.purchase(ctx, false)
	}
	return c.ErrDenied
}
func quantityBounds(p *c.Product, sku uint) (int, int, bool) {
	lo := max(1, p.Min)
	hi := 10000
	if p.Max > 0 {
		hi = min(hi, p.Max)
	}
	if p.MaxQuantity != nil {
		hi = min(hi, *p.MaxQuantity)
	}
	available := p.Available && !p.Unsupported
	if len(p.SKUs) > 0 {
		found := false
		for _, s := range p.SKUs {
			if s.ID == sku {
				found = true
				available = available && s.Available
				if s.MaxQuantity != nil {
					hi = min(hi, *s.MaxQuantity)
				}
			}
		}
		available = available && found
	}
	return lo, hi, available && lo <= hi
}
func quantityTiers(lo, hi int, more bool) []int {
	seeds := []int{lo, 2, 5, 10}
	if more {
		seeds = []int{20, 30, 50, 100}
	}
	seen := map[int]bool{}
	var out []int
	for _, n := range seeds {
		if n >= lo && n <= hi && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}
func (v *view) changeQuantity(ctx context.Context, n int) error {
	p, e := v.linkProduct(ctx, v.state.Selection.ProductID)
	if e != nil {
		return e
	}
	if p == nil {
		return c.ErrDenied
	}
	if p.Membership && !p.Available {
		return v.membershipPurchase(p)
	}
	lo, hi, ok := quantityBounds(p, v.state.Selection.SKUID)
	if !ok || n < lo || n > hi {
		rt.AnswerCallback(ctx, text(v.actor.Locale, "quantity_limit"))
		return v.purchase(ctx, false)
	}
	v.state.Selection.Quantity = n
	v.state.Step = ""
	rt.AnswerCallback(ctx, fmt.Sprintf(text(v.actor.Locale, "quantity_selected"), n))
	return v.purchase(ctx, true)
}
func (v *view) purchase(ctx context.Context, reprice bool) error {
	p, e := v.linkProduct(ctx, v.state.Selection.ProductID)
	if e != nil {
		return e
	}
	if p == nil {
		return c.ErrDenied
	}
	if p.Membership && !p.Available {
		return v.membershipPurchase(p)
	}
	v.state.Current = action{Kind: "purchase"}
	v.state.OrderID = 0
	lo, hi, buyable := quantityBounds(p, v.state.Selection.SKUID)
	boundsOK := buyable
	n := v.state.Selection.Quantity
	buyable = buyable && n >= lo && n <= hi
	if reprice {
		v.state.Quote = nil
		v.state.Operation = ""
		if buyable && v.actor.UserID != 0 {
			q, e := v.service.d.Orders.Preview(ctx, v.actor, v.state.Selection)
			if e != nil {
				v.say(text(v.actor.Locale, "preview_failed"))
			} else if q != nil {
				v.state.Quote = q
				op, e := nonce()
				if e != nil {
					return e
				}
				v.state.Operation = fmt.Sprintf("tg:%d:%d:%s", v.actor.BotID, v.actor.UserID, op)
			}
		}
	}
	v.purchaseHeader(p)
	if p.Unsupported {
		v.say(text(v.actor.Locale, "unsupported"))
		v.urlButton(text(v.actor.Locale, "open_product"), p.URL)
		v.button(text(v.actor.Locale, "product_details"), action{Kind: "details", Page: 1})
	} else {
		v.purchaseSKUs(p, n)
		more := v.purchaseQuantities(lo, hi, n, boundsOK)
		v.purchasePrice(p, buyable)
		start := len(v.panel().Buttons)
		if more {
			v.button(text(v.actor.Locale, "more_quantity"), action{Kind: "more_quantity"})
		}
		v.button(text(v.actor.Locale, "product_details"), action{Kind: "details", Page: 1})
		v.columns(2, start)
	}
	v.navRow(text(v.actor.Locale, "back_list"), action{Kind: "products", ID: v.state.Category, Page: max(1, v.state.ProductPage)})
	return nil
}

// purchaseHeader renders public product metadata without reading account data.
func (v *view) purchaseHeader(p *c.Product) {
	v.panel().PhotoURL = p.Image
	v.html("<b>" + html.EscapeString(short(p.Title, 100)) + "</b>")
	meta := ""
	if p.FulfillmentType != "" {
		key := "fulfillment_manual"
		if p.FulfillmentType == "auto" || p.FulfillmentType == "automatic" {
			key = "fulfillment_auto"
		}
		meta = text(v.actor.Locale, key)
	}
	if p.Stock != "" {
		stock := short(p.Stock, 80)
		if meta != "" {
			meta += " · " + stock
		} else {
			meta = stock
		}
	}
	if meta != "" {
		v.say(meta)
	}
	summary := p.Summary
	if summary == "" {
		summary = p.Description
	}
	if summary != "" {
		v.say(short(summary, 180))
	}
}

// purchaseSKUs renders the public selection and available SKU buttons.
func (v *view) purchaseSKUs(p *c.Product, n int) {
	skuName := ""
	available := 0
	for _, sku := range p.SKUs {
		if sku.Available {
			available++
		}
		if sku.ID == v.state.Selection.SKUID {
			skuName = sku.Name
		}
	}
	if len(p.SKUs) > 0 && v.state.Selection.SKUID == 0 {
		v.say(text(v.actor.Locale, "choose_sku"))
	} else if skuName != "" {
		v.say(fmt.Sprintf(text(v.actor.Locale, "selected_line"), short(skuName, 60), n))
	} else {
		v.say(fmt.Sprintf(text(v.actor.Locale, "quantity_selected"), n))
	}
	if len(p.SKUs) > 1 || available != 1 {
		start := len(v.panel().Buttons)
		for _, sku := range p.SKUs {
			label := sku.Name + " · " + money(p.Currency, sku.Price)
			if !sku.Available {
				label = sku.Name + " · " + text(v.actor.Locale, "sold_out")
			}
			if sku.ID == v.state.Selection.SKUID {
				v.styled("✓ "+label, stylePrimary, action{Kind: "sku", ID: sku.ID})
				continue
			}
			v.button(label, action{Kind: "sku", ID: sku.ID})
		}
		v.columns(2, start)
	}
}

// purchaseQuantities returns whether the secondary quantity menu is available.
func (v *view) purchaseQuantities(lo, hi, n int, boundsOK bool) bool {
	more := false
	if boundsOK && lo < hi {
		start := len(v.panel().Buttons)
		v.button("−", action{Kind: "delta", Value: "-1"})
		v.button(fmt.Sprintf(text(v.actor.Locale, "quantity_display"), n), action{Kind: "label", Value: fmt.Sprintf(text(v.actor.Locale, "quantity_selected"), n)})
		v.button("+", action{Kind: "delta", Value: "1"})
		v.columns(3, start)
		start = len(v.panel().Buttons)
		for _, t := range quantityTiers(lo, hi, v.state.MoreQuantity) {
			if t == n {
				v.styled(strconv.Itoa(t), stylePrimary, action{Kind: "label", Value: fmt.Sprintf(text(v.actor.Locale, "quantity_selected"), n)})
				continue
			}
			v.button(strconv.Itoa(t), action{Kind: "quantity", Value: strconv.Itoa(t)})
		}
		v.columns(4, start)
		if v.state.MoreQuantity {
			for _, step := range []int{10, 100, 1000} {
				if hi-lo < step {
					continue
				}
				start = len(v.panel().Buttons)
				v.button("−"+strconv.Itoa(step), action{Kind: "delta", Value: strconv.Itoa(-step)})
				v.button("+"+strconv.Itoa(step), action{Kind: "delta", Value: strconv.Itoa(step)})
				v.columns(2, start)
			}
			v.button(text(v.actor.Locale, "back_purchase"), action{Kind: "more_quantity", Value: "back"})
		} else {
			more = true
		}
	}
	return more
}

// purchasePrice distinguishes public unit prices from an authorized private quote.
func (v *view) purchasePrice(p *c.Product, buyable bool) {
	if v.actor.UserID == 0 {
		v.state.Quote, v.state.Operation = nil, ""
		price := p.Price
		for _, sku := range p.SKUs {
			if sku.ID == v.state.Selection.SKUID {
				price = sku.Price
			}
		}
		v.say(text(v.actor.Locale, "public_price") + " · " + money(p.Currency, price))
		if buyable {
			v.styled(text(v.actor.Locale, "continue_checkout"), styleSuccess, action{Kind: "continue_checkout"})
		}
	}
	if v.state.Quote != nil && buyable {
		q := v.state.Quote
		line := html.EscapeString(text(v.actor.Locale, "payable")) + " <b>" + html.EscapeString(money(q.Currency, q.Amount)) + "</b>"
		if q.Discounted {
			line += "（" + html.EscapeString(text(v.actor.Locale, "discounted")) + "）"
		}
		v.html(line)
		v.styled(text(v.actor.Locale, "confirm")+" · "+money(q.Currency, q.Amount), styleSuccess, action{Kind: "confirm"})
	} else if !buyable && v.state.Selection.SKUID != 0 || !p.Available {
		v.say(text(v.actor.Locale, "sold_out"))
	}
}
func (v *view) quote(ctx context.Context) error { return v.purchase(ctx, true) }
func (v *view) productDetails(ctx context.Context, page int) error {
	p, e := v.linkProduct(ctx, v.state.Selection.ProductID)
	if e != nil {
		return e
	}
	if p == nil {
		return c.ErrDenied
	}
	if page < 1 {
		page = 1
	}
	v.state.Current = action{Kind: "details", Page: page}
	// Slice runes before escaping. Each page has a conservative UTF-16 budget.
	r := []rune(p.Description)
	const size = 1500
	pages := max(1, (len(r)+size-1)/size)
	page = min(page, pages)
	start := min((page-1)*size, len(r))
	end := min(start+size, len(r))
	v.html("<b>" + html.EscapeString(short(p.Title, 100)) + "</b>")
	v.say(string(r[start:end]))
	v.pages(action{Kind: "details", Page: page}, end < len(r))
	v.homeButton()
	return nil
}
func (v *view) confirm(ctx context.Context) error {
	product, err := v.linkProduct(ctx, v.state.Selection.ProductID)
	if err != nil {
		return err
	}
	if product == nil {
		return c.ErrDenied
	}
	if product.Membership && !product.Available {
		return v.membershipPurchase(product)
	}
	if v.state.Quote == nil || v.state.Operation == "" {
		return c.ErrDenied
	}
	rt.AnswerCallback(ctx, text(v.actor.Locale, "processing"))
	o, e := v.service.d.Orders.Create(ctx, v.actor, v.state.Selection, *v.state.Quote, v.state.Operation)
	if errors.Is(e, c.ErrPriceChanged) {
		v.say(text(v.actor.Locale, "price_changed"))
		return v.purchase(ctx, true)
	}
	if e != nil {
		if !errors.Is(e, c.ErrUnavailable) && !errors.Is(e, c.ErrDenied) {
			return e
		}
		v.say(text(v.actor.Locale, "failed"))
		return v.purchase(ctx, false)
	}
	if o == nil || o.UserID != v.actor.UserID {
		return c.ErrDenied
	}
	v.state.Quote = nil
	v.state.Step = ""
	v.state.Payment = nil
	v.state.PaymentUncertain = false
	return v.channels(ctx, o.ID, true)
}
