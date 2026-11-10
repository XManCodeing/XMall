package botapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	notifycontract "github.com/XManCodeing/XMall/internal/modules/telegram/notify/contract"
	"github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

type failureTransport func(*http.Request) (*http.Response, error)

// RoundTrip delegates req to the test transport and returns its synthetic response/error.
func (transport failureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

type failingResponseBody struct{ secret string }

// Read returns a synthetic secret-bearing failure without producing response data.
func (body failingResponseBody) Read([]byte) (int, error) { return 0, errors.New(body.secret) }

// Close satisfies io.ReadCloser without external resources.
func (failingResponseBody) Close() error { return nil }

// assertSafeNotifyError verifies all formatted and unwrap-able errors exclude the synthetic secret.
func assertSafeNotifyError(t *testing.T, err error, secret string) {
	t.Helper()
	if !errors.Is(err, notifycontract.ErrNotifySendFailed) {
		t.Fatalf("missing failure sentinel: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", err, err), secret) {
		t.Fatalf("secret exposed: %v", err)
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range many.Unwrap() {
			if strings.Contains(fmt.Sprintf("%+v %#v", cause, cause), secret) {
				t.Fatal("secret retained in error chain")
			}
		}
	}
}

// TestNotificationErrorsExcludeCredentials exercises HTTP, body-read, API and malformed responses for every send mode.
func TestNotificationErrorsExcludeCredentials(t *testing.T) {
	const token = "123:synthetic-private-token"
	attachmentPath := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(attachmentPath, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	modes := []string{"", "https://example.test/photo.png", "https://example.test/document.txt", "multipart"}
	cases := []struct {
		name    string
		status  int
		body    string
		failure string
	}{
		{"transport", 0, "", "transport"},
		{"read", 200, "", "read"},
		{"http", 500, token, ""},
		{"api", 200, `{"ok":false,"error_code":429,"description":"` + token + `","parameters":{"retry_after":9}}`, ""},
		{"malformed", 200, token, ""},
		{"oversized", 200, strings.Repeat("x", int(telegramapi.DefaultMaxResponseBytes)) + token, ""},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			for _, attachment := range modes {
				client := NewWithHTTPClient(&http.Client{Transport: failureTransport(func(req *http.Request) (*http.Response, error) {
					if scenario.failure == "transport" {
						return nil, errors.New(req.URL.String() + token)
					}
					var body io.ReadCloser = io.NopCloser(strings.NewReader(scenario.body))
					if scenario.failure == "read" {
						body = failingResponseBody{token}
					}
					return &http.Response{StatusCode: scenario.status, Body: body, Header: make(http.Header)}, nil
				})})
				options := notifycontract.SendOptions{ChatID: "1", Message: "test", AttachmentURL: attachment}
				var err error
				if attachment == "multipart" {
					err = client.sendMultipartMedia(context.Background(), token, "sendDocument", "document", attachmentPath, options)
				} else {
					err = client.SendWithBotToken(context.Background(), token, options)
				}
				assertSafeNotifyError(t, err, token)
				if scenario.name == "api" {
					var apiErr *telegramapi.APIError
					if !errors.As(err, &apiErr) || apiErr.ErrorCode != 429 || apiErr.RetryAfter != 9 {
						t.Fatalf("safe API diagnostic lost: %v", err)
					}
				}
			}
		})
	}
}

// TestNotificationLocalErrorsExcludeCredentials covers local IO, encoding and URL construction failures.
func TestNotificationLocalErrorsExcludeCredentials(t *testing.T) {
	const token = "synthetic-secret%invalid"
	client := New()
	options := notifycontract.SendOptions{ChatID: "1", Message: "test"}
	assertSafeNotifyError(t, client.sendJSONRequest(context.Background(), token, "sendMessage", map[string]interface{}{}), token)
	assertSafeNotifyError(t, client.sendJSONRequest(context.Background(), token, "sendMessage", map[string]interface{}{token: make(chan int)}), token)
	assertSafeNotifyError(t, client.sendMultipartMedia(context.Background(), token, "sendDocument", "document", filepath.Join(t.TempDir(), token), options), token)
	path := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	assertSafeNotifyError(t, client.sendMultipartMedia(context.Background(), token, "sendDocument", "document", path, options), token)
	assertSafeNotifyError(t, client.sendMultipartMedia(context.Background(), token, "sendDocument", "document", t.TempDir(), options), token)
}

// TestNotificationCancellationAndRedirectSafety preserves cancellation detection and refuses redirected credentials.
func TestNotificationCancellationAndRedirectSafety(t *testing.T) {
	const token = "synthetic-private-token"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewWithHTTPClient(&http.Client{Transport: failureTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New(token) })})
	err := client.SendWithBotToken(ctx, token, notifycontract.SendOptions{ChatID: "1", Message: "test"})
	assertSafeNotifyError(t, err, token)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	original := &http.Client{}
	cloned := NewWithHTTPClient(original)
	if original.CheckRedirect != nil || cloned.httpClient.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect protection missing or caller mutated")
	}
}
