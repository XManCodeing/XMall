package telegrambroadcast

import (
	"context"
	"strings"

	channelapp "github.com/XManCodeing/XMall/internal/modules/channelclient/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	cutover "github.com/XManCodeing/XMall/internal/modules/telegram/cutover/application"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// NewCutover shares the runtime's store, cipher and API; constructing it does not
// contact Telegram. An unconfigured runtime remains fail-closed.
func NewCutover(manager *runtimeapp.Manager) *cutover.Service {
	if manager == nil || manager.Store == nil {
		return &cutover.Service{}
	}
	return &cutover.Service{Store: CutoverStore{Store: manager.Store}, Network: cutoverNetwork{manager}}
}

// NewManagedCutover wires explicit imports and conservative readiness checks.
// Counts are global upper bounds: legacy records lack a proven per-bot source.
func NewManagedCutover(manager *runtimeapp.Manager, channels *channelapp.Service, orders ordercontract.Store, redisClient *redis.Client) *cutover.Service {
	s := NewCutover(manager)
	s.Importer = legacyImporter{manager: manager, channels: channels}
	readiness := &legacyReadiness{channels: channels, orders: orders}
	if redisClient != nil {
		readiness.queues = asynq.NewInspectorFromRedisClient(redisClient)
	}
	s.Readiness = readiness
	return s
}

type legacyImporter struct {
	manager  *runtimeapp.Manager
	channels *channelapp.Service
}

func (a legacyImporter) Import(ctx context.Context, r cutover.ImportRequest) (cutover.ImportResult, error) {
	blocked := func() (cutover.ImportResult, error) { return cutover.ImportResult{}, cutover.ErrBlocked }
	if a.manager == nil || a.manager.API == nil || a.manager.Store == nil || a.channels == nil || r.ChannelID == 0 || r.TelegramID <= 0 {
		return blocked()
	}
	client, err := a.channels.GetChannelClient(r.ChannelID)
	if err != nil || client == nil || client.ChannelType != "telegram_bot" || client.DeletedAt != nil {
		return blocked()
	}
	token, err := a.channels.DecryptBotToken(client)
	if err != nil || strings.TrimSpace(token) == "" {
		return blocked()
	}
	identity, err := a.manager.API.Identity(ctx, token)
	if err != nil || identity.ID != r.TelegramID {
		return blocked()
	}
	// Re-read the exact source after getMe; never bind a concurrently replaced credential.
	current, err := a.channels.GetChannelClient(r.ChannelID)
	if err != nil || current == nil || current.ChannelType != "telegram_bot" || current.DeletedAt != nil || current.BotToken != client.BotToken {
		return blocked()
	}
	store, ok := a.manager.Store.(interface {
		ImportLegacy(context.Context, *runtimedomain.Instance, string) (*runtimedomain.Instance, error)
	})
	if !ok {
		return blocked()
	}
	cipher, err := a.manager.Seal(token)
	if err != nil {
		return blocked()
	}
	id := r.ChannelID
	input := &runtimedomain.Instance{TelegramID: identity.ID, Username: identity.Username, ChannelClientID: &id, TokenCipher: cipher, Mode: "external", Version: 1, State: "stopped"}
	existing, err := store.ImportLegacy(ctx, input, client.BotToken)
	if err != nil {
		return blocked()
	}
	return cutover.ImportResult{BotID: existing.ID, ChannelID: r.ChannelID, Version: existing.Version, Mode: existing.Mode, ContactsPolicy: "not_imported_no_proven_source; users_must_start_bot"}, nil
}

type queueInventory interface {
	Queues() ([]string, error)
	GetQueueInfo(string) (*asynq.QueueInfo, error)
}
type legacyReadiness struct {
	channels *channelapp.Service
	orders   ordercontract.Store
	queues   queueInventory
}

func (r *legacyReadiness) Inspect(ctx context.Context, channelID uint) (cutover.Inventory, error) {
	v := cutover.Inventory{Scope: "global_all_orders_and_all_queue_tasks_upper_bound"}
	if r == nil || r.channels == nil || r.orders == nil || r.queues == nil || ctx.Err() != nil {
		return v, cutover.ErrBlocked
	}
	client, err := r.channels.GetChannelClient(channelID)
	if err != nil || client == nil || client.ChannelType != "telegram_bot" || client.DeletedAt != nil {
		return v, cutover.ErrBlocked
	}
	// Unknown statuses count as unfinished, rather than silently escaping a list.
	_, total, err := r.orders.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 1})
	if err != nil {
		return v, cutover.ErrBlocked
	}
	v.UnfinishedOrders = total
	for _, status := range []string{"completed", "canceled", "refunded", "delivered"} {
		_, count, err := r.orders.ListAdmin(ordercontract.ListFilter{Page: 1, PageSize: 1, Status: status})
		if err != nil {
			return v, cutover.ErrBlocked
		}
		v.UnfinishedOrders -= count
	}
	queues, err := r.queues.Queues()
	if err != nil {
		return v, cutover.ErrBlocked
	}
	for _, name := range queues {
		if ctx.Err() != nil {
			return v, cutover.ErrBlocked
		}
		info, err := r.queues.GetQueueInfo(name)
		if err != nil || info == nil {
			return v, cutover.ErrBlocked
		}
		v.Pending += int64(info.Pending)
		v.Retry += int64(info.Retry)
		v.Scheduled += int64(info.Scheduled)
		v.Archived += int64(info.Archived)
		v.Active += int64(info.Active + info.Aggregating)
	}
	v.Checked = true
	return v, nil
}
func (r *legacyReadiness) RevokeCredentials(ctx context.Context, id uint) error {
	if r == nil || r.channels == nil || ctx.Err() != nil {
		return cutover.ErrBlocked
	}
	client, err := r.channels.GetChannelClient(id)
	if err != nil || client == nil || client.ChannelType != "telegram_bot" || client.DeletedAt != nil {
		return cutover.ErrBlocked
	}
	// Rotate the dedicated API secret, then disable the channel. Rotation also
	// prevents an accidental later re-enable from restoring the old credential.
	if _, err = r.channels.ResetChannelClientSecret(id); err != nil {
		return cutover.ErrBlocked
	}
	if err = r.channels.UpdateChannelClientStatus(id, 0); err != nil {
		return cutover.ErrBlocked
	}
	return nil
}

