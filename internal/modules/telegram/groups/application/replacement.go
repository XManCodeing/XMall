package application

import (
	"context"
	"strconv"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// PreviewReplacement checks a proposed destination without changing configuration or Telegram state.
// input pins the source versions and exact destination IDs. It returns a bounded capability
// snapshot or an explicit validation/conflict/capability error; no database transaction spans I/O.
func (service *Service) PreviewReplacement(ctx context.Context, input c.ReplacementInput) (*c.ReplacementPreview, error) {
	chat, err := strconv.ParseInt(input.NewTelegramChatID, 10, 64)
	if err != nil || chat >= 0 || strconv.FormatInt(chat, 10) != input.NewTelegramChatID || input.TargetID == 0 || input.NewBotInstanceID == 0 || input.ExpectedVersion == 0 || input.ExpectedRouteVersion == 0 || service.Store == nil || service.Probe == nil {
		return nil, c.ErrInvalid
	}
	source, err := service.replacementSource(ctx, input)
	if err != nil {
		return nil, err
	}
	if source.BotInstanceID == input.NewBotInstanceID && source.TelegramChatID == chat {
		return nil, c.ErrInvalid
	}
	if err := service.replacementOccupancy(ctx, source.ID, chat); err != nil {
		return nil, err
	}
	identity, err := service.Probe.Instance(ctx, input.NewBotInstanceID)
	if err != nil {
		return nil, err
	}
	if !identity.Running || identity.TelegramID <= 0 || identity.Generation == 0 {
		return nil, c.ErrCapability
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	checkedAt := service.Now().UTC()
	result := service.Probe.Check(checkCtx, input.NewBotInstanceID, chat)
	if result.ErrorClass != "" {
		return nil, c.ErrCapability
	}
	observation := result.Observation
	if observation.BotTelegramID != identity.TelegramID || observation.Generation != identity.Generation || !observation.IsMember || len(Missing(d.Target{Purpose: source.Purpose, Observation: observation})) != 0 {
		return nil, c.ErrCapability
	}
	// Re-read after platform I/O: neither the source nor instance generation may drift.
	current, err := service.replacementSource(ctx, input)
	if err != nil {
		return nil, err
	}
	if current.BotInstanceID != source.BotInstanceID || current.TelegramChatID != source.TelegramChatID || current.Purpose != source.Purpose {
		return nil, c.ErrConflict
	}
	latest, err := service.Probe.Instance(ctx, input.NewBotInstanceID)
	if err != nil {
		return nil, err
	}
	if latest != identity {
		return nil, c.ErrConflict
	}
	if err := service.replacementOccupancy(ctx, source.ID, chat); err != nil {
		return nil, err
	}
	observation.State, observation.ErrorClass, observation.Source = "ready", "", "migration_preflight"
	observation.CheckedAt, observation.ObservedAt = &checkedAt, &checkedAt
	return &c.ReplacementPreview{Input: input, Source: *source, Destination: observation, CheckedAt: checkedAt, ExpiresAt: checkedAt.Add(Freshness)}, nil
}

// replacementSource loads the exact source versions for input, rejecting invalid purposes.
// The result remains an observation only; the actual switch must use a transactional CAS.
func (service *Service) replacementSource(ctx context.Context, input c.ReplacementInput) (*d.Target, error) {
	source, err := service.Store.Get(ctx, input.TargetID)
	if err != nil {
		return nil, err
	}
	if source == nil || source.ID != input.TargetID || source.Version != input.ExpectedVersion || source.RouteVersion != input.ExpectedRouteVersion || source.RecoveryTaskID != 0 {
		return nil, c.ErrConflict
	}
	if source.Purpose != "membership" && source.Purpose != "support" {
		return nil, c.ErrInvalid
	}
	return source, nil
}

// replacementOccupancy rejects a destination already owned by another logical target.
// targetID may retain its own chat during a Bot replacement; database uniqueness remains final authority.
func (service *Service) replacementOccupancy(ctx context.Context, targetID uint, chat int64) error {
	rows, total, err := service.Store.List(ctx, c.Filter{ChatID: chat, Page: 1, PageSize: 2})
	if err != nil {
		return err
	}
	if total > 1 || total != int64(len(rows)) {
		return c.ErrConflict
	}
	for _, row := range rows {
		if row.ID != targetID {
			return c.ErrConflict
		}
	}
	return nil
}
