package application

import (
	"context"
	"strconv"
	"strings"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	botreplacement "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// GroupPreviewer exposes only read-only platform/capability preflight, not route mutation.
type GroupPreviewer interface {
	PreviewReplacement(context.Context, groups.ReplacementInput) (*groups.ReplacementPreview, error)
}

// Service persists trusted previews. Execution is deliberately a separate capability;
// callers cannot confirm or switch a route through this preview-only service.
type Service struct {
	Bots     *botreplacement.Service
	Identity *IdentityExecutor
	Store    c.Store
	Groups   GroupPreviewer
}

// GroupPreflightInput contains administrator intent; source identity and scope are server-derived.
type GroupPreflightInput struct {
	groups.ReplacementInput
	RequestKey string `json:"request_key"`
	Reason     string `json:"reason"`
}

// PreviewGroup validates input, checks the destination outside transactions, and saves the task.
// actor is obtained from authenticated backend context, never a JSON field. The returned task
// remains awaiting_confirmation until an actual recovery executor is available.
func (service *Service) PreviewGroup(ctx context.Context, actor uint, input GroupPreflightInput) (*d.Task, error) {
	if actor == 0 || service.Store == nil || service.Groups == nil || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 512 || !validRequestKey(input.RequestKey) {
		return nil, c.ErrInvalid
	}
	preview, err := service.Groups.PreviewReplacement(ctx, input.ReplacementInput)
	if err != nil {
		return nil, err
	}
	// A cross-Bot replacement needs full instance/module conflict checks and cannot be
	// smuggled through the single-group preflight before that executor is implemented.
	if preview.Source.BotInstanceID != input.NewBotInstanceID {
		return nil, c.ErrInvalid
	}
	chat, err := strconv.ParseInt(input.NewTelegramChatID, 10, 64)
	if err != nil {
		return nil, c.ErrInvalid
	}
	botID := strconv.FormatUint(uint64(preview.Source.BotInstanceID), 10)
	targetID := strconv.FormatUint(uint64(preview.Source.ID), 10)
	snapshot := d.Snapshot{
		Scope:       d.Scope{TargetIDs: []string{targetID}, BotIDs: []string{botID}, ChatIDs: []string{strconv.FormatInt(preview.Source.TelegramChatID, 10), strconv.FormatInt(chat, 10)}},
		Old:         d.Endpoint{TargetID: preview.Source.ID, BotID: preview.Source.BotInstanceID, ChatID: preview.Source.TelegramChatID},
		New:         d.Endpoint{TargetID: preview.Source.ID, BotID: input.NewBotInstanceID, ChatID: chat},
		ExpectedOld: d.Versions{Config: preview.Source.Version, Route: preview.Source.RouteVersion},
		ExpectedNew: d.Versions{Generation: preview.Destination.Generation},
	}
	return service.Store.CreatePreflight(ctx, c.PreflightInput{Kind: d.GroupReplace, RequestKey: input.RequestKey, Snapshot: snapshot, ActorID: actor, Reason: strings.TrimSpace(input.Reason), TTL: preview.ExpiresAt.Sub(preview.CheckedAt)})
}

// validRequestKey accepts bounded opaque ASCII keys without URLs, whitespace or secrets.
func validRequestKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_-.:", character)) {
			return false
		}
	}
	return true
}
