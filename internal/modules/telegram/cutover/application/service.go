package cutover

import (
	"context"
	"errors"
)

var ErrBlocked = errors.New("telegram cutover blocked")

type Instance struct {
	BotID, ChannelID uint
	Version          uint64
	Mode             string
	Enabled          bool
}
type Store interface {
	Target(context.Context, uint) (Instance, error)
	ByChannel(context.Context, uint) (*Instance, error)
	Switch(context.Context, Instance, string, bool) error
}

// Network must redact credentials and webhook URLs in returned errors.
type Network interface {
	HasWebhook(context.Context, uint) (bool, error)
	DeleteWebhook(context.Context, uint, bool) error
}
type Request struct {
	ChannelID                uint   `json:"channel_id" binding:"required"`
	BotID                    uint   `json:"bot_id" binding:"required"`
	Version                  uint64 `json:"version" binding:"required"`
	ConfirmExternalStopped   bool   `json:"confirm_external_stopped"`
	ConfirmDeleteWebhook     bool   `json:"confirm_delete_webhook"`
	ConfirmRevokeCredentials bool   `json:"confirm_revoke_credentials"`
	ConfirmArchivedIsolation bool   `json:"confirm_archived_isolation"`
}
type Inventory struct {
	Checked          bool   `json:"checked"`
	Scope            string `json:"scope"`
	UnfinishedOrders int64  `json:"unfinished_orders"`
	Pending          int64  `json:"pending"`
	Retry            int64  `json:"retry"`
	Scheduled        int64  `json:"scheduled"`
	Archived         int64  `json:"archived"`
	Active           int64  `json:"active"`
}

// Readiness must inventory every legacy queue, including tasks without a channel.
// Conservative global counts are acceptable; incomplete snapshots are not.
type Readiness interface {
	Inspect(context.Context, uint) (Inventory, error)
	RevokeCredentials(context.Context, uint) error
}

type ImportRequest struct {
	ChannelID  uint  `json:"channel_id" binding:"required"`
	TelegramID int64 `json:"telegram_id" binding:"required,gt=0"`
}
type ImportResult struct {
	BotID            uint   `json:"bot_id"`
	ChannelID        uint   `json:"channel_id"`
	Version          uint64 `json:"version"`
	Mode             string `json:"mode"`
	ContactsImported int    `json:"contacts_imported"`
	ContactsPolicy   string `json:"contacts_policy"`
}
type Importer interface {
	Import(context.Context, ImportRequest) (ImportResult, error)
}

type Preview struct {
	ChannelID                    uint      `json:"channel_id"`
	BotID                        uint      `json:"bot_id"`
	Version                      uint64    `json:"version"`
	Mode                         string    `json:"mode"`
	Enabled                      bool      `json:"enabled"`
	NetworkChecked               bool      `json:"network_checked"`
	RequiresExternalStop         bool      `json:"requires_external_stop"`
	RequiresCredentialRevocation bool      `json:"requires_credential_revocation"`
	Inventory                    Inventory `json:"inventory"`
	Blockers                     []string  `json:"blockers"`
}
type Service struct {
	Store     Store
	Network   Network
	Readiness Readiness
	Importer  Importer
}

func (s *Service) Import(ctx context.Context, r ImportRequest) (ImportResult, error) {
	if s == nil || s.Importer == nil || r.ChannelID == 0 || r.TelegramID <= 0 {
		return ImportResult{}, ErrBlocked
	}
	return s.Importer.Import(ctx, r)
}
func (s *Service) inventory(ctx context.Context, id uint) (Inventory, []string) {
	if s.Readiness == nil {
		return Inventory{}, []string{"inventory_unavailable"}
	}
	v, err := s.Readiness.Inspect(ctx, id)
	if err != nil || !v.Checked || v.Scope == "" || v.UnfinishedOrders < 0 || v.Pending < 0 || v.Retry < 0 || v.Scheduled < 0 || v.Archived < 0 || v.Active < 0 {
		return Inventory{}, []string{"inventory_unavailable"}
	}
	blockers := []string{}
	if v.UnfinishedOrders != 0 {
		blockers = append(blockers, "unfinished_orders")
	}
	if v.Pending+v.Retry+v.Scheduled+v.Active != 0 {
		blockers = append(blockers, "legacy_queue_not_drained")
	}
	return v, blockers
}

