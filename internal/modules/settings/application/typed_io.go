package settingsapp

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/shared/jsonmap"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	settingsintegration "github.com/XManCodeing/XMall/internal/modules/settings/schema/integration"
	settingsmessaging "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	settingssecurity "github.com/XManCodeing/XMall/internal/modules/settings/schema/security"
	settingsstorefront "github.com/XManCodeing/XMall/internal/modules/settings/schema/storefront"
)

// GetDashboardSetting 获取仪表盘设置（优先 settings，空时回退默认）。
func (s *Service) GetDashboardSetting() (settingsstorefront.DashboardSetting, error) {
	fallback := settingsstorefront.DefaultDashboardSetting()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyDashboardConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsstorefront.DecodeDashboardSetting(value, fallback), nil
}

// GetDashboardLowStockThreshold 获取低库存阈值（读取失败回退默认值）。
func (s *Service) GetDashboardLowStockThreshold() int {
	defaultThreshold := int(settingsstorefront.DefaultDashboardSetting().Alert.LowStockThreshold)
	if s == nil {
		return defaultThreshold
	}
	setting, err := s.GetDashboardSetting()
	if err != nil {
		return defaultThreshold
	}
	return int(setting.Alert.LowStockThreshold)
}

// GetAffiliateSetting 获取推广返利设置（优先 settings，空时回退默认）。
func (s *Service) GetAffiliateSetting() (settingsintegration.AffiliateSetting, error) {
	fallback := settingsintegration.DefaultAffiliateSetting()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyAffiliateConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsintegration.DecodeAffiliateSetting(value, fallback), nil
}

// UpdateAffiliateSetting 更新推广返利设置。
func (s *Service) UpdateAffiliateSetting(setting settingsintegration.AffiliateSetting) (settingsintegration.AffiliateSetting, error) {
	normalized := settingsintegration.NormalizeAffiliateSetting(setting)
	if err := settingsintegration.ValidateAffiliateSetting(normalized); err != nil {
		return settingsintegration.DefaultAffiliateSetting(), err
	}
	if _, err := s.Update(constants.SettingKeyAffiliateConfig, map[string]interface{}(settingsintegration.EncodeAffiliateSetting(normalized))); err != nil {
		return settingsintegration.DefaultAffiliateSetting(), err
	}
	return normalized, nil
}

// GetUpstreamSyncConfig 获取上游同步配置。
// fallbackInterval 来自 config.yml，仅在数据库没有覆盖值时使用。
func (s *Service) GetUpstreamSyncConfig(fallbackInterval string) (settingsintegration.UpstreamSyncConfig, error) {
	fallback := settingsintegration.UpstreamSyncFallback(fallbackInterval)
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyUpstreamSyncConfig)
	if err != nil {
		return fallback, err
	}
	return settingsintegration.DecodeUpstreamSyncConfig(value, fallback), nil
}

// GetUpstreamSyncInterval 返回归一化后的同步间隔。
func (s *Service) GetUpstreamSyncInterval(fallbackInterval string) (time.Duration, error) {
	config, err := s.GetUpstreamSyncConfig(fallbackInterval)
	if err != nil {
		return time.Duration(config.IntervalMinutes) * time.Minute, err
	}
	return time.Duration(config.IntervalMinutes) * time.Minute, nil
}

// GetNotificationCenterSetting 获取通知中心配置（优先 settings，空时回退默认）。
func (s *Service) GetNotificationCenterSetting() (settingsmessaging.NotificationCenterSetting, error) {
	fallback := settingsmessaging.NotificationCenterDefaultSetting()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyNotificationCenterConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsmessaging.NormalizeNotificationCenterSetting(settingsmessaging.DecodeNotificationCenterSetting(value, fallback)), nil
}

