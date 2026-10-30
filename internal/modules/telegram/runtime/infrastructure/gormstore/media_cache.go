package gormstore

import (
	"context"
	"errors"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CachedMedia(ctx context.Context, bot uint, pathHash, contentHash string) (string, error) {
	var row d.MediaCache
	err := s.db.WithContext(ctx).Where("bot_id = ? AND path_hash = ? AND content_hash = ?", bot, pathHash, contentHash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.FileID, err
}
func (s *Store) CacheMedia(ctx context.Context, bot uint, pathHash, contentHash, fileID string) error {
	if fileID == "" {
		return nil
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "bot_id"}, {Name: "path_hash"}, {Name: "content_hash"}}, DoUpdates: clause.Assignments(map[string]any{"file_id": fileID, "updated_at": time.Now()})}).Create(&d.MediaCache{BotID: bot, PathHash: pathHash, ContentHash: contentHash, FileID: fileID}).Error
}
