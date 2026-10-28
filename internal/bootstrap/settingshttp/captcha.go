package settingsbootstrap

import (
	"github.com/homles698698-lab/XMall/internal/config"
	captchaapp "github.com/homles698698-lab/XMall/internal/modules/captcha/application"
	settingsapp "github.com/homles698698-lab/XMall/internal/modules/settings/application"
	settingssecurity "github.com/homles698698-lab/XMall/internal/modules/settings/schema/security"
)

type settingsCaptchaAdapter struct {
	settings *settingsapp.Service
	cfg      *config.Config
	captcha  *captchaapp.Service
}

func (a settingsCaptchaAdapter) GetCaptchaSetting() (settingssecurity.CaptchaSetting, error) {
	return a.settings.GetCaptchaSetting(a.cfg.Captcha)
}

func (a settingsCaptchaAdapter) PatchCaptchaSetting(patch settingssecurity.CaptchaSettingPatch) (settingssecurity.CaptchaSetting, error) {
	return a.settings.PatchCaptchaSetting(a.cfg.Captcha, patch)
}

func (a settingsCaptchaAdapter) ApplyRuntime(setting settingssecurity.CaptchaSetting) {
	a.cfg.Captcha = settingssecurity.CaptchaSettingToConfig(setting)
	if a.captcha != nil {
		a.captcha.SetDefaultConfig(a.cfg.Captcha)
		a.captcha.InvalidateCache()
	}
}