// PatchNotificationCenterSetting 基于补丁更新通知中心配置。
func (s *Service) PatchNotificationCenterSetting(patch settingsmessaging.NotificationCenterSettingPatch) (settingsmessaging.NotificationCenterSetting, error) {
	current, err := s.GetNotificationCenterSetting()
	if err != nil {
		return settingsmessaging.NotificationCenterSetting{}, err
	}
	next, err := settingsmessaging.ApplyNotificationCenterSettingPatch(current, patch)
	if err != nil {
		return settingsmessaging.NotificationCenterSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeyNotificationCenterConfig, settingsmessaging.NotificationCenterSettingToMap(next)); err != nil {
		return settingsmessaging.NotificationCenterSetting{}, err
	}
	return next, nil
}

// GetSMTPSetting 获取 SMTP 设置（优先 settings，空时回退默认配置）。
func (s *Service) GetSMTPSetting(defaultCfg config.EmailConfig) (settingsmessaging.SMTPSetting, error) {
	fallback := settingsmessaging.DefaultSMTPSetting(defaultCfg)
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeySMTPConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsmessaging.NormalizeSMTPSetting(settingsmessaging.DecodeSMTPSetting(value, fallback)), nil
}

// PatchSMTPSetting 基于补丁更新 SMTP 设置。
func (s *Service) PatchSMTPSetting(defaultCfg config.EmailConfig, patch settingsmessaging.SMTPSettingPatch) (settingsmessaging.SMTPSetting, error) {
	current, err := s.GetSMTPSetting(defaultCfg)
	if err != nil {
		return settingsmessaging.SMTPSetting{}, err
	}
	next, err := settingsmessaging.ApplySMTPSettingPatch(current, patch)
	if err != nil {
		return settingsmessaging.SMTPSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeySMTPConfig, map[string]interface{}(settingsmessaging.EncodeSMTPSetting(next))); err != nil {
		return settingsmessaging.SMTPSetting{}, err
	}
	return next, nil
}

// GetCaptchaSetting 获取验证码设置（优先 settings，空时回退 config.yml）。
func (s *Service) GetCaptchaSetting(defaultCfg config.CaptchaConfig) (settingssecurity.CaptchaSetting, error) {
	fallback := settingssecurity.DefaultCaptchaSetting(defaultCfg)
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyCaptchaConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingssecurity.NormalizeCaptchaSetting(settingssecurity.DecodeCaptchaSetting(value, fallback)), nil
}

// PatchCaptchaSetting 基于补丁更新验证码设置。
func (s *Service) PatchCaptchaSetting(defaultCfg config.CaptchaConfig, patch settingssecurity.CaptchaSettingPatch) (settingssecurity.CaptchaSetting, error) {
	current, err := s.GetCaptchaSetting(defaultCfg)
	if err != nil {
		return settingssecurity.CaptchaSetting{}, err
	}
	next, err := settingssecurity.ApplyCaptchaSettingPatch(current, patch)
	if err != nil {
		return settingssecurity.CaptchaSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeyCaptchaConfig, map[string]interface{}(settingssecurity.EncodeCaptchaSetting(next))); err != nil {
		return settingssecurity.CaptchaSetting{}, err
	}
	return next, nil
}

// GetTelegramAuthSetting 获取 Telegram 登录配置。
func (s *Service) GetTelegramAuthSetting(defaultCfg config.TelegramAuthConfig) (settingssecurity.TelegramAuthSetting, error) {
	fallback := settingssecurity.DefaultTelegramAuthSetting(defaultCfg)
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyTelegramAuthConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingssecurity.NormalizeTelegramAuthSetting(settingssecurity.DecodeTelegramAuthSetting(value, fallback)), nil
}

