package httpintegration

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxImageBytes = 2 << 20

// FetchPaymentImage accepts gateway-provided public HTTP(S) URLs only. It
// returns nil for a confirmed non-image response (a raw payment/checkout URL).
// Network failures are not treated as QR text: doing so could encode an image
// endpoint instead of the payment payload. Errors never include signed URLs.
func FetchPaymentImage(ctx context.Context, raw string) ([]byte, error) {
	transport := &http.Transport{Proxy: nil, DialContext: safeDial, DisableKeepAlives: true, DisableCompression: true, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	defer transport.CloseIdleConnections()
	return fetchPaymentImage(ctx, raw, transport)
}

func paymentImageURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Fragment != "" {
		return nil, errors.New("invalid payment image URL")
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		return nil, errors.New("unsupported payment image port")
	}
	return u, nil
}

func fetchPaymentImage(ctx context.Context, raw string, transport http.RoundTripper) ([]byte, error) {
	u, err := paymentImageURL(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 2 {
			return errors.New("payment image redirect limit")
		}
		next, err := paymentImageURL(req.URL.String())
		// The initial gateway URL is the allowed origin; redirects cannot
		// move to another host/port or downgrade transport security.
		if err != nil || next.Scheme != u.Scheme || !strings.EqualFold(next.Host, u.Host) {
			return errors.New("payment image redirect forbidden")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid payment image request")
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("payment image fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxImageBytes {
		return nil, errors.New("invalid payment image response")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil || len(body) > maxImageBytes {
		return nil, errors.New("payment image exceeds size limit")
	}
	declared, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	detected := http.DetectContentType(body)
	if !strings.HasPrefix(declared, "image/") && !strings.HasPrefix(detected, "image/") {
		path := strings.ToLower(u.Path)
		for _, suffix := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"} {
			if strings.HasSuffix(path, suffix) {
				return nil, errors.New("invalid payment image content")
			}
		}
		return nil, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
		return nil, errors.New("invalid payment image dimensions")
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid payment image data")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil || out.Len() > maxImageBytes {
		return nil, errors.New("payment image encoding failed")
	}
	return out.Bytes(), nil
}
