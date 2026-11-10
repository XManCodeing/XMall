package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// ConfirmDeletedTopic authorizes an exact POST and loads server-owned bot/mapping
// data. Input is an explicit verified fact plus CAS, never a Telegram delete call.
// Returns conflict for stale/unknown mappings and never guesses deletion on error.
func (s *AdminService) ConfirmDeletedTopic(ctx context.Context, actor, id uint, in c.AdminTopicDeletionInput) error {
	row, err := s.TopicOperation(ctx, actor, id)
	if err != nil {
		return err
	}
	if s.recoveryResolver == nil || s.generation == nil {
		return c.ErrAdminUnavailable
	}
	authority, ok := s.recoveryResolver.Authorizer.(TopicDeletionAuthorizer)
	if !ok {
		return c.ErrDenied
	}
	if err = authority.AuthorizeSupportTopicDeletion(ctx, actor, row.BotID); err != nil {
		return err
	}
	if !in.Confirmed || in.ExpectedVersion == 0 || in.ExpectedVersion >= uint64(1<<63-1) || in.MappingVersion == 0 || in.MappingID == 0 || in.ChatID >= 0 || in.ThreadID <= 0 || !adminEvidenceReference.MatchString(in.Evidence) {
		return c.ErrAdminInvalid
	}
	mapping := row.Mapping
	if row.Version != in.ExpectedVersion || (row.State != "succeeded" && row.State != "failed") || mapping == nil ||
		(mapping.State != "open" && mapping.State != "closed") || mapping.ID != in.MappingID || mapping.Version != in.MappingVersion || mapping.ChatID != in.ChatID || mapping.ThreadID != in.ThreadID {
		return c.ErrConflict
	}
	generation, err := s.generation(ctx, row.BotID)
	if err != nil {
		return err
	}
	if generation == 0 {
		return c.ErrConflict
	}
	return s.recoveryResolver.ConfirmTopicDeleted(ctx, row.BotID, mapping.ID, c.TopicDeletionConfirmation{
		OperationID: row.ID, OperationVersion: row.Version, ActorID: actor, Generation: generation, ExpectedVersion: mapping.Version,
		ChatID: mapping.ChatID, ThreadID: mapping.ThreadID, RouteVersion: mapping.RouteVersion, Evidence: in.Evidence,
	}, s.now().UTC())
}