// PatchTelegramAuthSetting 基于补丁更新 Telegram 登录配置。
func (s *Service) PatchTelegramAuthSetting(defaultCfg config.TelegramAuthConfig, patch settingssecurity.TelegramAuthSettingPatch) (settingssecurity.TelegramAuthSetting, error) {
	current, err := s.GetTelegramAuthSetting(defaultCfg)
	if err != nil {
		return settingssecurity.TelegramAuthSetting{}, err
	}
	next := settingssecurity.NormalizeTelegramAuthSetting(settingssecurity.ApplyTelegramAuthSettingPatch(current, patch))
	if err := settingssecurity.ValidateTelegramAuthSetting(next); err != nil {
		return settingssecurity.TelegramAuthSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeyTelegramAuthConfig, map[string]interface{}(settingssecurity.EncodeTelegramAuthSetting(next))); err != nil {
		return settingssecurity.TelegramAuthSetting{}, err
	}
	return next, nil
}

// GetGoogleAuthSetting 获取 Google Identity Services 登录配置。
func (s *Service) GetGoogleAuthSetting(defaultCfg config.GoogleAuthConfig) (settingssecurity.GoogleAuthSetting, error) {
	fallback := settingssecurity.DefaultGoogleAuthSetting(defaultCfg)
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyGoogleAuthConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingssecurity.DecodeGoogleAuthSetting(value, fallback), nil
}

// PatchGoogleAuthSetting 基于补丁更新 Google Identity Services 登录配置。
func (s *Service) PatchGoogleAuthSetting(defaultCfg config.GoogleAuthConfig, patch settingssecurity.GoogleAuthSettingPatch) (settingssecurity.GoogleAuthSetting, error) {
	current, err := s.GetGoogleAuthSetting(defaultCfg)
	if err != nil {
		return settingssecurity.GoogleAuthSetting{}, err
	}
	next := settingssecurity.NormalizeGoogleAuthSetting(settingssecurity.ApplyGoogleAuthSettingPatch(current, patch))
	if err := settingssecurity.ValidateGoogleAuthSetting(next); err != nil {
		return settingssecurity.GoogleAuthSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeyGoogleAuthConfig, map[string]interface{}(settingssecurity.EncodeGoogleAuthSetting(next))); err != nil {
		return settingssecurity.GoogleAuthSetting{}, err
	}
	return next, nil
}

// GetOrderEmailTemplateSetting 获取订单邮件模板配置（优先 settings，空时回退默认）。
func (s *Service) GetOrderEmailTemplateSetting() (settingsmessaging.OrderEmailTemplateSetting, error) {
	fallback := settingsmessaging.DefaultOrderEmailTemplateSetting()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyOrderEmailTemplateConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsmessaging.NormalizeOrderEmailTemplateSetting(settingsmessaging.DecodeOrderEmailTemplateSetting(value, fallback)), nil
}

// PatchOrderEmailTemplateSetting 基于补丁更新订单邮件模板配置。
func (s *Service) PatchOrderEmailTemplateSetting(patch settingsmessaging.OrderEmailTemplateSettingPatch) (settingsmessaging.OrderEmailTemplateSetting, error) {
	current, err := s.GetOrderEmailTemplateSetting()
	if err != nil {
		return settingsmessaging.OrderEmailTemplateSetting{}, err
	}
	next, err := settingsmessaging.ApplyOrderEmailTemplateSettingPatch(current, patch)
	if err != nil {
		return settingsmessaging.OrderEmailTemplateSetting{}, err
	}
	if _, err := s.Update(constants.SettingKeyOrderEmailTemplateConfig, map[string]interface{}(settingsmessaging.EncodeOrderEmailTemplateSetting(next))); err != nil {
		return settingsmessaging.OrderEmailTemplateSetting{}, err
	}
	return next, nil
}

// ResetOrderEmailTemplateSetting 重置订单邮件模板为默认。
func (s *Service) ResetOrderEmailTemplateSetting() (settingsmessaging.OrderEmailTemplateSetting, error) {
	defaultSetting := settingsmessaging.DefaultOrderEmailTemplateSetting()
	if s == nil {
		return defaultSetting, nil
	}
	if _, err := s.Update(constants.SettingKeyOrderEmailTemplateConfig, map[string]interface{}(settingsmessaging.EncodeOrderEmailTemplateSetting(defaultSetting))); err != nil {
		return settingsmessaging.OrderEmailTemplateSetting{}, err
	}
	return defaultSetting, nil
}

