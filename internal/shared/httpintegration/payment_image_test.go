package httpintegration

import (
	"bytes"
	"context"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPaymentImageFetch(t *testing.T) {
	var pngBody, jpgBody, gifBody bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	_ = png.Encode(&pngBody, img)
	_ = jpeg.Encode(&jpgBody, img, nil)
	_ = gif.Encode(&gifBody, img, nil)
	for _, tt := range []struct {
		name, url, contentType string
		body                   []byte
		wantImage, wantErr     bool
	}{
		{"extensionless PNG", "https://pay.example/qr?id=1", "image/png", pngBody.Bytes(), true, false},
		{"sniffed PNG", "https://pay.example/qr", "application/octet-stream", pngBody.Bytes(), true, false},
		{"GIF", "https://pay.example/qr", "image/gif", gifBody.Bytes(), true, false},
		{"JPEG", "https://pay.example/qr", "image/jpeg", jpgBody.Bytes(), true, false},
		{"checkout", "https://pay.example/pay", "text/html", []byte("<html>checkout</html>"), false, false},
		{"corrupt", "https://pay.example/qr", "image/png", []byte("bad"), false, true},
		{"false image", "https://pay.example/qr.png", "text/html", []byte("<html>bad</html>"), false, true},
		{"SVG", "https://pay.example/qr", "image/svg+xml", []byte("<svg/>"), false, true},
		{"large", "https://pay.example/qr", "image/png", bytes.Repeat([]byte("x"), maxImageBytes+1), false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := fetchPaymentImage(context.Background(), tt.url, imageTransport(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing timeout")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{tt.contentType}}, Body: io.NopCloser(bytes.NewReader(tt.body))}, nil
			}))
			if (err != nil) != tt.wantErr || (len(body) > 0) != tt.wantImage {
				t.Fatalf("image=%d err=%v", len(body), err)
			}
			if tt.wantImage {
				if _, err := png.Decode(bytes.NewReader(body)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPaymentImageRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/qr", "http://169.254.169.254/qr", "http://[::1]/qr", "https://user:pass@pay.example/qr", "file:///qr", "https://pay.example:8443/qr"} {
		if _, err := FetchPaymentImage(context.Background(), raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPaymentImageBoundsAndCancellation(t *testing.T) {
	var largeDimensions bytes.Buffer
	_ = png.Encode(&largeDimensions, image.NewGray(image.Rect(0, 0, 2049, 1)))
	for _, tt := range []struct {
		status int
		length int64
		body   []byte
	}{
		{503, 0, nil}, {200, maxImageBytes + 1, nil}, {200, 0, largeDimensions.Bytes()},
	} {
		_, err := fetchPaymentImage(context.Background(), "https://pay.example/qr", imageTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tt.status, ContentLength: tt.length, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(tt.body))}, nil
		}))
		if err == nil {
			t.Fatal("accepted invalid image response")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchPaymentImage(ctx, "https://pay.example/qr", imageTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() }))
	if err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestPaymentImageSameOriginRedirect(t *testing.T) {
	var body bytes.Buffer
	_ = png.Encode(&body, image.NewGray(image.Rect(0, 0, 2, 2)))
	calls := 0
	photo, err := fetchPaymentImage(context.Background(), "https://pay.example/start", imageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"/image"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(body.Bytes()))}, nil
	}))
	if err != nil || len(photo) == 0 || calls != 2 {
		t.Fatalf("calls=%d photo=%d err=%v", calls, len(photo), err)
	}
}

func TestPaymentImageRedirectPolicy(t *testing.T) {
	for _, target := range []string{"https://other.example/qr", "http://pay.example/qr", "https://127.0.0.1/qr", "https://pay.example/loop"} {
		calls := 0
		_, err := fetchPaymentImage(context.Background(), "https://pay.example/start", imageTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}))
		if err == nil || (target != "https://pay.example/loop" && calls != 1) || calls > 3 {
			t.Fatalf("target=%s calls=%d err=%v", target, calls, err)
		}
	}
}
