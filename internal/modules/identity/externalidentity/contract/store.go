package externalidentitycontract

import (
	"errors"
	"time"

	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
)

// ErrTelegramIdentityChanged 表示购买时的绑定代次或有效账号已变化。
var ErrTelegramIdentityChanged = errors.New("telegram transaction identity changed or unavailable")

// TelegramBinding 保存服务端验证的身份及绑定代次，不依赖 Bot 私聊来源。
// ID 是外部身份记录主键；解绑再绑定必须产生新代次。
type TelegramBinding struct {
	ID         uint
	TelegramID int64
}

// TransactionTelegramGuard 在调用方事务中锁定身份和账号直到提交。
// userID 是可信账号；binding 是预检快照；返回身份变化或数据库错误。
type TransactionTelegramGuard interface {
	LockTelegramBinding(userID uint, binding TelegramBinding) error
}

type TelegramUserFilter struct {
	Page             int
	PageSize         int
	Keyword          string
	DisplayName      string
	TelegramUsername string
	TelegramUserID   string
	CreatedFrom      *time.Time
	CreatedTo        *time.Time
	UserIDs          []uint
}

type TelegramUser struct {
	UserID           uint
	DisplayName      string
	UserEmail        string
	TelegramUsername string
	TelegramUserID   string
	BoundAt          time.Time
	UserCreatedAt    time.Time
}

type Store interface {
	GetByProviderUserID(provider, providerUserID string) (*externalidentitydomain.Identity, error)
	GetByUserProvider(userID uint, provider string) (*externalidentitydomain.Identity, error)
	ListByUserID(userID uint) ([]externalidentitydomain.Identity, error)
	ListTelegramUsers(filter TelegramUserFilter) ([]TelegramUser, int64, error)
	Create(*externalidentitydomain.Identity) error
	Update(*externalidentitydomain.Identity) error
	DeleteByID(id uint) error
}