// GetTelegramBotConfig 获取 Telegram Bot 配置。
func (s *Service) GetTelegramBotConfig() (settingsmessaging.TelegramBotConfigSetting, error) {
	fallback := settingsmessaging.DefaultTelegramBotConfig()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyTelegramBotConfig)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		fallback.Menu.Items = supportedBotMenus(fallback.Menu.Items)
		return fallback, nil
	}
	parsed := settingsmessaging.DecodeTelegramBotConfig(value, fallback)
	parsed.Menu.Items = supportedBotMenus(settingsmessaging.EnsureBuiltinMenuItems(parsed.Menu.Items))
	return parsed, nil
}

// UpdateTelegramBotConfig 整对象覆盖更新 Telegram Bot 配置，自动递增 config_version。
func (s *Service) UpdateTelegramBotConfig(cfg settingsmessaging.TelegramBotConfigSetting) (settingsmessaging.TelegramBotConfigSetting, error) {
	return s.updateTelegramBotConfig(cfg, true)
}

func (s *Service) updateTelegramBotConfig(cfg settingsmessaging.TelegramBotConfigSetting, normalizeMenu bool) (settingsmessaging.TelegramBotConfigSetting, error) {
	current, err := s.GetTelegramBotConfig()
	if err != nil {
		return settingsmessaging.TelegramBotConfigSetting{}, err
	}

	if cfg.ConfigVersion != current.ConfigVersion {
		return settingsmessaging.TelegramBotConfigSetting{}, ErrTelegramBotConfigConflict
	}
	cfg.Enabled = false // Runtime is controlled exclusively by managed instances.
	cfg.ConfigVersion = current.ConfigVersion + 1
	cfg.Basic.Description = settingsmessaging.NormalizeLocalizedText(cfg.Basic.Description)
	cfg.Welcome.Message = settingsmessaging.NormalizeLocalizedText(cfg.Welcome.Message)
	cfg.Help.Title = settingsmessaging.NormalizeLocalizedText(cfg.Help.Title)
	cfg.Help.Intro = settingsmessaging.NormalizeLocalizedText(cfg.Help.Intro)
	cfg.Help.CenterHint = settingsmessaging.NormalizeLocalizedText(cfg.Help.CenterHint)
	cfg.Help.SupportHint = settingsmessaging.NormalizeLocalizedText(cfg.Help.SupportHint)
	cfg.Help.Items = settingsmessaging.NormalizeHelpItems(cfg.Help.Items)
	if normalizeMenu {
		cfg.Menu.Items = supportedBotMenus(settingsmessaging.NormalizeTelegramBotMenuItems(cfg.Menu.Items))
	}

	store, ok := s.repo.(interface {
		CompareAndSwapTelegramBotConfig(string, int, jsonmap.JSON) (bool, error)
	})
	if !ok {
		return settingsmessaging.TelegramBotConfigSetting{}, errors.New("settings store does not support CAS")
	}
	value := s.registry.Normalize(constants.SettingKeyTelegramBotConfig, settingsmessaging.EncodeTelegramBotConfig(cfg))
	value["menu"] = settingsmessaging.EncodeTelegramBotConfig(cfg)["menu"]
	if err := s.registry.Validate(constants.SettingKeyTelegramBotConfig, value); err != nil {
		return settingsmessaging.TelegramBotConfigSetting{}, err
	}
	updated, err := store.CompareAndSwapTelegramBotConfig(constants.SettingKeyTelegramBotConfig, current.ConfigVersion, value)
	if err != nil {
		return settingsmessaging.TelegramBotConfigSetting{}, err
	}
	if !updated {
		return settingsmessaging.TelegramBotConfigSetting{}, ErrTelegramBotConfigConflict
	}

	return cfg, nil
}

