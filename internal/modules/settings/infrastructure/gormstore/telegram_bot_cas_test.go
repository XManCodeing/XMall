package settingsstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	if err != nil || len(current.Menu.Items) != len(settingsmessaging.BuiltinTelegramBotMenuKeysOrder)+4 {
		t.Fatalf("legacy items hidden: %+v %v", current.Menu, err)
	}
	for _, section := range []string{"basic", "welcome", "help"} {
		next, err := service.PatchTelegramBotConfigSection(section, current.ConfigVersion, json.RawMessage(`{}`))
		if err != nil || len(next.Menu.Items) != len(settingsmessaging.BuiltinTelegramBotMenuKeysOrder)+4 {
			t.Fatalf("%s blocked or changed legacy menu: %+v %v", section, next.Menu, err)
		}
		current = next
	}
	channel := settingsmessaging.SerializeTelegramBotConfigForChannel(current, "")
	if len(channel["menu"].(map[string]interface{})["items"].([]interface{})) != len(settingsmessaging.BuiltinTelegramBotMenuKeysOrder)+4 {
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
	for _, value := range []string{"shop", "orders", "memberships", "wallet", "language", "help"} {
		body := fmt.Sprintf(`{"items":[{"key":%q,"action":{"type":"builtin","value":%q}},{"key":"link","action":{"type":"url","value":"https://example.com"}}]}`, value, value)
		next, err := service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, json.RawMessage(body))
		if err != nil {
			t.Fatalf("rejected supported menu: %v", err)
		}
		current = next
	}
	dup := `{"items":[{"key":"shop","action":{"type":"builtin","value":"shop"}},{"key":"shop2","action":{"type":"builtin","value":"shop"}}]}`
	if _, err := service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, json.RawMessage(dup)); !errors.Is(err, settingsapp.ErrTelegramBotMenuDuplicate) {
		t.Fatalf("duplicate builtin accepted: %v", err)
	}
}

func newTelegramBotMenuTestService(t *testing.T) (*settingsapp.Service, *Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&SettingRecord{}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	return settingsapp.NewService(store), store
}

func TestTelegramBotMenuRawDuplicatesDoNotPersist(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%t", whole), func(t *testing.T) {
			service, store := newTelegramBotMenuTestService(t)
			current, err := service.PatchTelegramBotConfigSection("welcome", 0, json.RawMessage(`{"message":{"en-US":"keep me"}}`))
			if err != nil {
				t.Fatal(err)
			}
			before, found, err := store.GetByKey(constants.SettingKeyTelegramBotConfig)
			if err != nil || !found {
				t.Fatalf("read seed: found=%t err=%v", found, err)
			}
			type duplicateCase struct {
				name, key, actionType, value string
			}
			cases := []duplicateCase{
				{"exact", "other", "builtin", "shop"},
				{"value whitespace", "other", "builtin", " \tshop\n "},
				{"type whitespace", "other", " builtin \t", "shop"},
				{"all whitespace", " shop_home ", " builtin ", " shop "},
			}
			if whole {
				cases = append(cases,
					duplicateCase{"alias", "other", " builtin ", " shop_home "},
					duplicateCase{"key fallback", " shop_home ", " builtin ", " \t"},
				)
			}
			for _, tc := range cases {
				for _, enabled := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/enabled=%t", tc.name, enabled), func(t *testing.T) {
						cfg := current
						cfg.Menu.Items = []settingsmessaging.TelegramBotMenuItem{
							{Key: "shop", Enabled: true, Action: settingsmessaging.TelegramBotMenuAction{Type: "builtin", Value: "shop"}},
							{Key: tc.key, Enabled: enabled, Action: settingsmessaging.TelegramBotMenuAction{Type: tc.actionType, Value: tc.value}},
						}
						if whole {
							_, err = service.UpdateTelegramBotConfig(cfg)
						} else {
							body, marshalErr := json.Marshal(cfg.Menu)
							if marshalErr != nil {
								t.Fatal(marshalErr)
							}
							_, err = service.PatchTelegramBotConfigSection("menu", current.ConfigVersion, body)
						}
						if !errors.Is(err, settingsapp.ErrTelegramBotMenuDuplicate) {
							t.Fatalf("want duplicate error, got %v", err)
						}
						after, found, err := store.GetByKey(constants.SettingKeyTelegramBotConfig)
						if err != nil || !found || !reflect.DeepEqual(before, after) {
							t.Fatalf("rejected write changed persisted config: before=%+v after=%+v err=%v", before, after, err)
						}
						reloaded, err := service.GetTelegramBotConfig()
						if err != nil || reloaded.ConfigVersion != current.ConfigVersion {
							t.Fatalf("rejected write changed version: got=%d want=%d err=%v", reloaded.ConfigVersion, current.ConfigVersion, err)
						}
					})
				}
			}
		})
	}
}

