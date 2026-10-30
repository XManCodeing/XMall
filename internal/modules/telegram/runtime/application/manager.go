package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type Manager struct {
	Store      c.Store
	API        c.API
	key        []byte
	readUpload func(string, int64) ([]byte, error)
}

func NewManager(store c.Store, api c.API, secret string) *Manager {
	return NewManagerWithMediaReader(store, api, secret, nil)
}

// NewManagerWithMediaReader injects the bounded upload reader from infrastructure.
func NewManagerWithMediaReader(store c.Store, api c.API, secret string, readUpload func(string, int64) ([]byte, error)) *Manager {
	manager := &Manager{Store: store, API: api, key: crypto.DeriveKey(secret), readUpload: readUpload}
	if guarded, ok := store.(interface {
		SetCredentialOpener(func(string) (string, error))
	}); ok {
		guarded.SetCredentialOpener(manager.Open)
	}
	return manager
}
func (m *Manager) Token(i *d.Instance) (string, error) { return crypto.Decrypt(m.key, i.TokenCipher) }
func (m *Manager) Seal(value string) (string, error)   { return crypto.Encrypt(m.key, value) }
func (m *Manager) Open(value string) (string, error)   { return crypto.Decrypt(m.key, value) }
func (m *Manager) Create(ctx context.Context, token string) (*d.Instance, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 256 {
		return nil, errors.New("bot token required")
	}
	identity, err := m.API.Identity(ctx, token)
	if err != nil {
		return nil, errors.New("telegram identity verification failed")
	}
	if identity.ID <= 0 {
		return nil, errors.New("invalid telegram bot identity")
	}
	cipher, err := m.Seal(token)
	if err != nil {
		return nil, err
	}
	instance := &d.Instance{TelegramID: identity.ID, Username: identity.Username, TokenCipher: cipher, Mode: "internal", Version: 1, ProfileVersion: 1, State: "stopped"}
	if err = m.Store.Create(ctx, instance); err != nil {
		return nil, err
	}
	return instance, nil
}
func (m *Manager) Configure(ctx context.Context, id uint, version uint64, enabled *bool, token *string) error {
	instance, err := m.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if instance.Version != version {
		return c.ErrConflict
	}
	fields := map[string]interface{}{}
	if token != nil {
		if len(strings.TrimSpace(*token)) == 0 || len(*token) > 256 {
			return errors.New("invalid bot token")
		}
		identity, err := m.API.Identity(ctx, strings.TrimSpace(*token))
		if err != nil {
			return errors.New("telegram identity verification failed")
		}
		if identity.ID != instance.TelegramID {
			return errors.New("different bot identity requires a new instance")
		}
		cipher, err := m.Seal(strings.TrimSpace(*token))
		if err != nil {
			return err
		}
		fields["token_cipher"] = cipher
		fields["username"] = identity.Username
	}
	if enabled != nil {
		if *enabled {
			if instance.Mode != "internal" {
				return c.ErrConflict
			}
			plain, err := m.Token(instance)
			if err != nil {
				return errors.New("bot credential unavailable")
			}
			if token != nil {
				plain = strings.TrimSpace(*token)
			}
			identity, err := m.API.Identity(ctx, plain)
			if err != nil || identity.ID != instance.TelegramID {
				return errors.New("telegram bot identity verification failed")
			}
			webhook, err := m.API.Webhook(ctx, plain)
			if err != nil {
				return errors.New("telegram webhook check failed")
			}
			if webhook != "" {
				return errors.New("existing webhook requires explicit takeover")
			}
		}
		fields["enabled"] = *enabled
	}
	if len(fields) == 0 {
		return errors.New("empty configuration change")
	}
	return m.Store.Configure(ctx, id, version, fields)
}
func (m *Manager) Test(ctx context.Context, id uint) (c.BotIdentity, bool, error) {
	instance, err := m.Store.Get(ctx, id)
	if err != nil {
		return c.BotIdentity{}, false, err
	}
	token, err := m.Token(instance)
	if err != nil {
		return c.BotIdentity{}, false, errors.New("bot credential unavailable")
	}
	identity, err := m.API.Identity(ctx, token)
	if err != nil {
		return c.BotIdentity{}, false, errors.New("telegram connection failed")
	}
	webhook, err := m.API.Webhook(ctx, token)
	if err != nil {
		return identity, false, errors.New("telegram webhook check failed")
	}
	return identity, webhook != "", nil
}

type InstanceView struct {
	*d.Instance
	TokenSet bool  `json:"token_set"`
	Backlog  int64 `json:"backlog"`
	Unknown  int64 `json:"unknown"`
	Fresh    bool  `json:"fresh"`
}

func (m *Manager) Status(ctx context.Context, id uint) (InstanceView, error) {
	i, err := m.Store.Get(ctx, id)
	if err != nil {
		return InstanceView{}, err
	}
	view := View(*i)
	if counters, ok := m.Store.(interface {
		DeliveryCounts(context.Context, uint) (int64, int64, error)
	}); ok {
		view.Backlog, view.Unknown, err = counters.DeliveryCounts(ctx, id)
	}
	return view, err
}
func View(i d.Instance) InstanceView {
	tokenSet := i.TokenCipher != ""
	i.TokenCipher = ""
	fresh := i.LastSeenAt != nil && time.Since(*i.LastSeenAt) < 45*time.Second
	// Enabled is desired configuration, not evidence that the worker has stopped.
	// A confirmed stop is terminal and does not require continuing heartbeats.
	if i.State != "stopped" && !fresh {
		i.State = "stale"
	}
	return InstanceView{Instance: &i, TokenSet: tokenSet, Fresh: fresh}
}
