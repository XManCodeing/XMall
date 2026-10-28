package generichttp

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/upstream"
	"github.com/shopspring/decimal"
)

func money(v any) (string, error) {
	var d decimal.Decimal
	if value, ok := v.(decimal.Decimal); ok {
		d = value
	} else {
		s, err := exactScalar(v)
		if err != nil || len(s) > 128 || strings.TrimSpace(s) != s {
			return "", errors.New("invalid monetary amount")
		}
		d, err = decimal.NewFromString(s)
		if err != nil {
			return "", errors.New("invalid monetary amount")
		}
	}
	if d.IsNegative() || d.Exponent() < -18 || d.Exponent() > 18 || len(d.Coefficient().String()) > 36 {
		return "", errors.New("monetary amount out of bounds")
	}
	return d.String(), nil
}
func localized(v any) (jsonmap.JSON, error) {
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("empty product title")
		}
		return jsonmap.JSON{"zh-CN": s, "zh-TW": s, "en-US": s}, nil
	}
	m, ok := object(v)
	if !ok || len(m) == 0 {
		return nil, errors.New("invalid localized text")
	}
	out := jsonmap.JSON{}
	for k, v := range m {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, errors.New("invalid localized text")
		}
		out[k] = s
	}
	return out, nil
}
func priceValue(m map[string]any) any {
	if v, ok := m["price_amount"]; ok {
		return v
	}
	return m["price"]
}
func product(m map[string]any) (*upstream.UpstreamProduct, error) {
	id, err := identifier(m["id"], false)
	if err != nil {
		return nil, err
	}
	title, err := localized(m["title"])
	if err != nil {
		return nil, err
	}
	curr, err := currency(m["currency"])
	if err != nil {
		return nil, err
	}
	active, err := boolean(m["is_active"])
	if err != nil {
		return nil, err
	}
	p := &upstream.UpstreamProduct{ExternalID: id, Title: title, Currency: curr, IsActive: active, FulfillmentType: "upstream"}
	for name, target := range map[string]*jsonmap.JSON{"description": &p.Description, "content": &p.Content} {
		if v, ok := m[name]; ok {
			if s, ok := v.(string); ok && s == "" {
				*target = jsonmap.JSON{}
				continue
			}
			*target, err = localized(v)
			if err != nil {
				return nil, err
			}
		}
	}
	if v, ok := m["images"]; ok {
		list, e := array(v)
		if e != nil {
			return nil, e
		}
		for _, item := range list {
			s, e := text(item)
			if e != nil {
				return nil, e
			}
			p.Images = append(p.Images, s)
		}
	}
	if v, ok := m["slug"]; ok {
		p.Slug, err = text(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := m["skus"]; ok {
		rows, e := array(v)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			return nil, errors.New("explicit SKU collection cannot be empty")
		}
		ids := map[string]bool{}
		codes := map[string]bool{}
		for _, row := range rows {
			r, ok := object(row)
			if !ok {
				return nil, errors.New("invalid SKU object")
			}
			sku, e := parseSKU(r, active, curr)
			if e != nil {
				return nil, e
			}
			if ids[sku.ExternalID] {
				return nil, errors.New("duplicate SKU ID")
			}
			ids[sku.ExternalID] = true
			if sku.SKUCode != "" {
				if codes[sku.SKUCode] {
					return nil, errors.New("duplicate SKU code")
				}
				codes[sku.SKUCode] = true
			}
			p.SKUs = append(p.SKUs, *sku)
		}
		// Hex is injective, preserves opaque bytes, and cannot contain the ':' suffix.
		// Reserve all supplied codes first; generation is stable under SKU reordering.
		for i := range p.SKUs {
			sku := &p.SKUs[i]
			if sku.SKUCode == "" {
				code := "generic:" + hex.EncodeToString([]byte(sku.ExternalID))
				for codes[code] {
					code += ":"
				}
				sku.SKUCode = code
				codes[code] = true
			}
		}
		if priceValue(m) != nil {
			p.PriceAmount, err = money(priceValue(m))
			if err != nil {
				return nil, err
			}
		} else {
			p.PriceAmount = p.SKUs[0].PriceAmount
			minimum, _ := decimal.NewFromString(p.PriceAmount)
			for _, sku := range p.SKUs[1:] {
				d, _ := decimal.NewFromString(sku.PriceAmount)
				if d.LessThan(minimum) {
					minimum = d
					p.PriceAmount = sku.PriceAmount
				}
			}
		}
	} else {
		sku, e := parseSKU(m, active, curr)
		if e != nil {
			return nil, e
		}
		sku.ExternalID = ""
		sku.ProductLevel = true
		sku.SKUCode = "default"
		p.SKUs = []upstream.UpstreamSKU{*sku}
		p.PriceAmount = sku.PriceAmount
	}
	return p, nil
}
func parseSKU(m map[string]any, parentActive bool, parentCurrency string) (*upstream.UpstreamSKU, error) {
	id, err := identifier(m["id"], false)
	if err != nil {
		return nil, err
	}
	price, err := money(priceValue(m))
	if err != nil {
		return nil, err
	}
	quantity, err := integer(m["stock_quantity"])
	if err != nil || quantity < -1 {
		return nil, errors.New("invalid stock quantity")
	}
	active := parentActive
	if v, ok := m["is_active"]; ok {
		active, err = boolean(v)
		if err != nil {
			return nil, err
		}
		active = active && parentActive
	}
	if v, ok := m["currency"]; ok {
		c, e := currency(v)
		if e != nil || c != parentCurrency {
			return nil, errors.New("SKU currency mismatch")
		}
	}
	status := "in_stock"
	if quantity == -1 {
		status = "unlimited"
	} else if quantity == 0 {
		status = "out_of_stock"
	}
	if v, ok := m["stock_status"]; ok {
		s, e := text(v)
		if e != nil {
			return nil, e
		}
		if s != status && !(s == "low_stock" && quantity > 0) {
			return nil, errors.New("invalid or inconsistent stock status")
		}
		status = s
	}
	sku := &upstream.UpstreamSKU{ExternalID: id, PriceAmount: price, StockQuantity: quantity, StockStatus: status, IsActive: active}
	if v, ok := m["sku_code"]; ok {
		sku.SKUCode, err = text(v)
		if err != nil {
			return nil, err
		}
	} else if v, ok := m["code"]; ok {
		sku.SKUCode, err = text(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := m["spec_values"]; ok {
		spec, ok := object(v)
		if !ok {
			return nil, errors.New("invalid SKU spec values")
		}
		sku.SpecValues = jsonmap.JSON(spec)
	}
	return sku, nil
}
func order(m map[string]any) (*upstream.UpstreamOrderDetail, error) {
	id, err := identifier(m["order_id"], false)
	if err != nil {
		return nil, err
	}
	status, err := orderStatus(m["status"])
	if err != nil {
		return nil, err
	}
	amount, err := money(m["amount"])
	if err != nil {
		return nil, err
	}
	curr, err := currency(m["currency"])
	if err != nil {
		return nil, err
	}
	out := &upstream.UpstreamOrderDetail{ExternalID: id, Status: status, Amount: amount, Currency: curr}
	if v, ok := m["order_no"]; ok {
		out.OrderNo, err = identifier(v, true)
		if err != nil {
			return nil, err
		}
	}
	out.Fulfillment, err = fulfillment(m, status)
	if err != nil {
		return nil, err
	}
	// A delivered status without usable delivery content must not trigger success.
	if (status == "delivered" || status == "completed") && (out.Fulfillment == nil || out.Fulfillment.Status != "delivered") {
		return nil, errors.New("delivered order has no usable fulfillment")
	}
	return out, nil
}
func fulfillment(m map[string]any, orderState string) (*upstream.UpstreamFulfillment, error) {
	source := m
	if v, ok := m["fulfillment"]; ok && v != nil {
		if s, ok := v.(string); ok {
			source = map[string]any{"payload": s}
		} else {
			var ok bool
			source, ok = object(v)
			if !ok {
				return nil, errors.New("invalid fulfillment object")
			}
		}
	}
	payload := ""
	if v, ok := source["payload"]; ok && v != nil {
		var err error
		payload, err = text(v)
		if err != nil {
			return nil, errors.New("invalid fulfillment payload")
		}
	}
	var delivery jsonmap.JSON
	if v, ok := source["cards"]; ok && v != nil {
		cards, err := array(v)
		if err != nil {
			return nil, err
		}
		parts := make([]string, 0, len(cards))
		for _, card := range cards {
			s, err := text(card)
			if err != nil || strings.TrimSpace(s) == "" {
				return nil, errors.New("cards must be nonempty strings")
			}
			parts = append(parts, s)
		}
		if len(parts) > 0 {
			cardPayload := strings.Join(parts, "\n")
			if payload != "" && payload != cardPayload {
				return nil, errors.New("conflicting fulfillment payloads")
			}
			payload = cardPayload
			delivery = jsonmap.JSON{"cards": cards}
		}
	}
	if strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	state := "pending"
	if orderState == "delivered" || orderState == "completed" || orderState == "partially_delivered" {
		state = "delivered"
	}
	// Nested fulfillment status is independent from the enclosing order status.
	if _, nested := m["fulfillment"]; nested {
		if v, ok := source["status"]; ok {
			s, err := text(v)
			if err != nil || (s != "pending" && s != "delivered") {
				return nil, errors.New("invalid fulfillment status")
			}
			state = s
		}
	}
	if state == "delivered" && (orderState == "canceled" || orderState == "refunded" || orderState == "pending_payment") {
		return nil, errors.New("inconsistent fulfillment status")
	}
	typ := "auto"
	if _, nested := m["fulfillment"]; nested {
		if v, ok := source["type"]; ok {
			s, err := text(v)
			if err != nil || (s != "auto" && s != "manual" && s != "upstream") {
				return nil, errors.New("invalid fulfillment type")
			}
			typ = s
		}
	}
	return &upstream.UpstreamFulfillment{Type: typ, Status: state, Payload: payload, DeliveryData: delivery}, nil
}