func supportedBotMenus(items []settingsmessaging.TelegramBotMenuItem) []settingsmessaging.TelegramBotMenuItem {
	result := make([]settingsmessaging.TelegramBotMenuItem, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if item.Action.Type == "builtin" {
			if item.Action.Value == "" {
				item.Action.Value = item.Key
			}
			aliases := map[string]string{"shop_home": "shop", "my_orders": "orders", "my_wallet": "wallet", "switch_language": "language", "contact_support": "help"}
			if value, ok := aliases[item.Action.Value]; ok {
				item.Action.Value = value
			}
			if value, ok := aliases[item.Key]; ok {
				item.Key = value
			}
			if seen[item.Action.Value] {
				continue
			}
			seen[item.Action.Value] = true
		}
		result = append(result, item)
	}
	return result
}

var ErrTelegramBotConfigConflict = errors.New("telegram bot configuration version conflict")
var ErrTelegramBotSection = errors.New("invalid telegram bot configuration section")
var ErrTelegramBotMenuUnsupported = errors.New("internal bot menus support only builtin (shop, orders, wallet, language, help) and url actions")

// PatchTelegramBotConfigSection merges only the selected section; CAS prevents stale writes.
func (s *Service) PatchTelegramBotConfigSection(section string, expected int, raw json.RawMessage) (settingsmessaging.TelegramBotConfigSetting, error) {
	current, err := s.GetTelegramBotConfig()
	if err != nil {
		return current, err
	}
	if current.ConfigVersion != expected {
		return current, ErrTelegramBotConfigConflict
	}
	switch section {
	case "basic":
		var data struct {
			Basic         settingsmessaging.TelegramBotBasicConfig `json:"basic"`
			DefaultLocale string                                   `json:"default_locale"`
		}
		if err = json.Unmarshal(raw, &data); err == nil {
			current.Basic = data.Basic
			current.DefaultLocale = data.DefaultLocale
		}
	case "welcome":
		err = json.Unmarshal(raw, &current.Welcome)
	case "help":
		err = json.Unmarshal(raw, &current.Help)
	case "menu":
		err = json.Unmarshal(raw, &current.Menu)
		// This admin section configures the internal runtime. The shared schema and
		// channel serialization still retain external runners' action types.
		for _, item := range current.Menu.Items {
			switch strings.TrimSpace(item.Action.Type) {
			case "url":
			case "builtin":
				switch strings.TrimSpace(item.Action.Value) {
				case "shop", "orders", "wallet", "language", "help":
				default:
					return current, ErrTelegramBotMenuUnsupported
				}
			default:
				return current, ErrTelegramBotMenuUnsupported
			}
		}
	default:
		return current, ErrTelegramBotSection
	}
	if err != nil {
		return current, ErrTelegramBotSection
	}
	return s.updateTelegramBotConfig(current, section == "menu")
}

// GetTelegramBotRuntimeStatus 获取 Telegram Bot 运行时状态。
func (s *Service) GetTelegramBotRuntimeStatus() (settingsmessaging.TelegramBotRuntimeStatusSetting, error) {
	fallback := settingsmessaging.DefaultTelegramBotRuntimeStatus()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyTelegramBotRuntimeStatus)
	if err != nil {
		return fallback, err
	}
	if value == nil {
		return fallback, nil
	}
	return settingsmessaging.DecodeTelegramBotRuntimeStatus(value, fallback), nil
}

// UpdateTelegramBotRuntimeStatus 更新 Telegram Bot 运行时状态。
func (s *Service) UpdateTelegramBotRuntimeStatus(status settingsmessaging.TelegramBotRuntimeStatusSetting) error {
	if s == nil {
		return nil
	}
	_, err := s.Update(constants.SettingKeyTelegramBotRuntimeStatus, settingsmessaging.EncodeTelegramBotRuntimeStatus(status))
	return err
}
