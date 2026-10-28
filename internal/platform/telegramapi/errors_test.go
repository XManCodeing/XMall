package telegramapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIStatusClassificationAndNoRetries(t *testing.T) {
	for _, code := range []int{401, 403, 409, 429, 599} {
		for _, httpStatus := range []int{http.StatusOK, code} {
			t.Run(fmt.Sprintf("HTTP%d/code%d", httpStatus, code), func(t *testing.T) {
				var calls atomic.Int32
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(httpStatus)
					fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"dummy_secret https://private.invalid sensitive message","parameters":{"retry_after":23}}`, code)
				})
				message, err := client.SendMessage(context.Background(), SendMessageRequest{ChatID: 42, Text: "private message"})
				var api *APIError
				if !errors.As(err, &api) {
					t.Fatalf("expected APIError, got %T: %v", err, err)
				}
				if api.HTTPStatus != httpStatus || api.ErrorCode != code || api.RetryAfter != 23 {
					t.Fatalf("incorrect classification: %+v", api)
				}
				if message.MessageID != 0 || calls.Load() != 1 {
					t.Fatalf("failed send retried or returned success: id=%d calls=%d", message.MessageID, calls.Load())
				}
				for _, secret := range []string{"dummy_secret", "https://", "sensitive message", "private message"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error leaked %q", secret)
					}
				}
			})
		}
	}
}

func TestResponseBodyDeadline(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"ok":true,"result":`)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}, WithTimeout(40*time.Millisecond))
	_, err := client.GetMe(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response body was not deadline bounded: %v", err)
	}
}

func TestCallerDeadlineWins(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}, WithTimeout(5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.GetMe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("caller deadline not respected: %v", err)
	}
}

type syntheticTimeout struct{}

func (syntheticTimeout) Error() string { return "https://private.invalid/bot123:dummy_secret" }
func (syntheticTimeout) Timeout() bool { return true }

func TestUnderlyingTransportTimeoutIsRedacted(t *testing.T) {
	client, err := NewClient("123:dummy_secret", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, syntheticTimeout{}
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetMe(context.Background())
	var transport *TransportError
	if !errors.As(err, &transport) || !transport.Timeout {
		t.Fatalf("timeout classification lost: %v", err)
	}
	if strings.Contains(err.Error(), "dummy_secret") || strings.Contains(err.Error(), "https://") {
		t.Fatal("transport error leaked URL/token")
	}
}

func TestLongPollTimeoutValidation(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}, WithTimeout(2*time.Second))
	for _, request := range []GetUpdatesRequest{{Timeout: -1}, {Timeout: 51}, {Timeout: 2}, {Timeout: 3}, {Limit: -1}, {Limit: 101}} {
		if _, err := client.GetUpdates(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid polling parameters: %+v", request)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid polling request reached server")
	}
	if _, err := client.GetUpdates(context.Background(), GetUpdatesRequest{Timeout: 1, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("valid polling request was not sent exactly once")
	}
}
