package channelclientcontract

import (
	"time"

	channelclientdomain "github.com/homles698698-lab/XMall/internal/modules/channelclient/domain"
)

type Store interface {
	Create(*channelclientdomain.Client) error
	FindByID(uint) (*channelclientdomain.Client, error)
	FindByChannelKey(string) (*channelclientdomain.Client, error)
	FindActiveByChannelType(string) (*channelclientdomain.Client, error)
	FindAll() ([]channelclientdomain.Client, error)
	Update(*channelclientdomain.Client) error
	UpdateLastUsed(uint, time.Time) error
	Delete(uint, time.Time) error
}
