package application

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/url"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
)

// PaymentMedia performs no network I/O. QRImage is fetched and validated by
// the infrastructure adapter; raw QR payloads are encoded locally.
func PaymentMedia(p c.Payment) ([]byte, string, error) {
	link := ""
	u, e := url.Parse(strings.TrimSpace(p.PayURL))
	if e == nil && u.User == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		link = u.String()
	}
	if p.InteractionMode == constants.PaymentInteractionRedirect {
		if link == "" {
			return nil, "", c.ErrUnavailable
		}
		return nil, link, nil
	}
	if len(p.QRImage) > 0 {
		cfg, err := png.DecodeConfig(bytes.NewReader(p.QRImage))
		if err != nil || len(p.QRImage) > 2*1024*1024 || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, link, c.ErrUnavailable
		}
		return p.QRImage, link, nil
	}
	raw := strings.TrimSpace(p.QRCode)
	if p.InteractionMode == constants.PaymentInteractionQR && raw == "" {
		// Some gateways put their raw QR payload in PayURL rather than QRCode.
		raw = strings.TrimSpace(p.PayURL)
	}
	if p.QRCodeKind == "image" && !strings.HasPrefix(raw, "data:image/png;base64,") && len(p.QRImage) == 0 {
		return nil, link, c.ErrUnavailable
	}
	if strings.HasPrefix(raw, "data:image/png;base64,") {
		if len(raw) > 2*1024*1024 {
			return nil, link, c.ErrUnavailable
		}
		b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "data:image/png;base64,"))
		if e != nil {
			return nil, link, c.ErrUnavailable
		}
		cfg, e := png.DecodeConfig(bytes.NewReader(b))
		if e != nil || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, link, c.ErrUnavailable
		}
		return b, link, nil
	}
	if raw == "" && link == "" {
		raw = strings.TrimSpace(p.PayURL)
	}
	if raw == "" {
		return nil, link, nil
	}
	parsed, e := url.Parse(raw)
	if len(raw) > 2048 {
		return nil, link, c.ErrUnavailable
	}
	// Only known payment URI schemes are accepted when PayURL contains URLScheme.
	if p.QRCode == "" && p.InteractionMode != constants.PaymentInteractionQR {
		if e != nil || parsed == nil || (parsed.Scheme != "alipays" && parsed.Scheme != "alipay" && parsed.Scheme != "weixin") {
			return nil, link, c.ErrUnavailable
		}
	}
	code, e := qr.Encode(raw, qr.M, qr.Auto)
	if e != nil {
		return nil, link, e
	}
	img, e := barcode.Scale(code, 320, 320)
	if e != nil {
		return nil, link, e
	}
	var b bytes.Buffer
	if e = png.Encode(&b, img); e != nil {
		return nil, link, e
	}
	return b.Bytes(), link, nil
}
func (v *view) payment(p *c.Payment, orderID uint) error {
	if p == nil {
		return c.ErrUnavailable
	}
	if p.Paid {
		v.say(p.OrderNumber + "\n" + text(v.actor.Locale, "paid"))
	} else {
		if p.ExpiresAt != nil && !p.ExpiresAt.After(v.service.now()) {
			return c.ErrUnavailable
		}
		photo, link, e := PaymentMedia(*p)
		if e != nil {
			return e
		}
		if len(photo) == 0 && link == "" {
			return c.ErrUnavailable
		}
		v.say(p.OrderNumber + "\n" + p.Amount + " " + p.Currency)
		i := len(v.messages) - 1
		v.messages[i].Photo = photo
		if link != "" {
			v.messages[i].Buttons = append(v.messages[i].Buttons, []rt.Button{{Text: text(v.actor.Locale, "pay"), URL: link}})
		}
		if p.ExpiresAt != nil {
			v.messages[i].Text += "\n" + text(v.actor.Locale, "expires") + p.ExpiresAt.Format(time.RFC3339)
		}
	}
	v.button(text(v.actor.Locale, "refresh"), action{Kind: "order", ID: orderID, Owner: v.actor.UserID})
	v.homeButton()
	return nil
}
