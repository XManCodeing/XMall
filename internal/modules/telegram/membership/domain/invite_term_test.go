package domain

import (
	"testing"
	"time"
)

// TestInviteTermSemantics 验证空枚举只能是有限期限、长期零截止合法且不能凭时钟过期。
func TestInviteTermSemantics(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name            string
		term            InviteTerm
		expires         time.Time
		usable, expired bool
	}{
		{"legacy", "", now.Add(time.Hour), true, false},
		{"legacy_expired", "", now.Add(-time.Hour), false, true},
		{"legacy_zero", "", time.Time{}, false, false},
		{"limited", InviteTermLimited, now.Add(time.Hour), true, false},
		{"long_term", InviteTermLongTerm, time.Time{}, true, false},
		{"long_term_bad_expiry", InviteTermLongTerm, now.Add(-time.Hour), false, false},
		{"unknown_term", "other", now.Add(time.Hour), false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invite := AccessInvite{Term: test.term, ExpiresAt: test.expires, ExpiryTrusted: true}
			if IsInviteUsableAt(invite, now) != test.usable || IsInviteExpiredAt(invite, now) != test.expired {
				t.Fatal("unsafe invitation term interpretation")
			}
			invite.ExpiryTrusted = false
			if IsInviteExpiredAt(invite, now) {
				t.Fatal("untrusted deadline released liability")
			}
		})
	}
}