type cutoverNetwork struct{ manager *runtimeapp.Manager }

func (n cutoverNetwork) token(ctx context.Context, id uint) (string, error) {
	if n.manager == nil || n.manager.Store == nil || n.manager.API == nil {
		return "", cutover.ErrBlocked
	}
	i, err := n.manager.Store.Get(ctx, id)
	if err != nil || i == nil || i.Enabled || i.Mode != "cutover_pending" {
		return "", cutover.ErrBlocked
	}
	token, err := n.manager.Token(i)
	if err != nil || token == "" {
		return "", cutover.ErrBlocked
	}
	return token, nil
}
func (n cutoverNetwork) HasWebhook(ctx context.Context, id uint) (bool, error) {
	token, err := n.token(ctx, id)
	if err != nil {
		return false, err
	}
	url, err := n.manager.API.Webhook(ctx, token)
	if err != nil {
		return false, cutover.ErrBlocked
	}
	return url != "", nil
}
func (n cutoverNetwork) DeleteWebhook(ctx context.Context, id uint, drop bool) error {
	if drop {
		return cutover.ErrBlocked
	}
	token, err := n.token(ctx, id)
	if err != nil {
		return err
	}
	api, ok := n.manager.API.(interface {
		DeleteWebhook(context.Context, string, bool) error
	})
	if !ok || api.DeleteWebhook(ctx, token, false) != nil {
		return cutover.ErrBlocked
	}
	return nil
}

type CutoverStore struct{ Store runtimecontract.Store }

func cutoverView(i runtimedomain.Instance) cutover.Instance {
	var channel uint
	if i.ChannelClientID != nil {
		channel = *i.ChannelClientID
	}
	return cutover.Instance{BotID: i.ID, ChannelID: channel, Version: i.Version, Mode: i.Mode, Enabled: i.Enabled}
}
func (s CutoverStore) Target(ctx context.Context, id uint) (cutover.Instance, error) {
	i, err := s.Store.Get(ctx, id)
	if err != nil {
		return cutover.Instance{}, err
	}
	if i == nil {
		return cutover.Instance{}, cutover.ErrBlocked
	}
	return cutoverView(*i), nil
}
func (s CutoverStore) ByChannel(ctx context.Context, id uint) (*cutover.Instance, error) {
	items, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var found *cutover.Instance
	for _, i := range items {
		if i.ChannelClientID != nil && *i.ChannelClientID == id {
			if found != nil {
				return nil, cutover.ErrBlocked
			}
			v := cutoverView(i)
			found = &v
		}
	}
	return found, nil
}
func (s CutoverStore) Switch(ctx context.Context, i cutover.Instance, mode string, enabled bool) error {
	current, err := s.Target(ctx, i.BotID)
	if err != nil {
		return err
	}
	if current != i {
		return cutover.ErrBlocked
	}
	store, ok := s.Store.(interface {
		SwitchCutover(context.Context, uint, uint64, string) error
	})
	if !ok || enabled {
		return cutover.ErrBlocked
	}
	return store.SwitchCutover(ctx, i.BotID, i.Version, mode)
}
