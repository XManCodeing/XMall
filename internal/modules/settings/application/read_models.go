package settingsapp

import (
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	settingssecurity "github.com/XManCodeing/XMall/internal/modules/settings/schema/security"
	settingsstorefront "github.com/XManCodeing/XMall/internal/modules/settings/schema/storefront"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

// GetActiveHomeAnnouncement returns the currently displayable announcement.
func (s *Service) GetActiveHomeAnnouncement() (jsonmap.JSON, bool) {
	if s == nil {
		return nil, false
	}
	value, err := s.GetByKey(constants.SettingKeyHomeAnnouncement)
	if err != nil || value == nil {
		return nil, false
	}
	return settingsstorefront.ActiveHomeAnnouncement(value, time.Now())
}

// GetOrderRiskControlConfig returns the normalized order risk policy.
func (s *Service) GetOrderRiskControlConfig() (settingssecurity.OrderRiskControlConfig, error) {
	fallback := settingssecurity.DefaultOrderRiskControlConfig()
	if s == nil {
		return fallback, nil
	}
	value, err := s.GetByKey(constants.SettingKeyOrderRiskControlConfig)
	if err != nil {
		return fallback, err
	}
	return settingssecurity.DecodeOrderRiskControlConfig(value, fallback), nil
}
