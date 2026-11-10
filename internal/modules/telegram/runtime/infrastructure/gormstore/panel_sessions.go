package gormstore

import (
	"context"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

var _ c.PanelSessionReader = (*Store)(nil)

// PanelSessionsAfter returns a bounded exclusive-cursor page of existing private
// panels. Pending intent stays encrypted; no new table or business state is added.
func (s *Store) PanelSessionsAfter(ctx context.Context, bot, after uint, limit int, at time.Time) ([]d.Session, error) {
	if bot == 0 || limit < 1 || limit > 100 || at.IsZero() {
		return nil, c.ErrConflict
	}
	var rows []d.Session
	err := s.db.WithContext(ctx).Where("bot_id = ? AND id > ? AND expires_at > ? AND telegram_user_id = chat_id AND panel_message_id > 0", bot, after, at.UTC()).
		Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}
