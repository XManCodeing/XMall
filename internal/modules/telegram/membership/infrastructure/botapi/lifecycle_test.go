package botapi

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// BanChatMember records one synthetic removal without touching Telegram.
func (api *accessAPIStub) BanChatMember(ctx context.Context, token string, chatID, userID int64) error {
	return api.record(ctx, "ban", token, chatID, userID)
}

// UnbanChatMember records one synthetic safe restoration without touching Telegram.
func (api *accessAPIStub) UnbanChatMember(ctx context.Context, token string, chatID, userID int64) error {
	return api.record(ctx, "unban", token, chatID, userID)
}

// TestLifecycleAdapterOutcomes covers unknown timeouts, 429 backoff and fail-closed runtime modes.
func TestLifecycleAdapterOutcomes(t *testing.T) {
	for _, scenario := range []string{"success", "timeout", "rate", "external", "missing", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			access, ctx, api, route := accessFixture()
			access.Lifecycle = api
			want, calls := c.AccessSucceeded, 1
			switch scenario {
			case "timeout":
				api.wait, access.Timeout, want = true, time.Millisecond, c.AccessUnknown
			case "rate":
				api.err, want = accessErrorStub{code: 429, delay: 17}, c.AccessRetryable
			case "external":
				access.Store.(*accessStoreStub).instance.Mode = "external"
				want, calls = c.AccessRetryable, 0
			case "missing":
				access.Lifecycle, want, calls = nil, c.AccessFailed, 0
			case "permission":
				access.Routes.(*accessRoutesStub).err = c.ErrCapability
				want, calls = c.AccessRetryable, 0
			}
			result := access.BanMembershipMember(ctx, c.AccessJoinAction{Route: route, TelegramID: 456})
			if result.Outcome != want || len(api.calls) != calls {
				t.Fatalf("outcome=%+v calls=%d", result, len(api.calls))
			}
			if scenario == "rate" && (result.RetryAfter != 17*time.Second || result.ErrorClass != "rate_limited") {
				t.Fatal("rate hint or sanitized classification lost")
			}
		})
	}
}
