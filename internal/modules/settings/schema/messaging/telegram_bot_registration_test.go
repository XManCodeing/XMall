package settingsmessaging

import (
	"testing"

	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// TestRegistrationDecodeFailsClosed verifies absent/legacy/malformed flags do not
// inherit an enabled fallback and that valid booleans survive serialization.
func TestRegistrationDecodeFailsClosed(t *testing.T) {
	fallback := DefaultTelegramBotConfig()
	fallback.Registration.Enabled = true
	for _, raw := range []jsonmap.JSON{nil, {}, {"bot_display_name": "legacy"}, {"registration": map[string]interface{}{"enabled": "true"}}} {
		if DecodeTelegramBotConfig(raw, fallback).Registration.Enabled {
			t.Fatalf("missing/invalid registration enabled: %+v", raw)
		}
	}
	encoded := EncodeTelegramBotConfig(fallback)
	if !DecodeTelegramBotConfig(encoded, DefaultTelegramBotConfig()).Registration.Enabled {
		t.Fatal("registration flag lost in round-trip")
	}
}
