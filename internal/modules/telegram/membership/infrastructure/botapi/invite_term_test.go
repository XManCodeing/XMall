package botapi

import (
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"testing"
	"time"
)

// TestLongTermInviteResponse 检查显式长期请求 expire_date=0，响应期限/申请制不匹配保持未知。
func TestLongTermInviteResponse(t *testing.T) {
	for _, scenario := range []string{"valid", "finite", "direct", "invalid_term"} {
		t.Run(scenario, func(t *testing.T) {
			adapter, ctx, api, route := accessFixture()
			api.invite.ExpireDate = 0
			input := c.AccessCreateInvite{Route: route, Name: "long-term", Term: d.InviteTermLongTerm}
			if scenario == "finite" {
				api.invite.ExpireDate = time.Now().Add(time.Hour).Unix()
			}
			if scenario == "direct" {
				api.invite.CreatesJoinRequest = false
			}
			if scenario == "invalid_term" {
				input.Term = "unrecognized"
			}
			result := adapter.CreateMembershipInvite(ctx, input)
			expected := c.AccessUnknown
			if scenario == "valid" {
				expected = c.AccessSucceeded
			}
			if scenario == "invalid_term" {
				expected = c.AccessFailed
			}
			if result.Outcome != expected {
				t.Fatalf("outcome %+v", result)
			}
			if scenario != "invalid_term" && (len(api.calls) != 1 || api.request.ExpireDate != 0) {
				t.Fatalf("request %+v", api.request)
			}
			if scenario == "invalid_term" && len(api.calls) != 0 {
				t.Fatal("invalid term dispatched")
			}
		})
	}
}
