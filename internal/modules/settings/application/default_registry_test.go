package settingsapp

import (
	"errors"
	"reflect"
	"testing"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// TestNotificationPrerequisitesBlockPersistence 验证参数表中的各渠道前提，失败不落库且关闭可保存。
func TestNotificationPrerequisitesBlockPersistence(t *testing.T) {
	for _, channel := range []string{"email", "telegram", "feishu"} {
		t.Run(channel, func(t *testing.T) {
			repo := newMockSettingRepo()
			service := NewService(repo)
			setting := settingsmessaging.NotificationCenterDefaultSetting()
			switch channel {
			case "email":
				setting.Channels.Email = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"admin@example.com"}}
			case "telegram":
				setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123456"}}
			case "feishu":
				setting.Channels.Feishu.Enabled = true
				setting.Channels.Feishu.Recipients = []string{"chat-test"}
			}
			_, err := service.Update(constants.SettingKeyNotificationCenterConfig, settingsmessaging.NotificationCenterSettingToMap(setting))
			if !errors.Is(err, settingsmessaging.ErrNotificationConfigInvalid) {
				t.Fatalf("expected prerequisite validation error, got %v", err)
			}
			if _, found := repo.store[constants.SettingKeyNotificationCenterConfig]; found {
				t.Fatal("invalid notification settings were persisted")
			}
			setting.Channels.Email.Enabled = false
			setting.Channels.Telegram.Enabled = false
			setting.Channels.Feishu.Enabled = false
			if _, err := service.Update(constants.SettingKeyNotificationCenterConfig, settingsmessaging.NotificationCenterSettingToMap(setting)); err != nil {
				t.Fatalf("disabling must be allowed: %v", err)
			}
		})
	}
}

// TestNotificationPrerequisitesUseEffectiveSettings 验证静态回退、数据库覆盖和关闭登录仍可推送。
func TestNotificationPrerequisitesUseEffectiveSettings(t *testing.T) {
	repo := newMockSettingRepo()
	service := NewService(repo).WithNotificationDefaults(
		config.EmailConfig{Enabled: true, Host: "smtp.example.com", Port: 587, From: "sender@example.com"},
		config.TelegramAuthConfig{Enabled: false, BotToken: "test-only-placeholder"},
	)
	state, err := service.GetNotificationPrerequisites()
	if err != nil || !state["email"] || !state["telegram"] {
		t.Fatalf("static defaults should be configured: %v, %v", state, err)
	}
	setting := settingsmessaging.NotificationCenterDefaultSetting()
	setting.Channels.Email = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"admin@example.com"}}
	setting.Channels.Telegram = settingsmessaging.NotificationChannelSetting{Enabled: true, Recipients: []string{"123456"}}
	if _, err := service.Update(constants.SettingKeyNotificationCenterConfig, settingsmessaging.NotificationCenterSettingToMap(setting)); err != nil {
		t.Fatalf("complete configuration should save without contacting providers: %v", err)
	}
	repo.store[constants.SettingKeySMTPConfig] = jsonmap.JSON{"enabled": false}
	repo.store[constants.SettingKeyTelegramAuthConfig] = jsonmap.JSON{"enabled": false, "bot_token": "  "}
	state, err = service.GetNotificationPrerequisites()
	if err != nil || state["email"] || state["telegram"] {
		t.Fatalf("database overrides must take precedence: %v, %v", state, err)
	}
}

// TestNotificationPatchCannotBypassPrerequisites 验证补丁保存入口同样拒绝缺少 Token 的启用请求。
func TestNotificationPatchCannotBypassPrerequisites(t *testing.T) {
	service := NewService(newMockSettingRepo())
	enabled := true
	recipients := []string{"123456"}
	_, err := service.PatchNotificationCenterSetting(settingsmessaging.NotificationCenterSettingPatch{
		Channels: &settingsmessaging.NotificationChannelsPatch{
			Telegram: &settingsmessaging.NotificationChannelPatch{Enabled: &enabled, Recipients: &recipients},
		},
	})
	if !errors.Is(err, settingsmessaging.ErrNotificationConfigInvalid) {
		t.Fatalf("patch must reject missing token: %v", err)
	}
}