func (s *Service) target(ctx context.Context, r Request) (Instance, error) {
	if s == nil || s.Store == nil || r.ChannelID == 0 || r.BotID == 0 || r.Version == 0 {
		return Instance{}, ErrBlocked
	}
	i, err := s.Store.Target(ctx, r.BotID)
	if err != nil {
		return Instance{}, err
	}
	if i.BotID != r.BotID || i.ChannelID != r.ChannelID || i.Version != r.Version ||
		(i.Mode != "external" && i.Mode != "cutover_pending" && i.Mode != "internal") ||
		(i.Enabled && i.Mode != "internal") {
		return Instance{}, ErrBlocked
	}
	mapped, err := s.Store.ByChannel(ctx, r.ChannelID)
	if err != nil {
		return Instance{}, err
	}
	if mapped == nil || *mapped != i {
		return Instance{}, ErrBlocked
	}
	return i, nil
}

func (s *Service) Preflight(ctx context.Context, r Request) (Preview, error) {
	i, err := s.target(ctx, r)
	if err != nil {
		return Preview{}, err
	}
	v, blockers := s.inventory(ctx, r.ChannelID)
	return Preview{ChannelID: r.ChannelID, BotID: r.BotID, Version: i.Version, Mode: i.Mode, Enabled: i.Enabled, RequiresExternalStop: i.Mode != "internal", RequiresCredentialRevocation: i.Mode != "internal", Inventory: v, Blockers: blockers}, nil
}

func (s *Service) Apply(ctx context.Context, r Request) error {
	i, err := s.target(ctx, r)
	if err != nil {
		return err
	}
	// A completed cutover is a no-op, including when polling was enabled later.
	// Callers must reload the current version after an interrupted operation;
	// stale versions never authorize an external effect.
	if i.Mode == "internal" {
		return nil
	}
	if !r.ConfirmExternalStopped || !r.ConfirmRevokeCredentials || s.Network == nil {
		return ErrBlocked
	}
	inventory, blockers := s.inventory(ctx, r.ChannelID)
	if len(blockers) != 0 || (inventory.Archived != 0 && !r.ConfirmArchivedIsolation) {
		return ErrBlocked
	}
	// Persist the old-entry fence before touching Telegram. Use the exact next
	// version, rather than accepting arbitrary concurrent configuration changes.
	if i.Mode == "external" {
		if err = s.Store.Switch(ctx, i, "cutover_pending", false); err != nil {
			return err
		}
		r.Version = i.Version + 1
		i, err = s.target(ctx, r)
		if err != nil || i.Mode != "cutover_pending" {
			return ErrBlocked
		}
	}
	// Fence survives revocation/network failures; resume with the latest version.
	if err = s.Readiness.RevokeCredentials(ctx, r.ChannelID); err != nil {
		return ErrBlocked
	}
	inventory, blockers = s.inventory(ctx, r.ChannelID)
	if len(blockers) != 0 || (inventory.Archived != 0 && !r.ConfirmArchivedIsolation) {
		return ErrBlocked
	}
	hook, err := s.Network.HasWebhook(ctx, r.BotID)
	if err != nil {
		return ErrBlocked
	}
	if hook {
		if !r.ConfirmDeleteWebhook {
			return ErrBlocked
		}
		// Revalidate after network preflight before its destructive operation.
		current, checkErr := s.target(ctx, r)
		if checkErr != nil || current != i {
			return ErrBlocked
		}
		if err = s.Network.DeleteWebhook(ctx, r.BotID, false); err != nil {
			return ErrBlocked
		}
		hook, err = s.Network.HasWebhook(ctx, r.BotID)
		if err != nil || hook {
			return ErrBlocked
		}
	}
	inventory, blockers = s.inventory(ctx, r.ChannelID)
	if len(blockers) != 0 || (inventory.Archived != 0 && !r.ConfirmArchivedIsolation) {
		return ErrBlocked
	}
	// Enabling polling remains an explicit, separately audited action.
	return s.Store.Switch(ctx, i, "internal", false)
}

// LegacyAllowed affects only explicitly associated migrated channels. Unrelated
// generic channels remain usable; lookup errors fail closed.
func (s *Service) LegacyAllowed(ctx context.Context, channelID uint) (bool, error) {
	if s == nil || s.Store == nil || channelID == 0 {
		return false, ErrBlocked
	}
	i, err := s.Store.ByChannel(ctx, channelID)
	if err != nil {
		return false, err
	}
	return i == nil || i.Mode == "external", nil
}