func TestTelegramBotMenuNormalizationPreservesBackfill(t *testing.T) {
	for _, whole := range []bool{false, true} {
		for _, tc := range []struct{ key, alias string }{
			{"shop", "shop_home"}, {"orders", "my_orders"}, {"wallet", "my_wallet"},
			{"language", "switch_language"}, {"help", "contact_support"},
		} {
			t.Run(fmt.Sprintf("whole=%t/%s", whole, tc.key), func(t *testing.T) {
				service, store := newTelegramBotMenuTestService(t)
				cfg := settingsmessaging.DefaultTelegramBotConfig()
				value := " " + tc.key + " "
				if whole {
					value = " " + tc.alias + " "
				}
				cfg.Menu.Items = []settingsmessaging.TelegramBotMenuItem{
					{Key: " " + tc.alias + " ", Enabled: false, Order: 7, Label: settingsmessaging.LocalizedText{"en-US": " custom "}, Action: settingsmessaging.TelegramBotMenuAction{Type: " builtin ", Value: value}},
					// A custom key makes backfill generate the same builtin action;
					// only user-supplied items must participate in duplicate validation.
					{Key: " custom ", Enabled: false, Order: 8, Action: settingsmessaging.TelegramBotMenuAction{Type: "builtin", Value: "shop"}},
					{Key: " link ", Enabled: true, Action: settingsmessaging.TelegramBotMenuAction{Type: " url ", Value: " https://example.com "}},
				}
				if tc.key == "shop" {
					cfg.Menu.Items = append(cfg.Menu.Items[:1], cfg.Menu.Items[2:]...)
				}
				var next settingsmessaging.TelegramBotConfigSetting
				var err error
				if whole {
					next, err = service.UpdateTelegramBotConfig(cfg)
				} else {
					body, marshalErr := json.Marshal(cfg.Menu)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					next, err = service.PatchTelegramBotConfigSection("menu", cfg.ConfigVersion, body)
				}
				if err != nil {
					t.Fatal(err)
				}
				if next.ConfigVersion != cfg.ConfigVersion+1 || len(next.Menu.Items) != len(settingsmessaging.BuiltinTelegramBotMenuKeysOrder)+1 {
					t.Fatalf("unexpected version/backfill: %+v", next)
				}
				first := next.Menu.Items[0]
				if first.Key != tc.key || first.Action.Type != "builtin" || first.Action.Value != tc.key || first.Enabled || first.Order != 7 || first.Label["en-US"] != "custom" {
					t.Fatalf("lost normalized user item: %+v", first)
				}
				seen := map[string]bool{}
				for _, item := range next.Menu.Items {
					if item.Action.Type == "url" {
						if item.Key != "link" || item.Action.Value != "https://example.com" {
							t.Fatalf("untrimmed url: %+v", item)
						}
						continue
					}
					if seen[item.Action.Value] {
						t.Fatalf("duplicate backfilled action: %+v", item)
					}
					seen[item.Action.Value] = true
					if item.Action.Value == "shop" && tc.key != "shop" && (item.Key != "custom" || item.Enabled || item.Order != 8) {
						t.Fatalf("backfill replaced user item: %+v", item)
					}
				}
				stored, found, err := store.GetByKey(constants.SettingKeyTelegramBotConfig)
				if err != nil || !found {
					t.Fatalf("read persisted config: found=%t err=%v", found, err)
				}
				// JSON round-trip avoids comparing int and float64 representations.
				want, err := json.Marshal(settingsmessaging.EncodeTelegramBotConfig(next))
				if err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(stored)
				if err != nil || string(got) != string(want) {
					t.Fatalf("persisted config differs: got=%s want=%s err=%v", got, want, err)
				}
			})
		}
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
	if len(next.Menu.Items) != len(settingsmessaging.BuiltinTelegramBotMenuKeysOrder) {
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