func TestDefaultSettingRegistryCoversLegacyNormalizedKeys(t *testing.T) {
	want := []string{
		constants.SettingKeyAffiliateConfig,
		constants.SettingKeyCallbackRoutesConfig,
		constants.SettingKeyDashboardConfig,
		constants.SettingKeyGoogleAuthConfig,
		constants.SettingKeyHomeAnnouncement,
		constants.SettingKeyNavConfig,
		constants.SettingKeyNotificationCenterConfig,
		constants.SettingKeyOrderConfig,
		constants.SettingKeyOrderRiskControlConfig,
		constants.SettingKeyPaymentConfig,
		constants.SettingKeyRegistrationConfig,
		constants.SettingKeySiteConfig,
		constants.SettingKeyTelegramAuthConfig,
		constants.SettingKeyTelegramBotConfig,
		constants.SettingKeyUpstreamSyncConfig,
		constants.SettingKeyWalletConfig,
	}

	if got := defaultSettingRegistry.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default registry keys mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestSettingServiceUpdateWithEffectsDescribesCacheImpact(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		value      map[string]interface{}
		wantEffect Effect
	}{
		{
			name:       "google auth config invalidates public cache",
			key:        constants.SettingKeyGoogleAuthConfig,
			value:      map[string]interface{}{"enabled": true, "client_id": "client.apps.googleusercontent.com"},
			wantEffect: EffectInvalidatePublicConfigCache,
		},
		{
			name:       "site config invalidates public cache",
			key:        constants.SettingKeySiteConfig,
			value:      map[string]interface{}{},
			wantEffect: EffectInvalidatePublicConfigCache,
		},
		{
			name:       "wallet config is effect only",
			key:        constants.SettingKeyWalletConfig,
			value:      map[string]interface{}{constants.SettingFieldWalletOnlyPayment: true},
			wantEffect: EffectInvalidatePublicConfigCache,
		},
		{
			name:       "callback routes invalidate route cache",
			key:        constants.SettingKeyCallbackRoutesConfig,
			value:      map[string]interface{}{constants.SettingFieldPaymentCallback: "/api/custom/callback"},
			wantEffect: EffectInvalidateCallbackRoutesCache,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newMockSettingRepo()
			service := NewService(repo)
			result, err := service.UpdateWithEffects(test.key, test.value)
			if err != nil {
				t.Fatalf("update setting: %v", err)
			}
			if !result.HasEffect(test.wantEffect) {
				t.Fatalf("expected effect %q, got %#v", test.wantEffect, result.Effects)
			}
		})
	}

	repo := newMockSettingRepo()
	service := NewService(repo)
	result, err := service.UpdateWithEffects("custom_extension_config", map[string]interface{}{"enabled": true})
	if err != nil {
		t.Fatalf("update unknown setting: %v", err)
	}
	if len(result.Effects) != 0 {
		t.Fatalf("unknown setting emitted effects: %#v", result.Effects)
	}
}

func TestSettingServiceUpdateKeepsUnknownKeyPassThroughBehavior(t *testing.T) {
	repo := newMockSettingRepo()
	service := NewService(repo)
	input := map[string]interface{}{
		"nested": map[string]interface{}{"enabled": true},
		"count":  float64(3),
	}

	got, err := service.Update("custom_extension_config", input)
	if err != nil {
		t.Fatalf("update unknown setting: %v", err)
	}
	if !reflect.DeepEqual(got, jsonmap.JSON(input)) {
		t.Fatalf("unknown setting changed during pass-through: %#v", got)
	}
	if saved := repo.store["custom_extension_config"]; !reflect.DeepEqual(saved, jsonmap.JSON(input)) {
		t.Fatalf("unknown setting persisted with a different shape: %#v", saved)
	}
}

func TestPaymentFeeConfigDefaultsSafeAndNormalizesSwitches(t *testing.T) {
	repo := newMockSettingRepo()
	service := NewService(repo)

	if got := service.GetPaymentFeeConfig(); got != (PaymentFeeConfig{}) {
		t.Fatalf("default payment fee config must be safe: %#v", got)
	}

	stored, err := service.Update(constants.SettingKeyPaymentConfig, map[string]interface{}{
		constants.SettingFieldCustomerFeeEnabled:         "true",
		constants.SettingFieldReuseLegacyOrderFeePayment: 1,
		"unexpected": true,
	})
	if err != nil {
		t.Fatalf("update payment fee config: %v", err)
	}
	want := jsonmap.JSON{
		constants.SettingFieldCustomerFeeEnabled:         true,
		constants.SettingFieldReuseLegacyOrderFeePayment: true,
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("normalized payment config mismatch: %#v", stored)
	}
	if got := service.GetPaymentFeeConfig(); got != (PaymentFeeConfig{CustomerFeeEnabled: true, ReuseLegacyOrderFeePayment: true}) {
		t.Fatalf("payment fee config mismatch: %#v", got)
	}
}
