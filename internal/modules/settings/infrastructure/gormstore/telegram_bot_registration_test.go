package settingsstore

import (
	"encoding/json"
	"errors"
	"testing"

	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
)

// TestRegistrationCASAndSectionPreservation verifies independent CAS updates and
// preservation when legacy basic/menu writers save their own sections.
func TestRegistrationCASAndSectionPreservation(t *testing.T) {
	service, _ := newTelegramBotMenuTestService(t)
	current, err := service.GetTelegramBotConfig()
	if err != nil || current.Registration.Enabled {
		t.Fatalf("default registration: %+v %v", current.Registration, err)
	}
	current, err = service.PatchTelegramBotConfigSection("registration", 0, json.RawMessage(`{"enabled":true}`))
	if err != nil || !current.Registration.Enabled || current.ConfigVersion != 1 {
		t.Fatalf("registration patch: %+v %v", current, err)
	}
	if _, err := service.PatchTelegramBotConfigSection("registration", 0, json.RawMessage(`{"enabled":false}`)); !errors.Is(err, settingsapp.ErrTelegramBotConfigConflict) {
		t.Fatalf("stale write accepted: %v", err)
	}
	for _, section := range []string{"basic", "menu"} {
		current, err = service.PatchTelegramBotConfigSection(section, current.ConfigVersion, json.RawMessage(`{}`))
		if err != nil || !current.Registration.Enabled {
			t.Fatalf("%s lost registration: %v", section, err)
		}
	}
	stored, err := service.GetTelegramBotConfig()
	if err != nil || !stored.Registration.Enabled {
		t.Fatalf("registration not persisted: %v", err)
	}
}
