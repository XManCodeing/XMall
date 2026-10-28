package settingsstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/XManCodeing/XMall/internal/constants"
	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"

	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestInternalMenuValidationPreservesLegacyExternalActions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&SettingRecord{}); err != nil {
		t.Fatal(err)
	}
	service := settingsapp.NewService(New(db))
	cfg := settingsmessaging.DefaultTelegramBotConfig()
	for _, action := range []settingsmessaging.TelegramBotMenuAction{
		{Type: "web_app", Value: "https://example.com"},
		{Type: "command", Value: "/start"},
		{Type: "builtin", Value: "gift_card"},
		{Type: "unknown", Value: "shop"},
	} {
		cfg.Menu.Items = append(cfg.Menu.Items, settingsmessaging.TelegramBotMenuItem{Key: action.Type, Enabled: true, Action: action})
	}
	// Generic storage/serialization remains compatible with external runners.
	if _, err := service.Update(constants.SettingKeyTelegramBotConfig, settingsmessaging.EncodeTelegramBotConfig(cfg)); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetTelegramBotConfig()
	if err != nil || len(current.Menu.Items) != 9 {
		t.Fatalf("legacy items hidden: %+v %v", current.Menu, err)
	}
	for _, section := range []string{"basic", "welcome", "help"} {
		next, err := service.PatchTelegramBotConfigSection(section, current.ConfigVersion, json.RawMessage(`{}`))
		if err != nil || len(next.Menu.Items) != 9 {
			t.Fatalf("%s blocked or changed legacy menu: %+v %v", section, next.Menu, err)
		}
		current = next
	}
	channel := settingsmessaging.SerializeTelegramBotConfigForChannel(current, "")
	if len(channel["menu"].(map[string]interface{})["items"].([]interface{})) != 9 {
		t.Fatal("external menu lost")
	}
	for _, actionType := range []string{"web_app", "command", "unknown", ""} {
		body := fmt.Sprintf(`{"items":[{"key":"bad","enabled":false,"action":{"type":%q,"value":"shop"}}]}`, actionType)
		if _, err := service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, json.RawMessage(body)); !errors.Is(err, settingsapp.ErrTelegramBotMenuUnsupported) {
			t.Fatalf("accepted type %q: %v", actionType, err)
		}
	}
	for _, value := range []string{"gift_card", "affiliate", "", "shop_home"} {
		body := fmt.Sprintf(`{"items":[{"key":"bad","action":{"type":"builtin","value":%q}}]}`, value)
		if _, err := service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, json.RawMessage(body)); !errors.Is(err, settingsapp.ErrTelegramBotMenuUnsupported) {
			t.Fatalf("accepted builtin %q: %v", value, err)
		}
	}
	for _, value := range []string{"shop", "orders", "wallet", "language", "help"} {
		body := fmt.Sprintf(`{"items":[{"key":%q,"action":{"type":"builtin","value":%q}},{"key":"link","action":{"type":"url","value":"https://example.com"}}]}`, value, value)
		next, err := service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, json.RawMessage(body))
		if err != nil {
			t.Fatalf("rejected supported menu: %v", err)
		}
		current = next
	}
}

func TestTelegramBotSectionsCAS(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&SettingRecord{}); err != nil {
		t.Fatal(err)
	}
	service := settingsapp.NewService(New(db))
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, body := range []string{`{"basic":{"display_name":"one"},"default_locale":"en-US"}`, `{"basic":{"display_name":"two"},"default_locale":"zh-CN"}`} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			_, err := service.PatchTelegramBotConfigSection("basic", 0, json.RawMessage(body))
			outcomes <- err
		}(body)
	}
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, settingsapp.ErrTelegramBotConfigConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	current, err := service.GetTelegramBotConfig()
	if err != nil {
		t.Fatal(err)
	}
	name := current.Basic.DisplayName
	next, err := service.PatchTelegramBotConfigSection("welcome", current.ConfigVersion, json.RawMessage(`{"enabled":true,"message":{"en-US":"hello"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if next.Basic.DisplayName != name || next.Welcome.Message["en-US"] != "hello" || next.Enabled {
		t.Fatalf("unexpected merged config: %+v", next)
	}
	if len(next.Menu.Items) != 5 {
		t.Fatalf("want five supported builtins, got %+v", next.Menu.Items)
	}
	for _, item := range next.Menu.Items {
		if item.Action.Type == "builtin" && (item.Key == "affiliate" || item.Key == "giftcard") {
			t.Fatal("unsupported builtin returned")
		}
	}
	if _, err := service.UpdateTelegramBotConfig(current); !errors.Is(err, settingsapp.ErrTelegramBotConfigConflict) {
		t.Fatalf("stale whole-object update: %v", err)
	}
}
