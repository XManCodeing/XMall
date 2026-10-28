package contract

import (
	"errors"

	settingssecurity "github.com/XManCodeing/XMall/internal/modules/settings/schema/security"
)

var (
	ErrConfigInvalid = settingssecurity.ErrCaptchaConfigInvalid
	ErrRequired      = errors.New("captcha required")
	ErrInvalid       = errors.New("captcha invalid")
	ErrVerifyFailed  = errors.New("captcha verify failed")
)
