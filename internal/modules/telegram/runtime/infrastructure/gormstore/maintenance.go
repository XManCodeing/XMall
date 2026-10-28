package gormstore

import (
	"context"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// Cleanup removes sensitive payloads without removing deduplication tombstones.
// Completed updates are no longer needed once their deterministic replies exist.
func (s *Store) Cleanup(ctx context.Context, botID uint) error {
	db := s.db.WithContext(ctx)
	if err := db.Model(&d.Inbox{}).Where("bot_id = ? AND status = ? AND payload_cipher <> ?", botID, "done", "").Update("payload_cipher", "").Error; err != nil {
		return err
	}
	if err := db.Model(&d.Session{}).Where("bot_id = ? AND expires_at < ? AND data_cipher <> ?", botID, time.Now(), "").Update("data_cipher", "").Error; err != nil {
		return err
	}
	// Failed/unknown deliveries remain inspectable for seven days before removing
	// their encrypted body; status/reference metadata is retained for audit.
	if err := db.Model(&d.Delivery{}).Where("bot_id = ? AND status IN ? AND updated_at < ? AND payload_cipher <> ?", botID, []string{"sent", "failed", "unknown"}, time.Now().Add(-7*24*time.Hour), "").Update("payload_cipher", "").Error; err != nil {
		return err
	}
	return db.Model(&d.Inbox{}).Where("bot_id = ? AND status = ? AND updated_at < ? AND payload_cipher <> ?", botID, "failed", time.Now().Add(-7*24*time.Hour), "").Update("payload_cipher", "").Error
}
func (s *Store) DeliveryCounts(ctx context.Context, botID uint) (int64, int64, error) {
	var pending, unknown int64
	if err := s.db.WithContext(ctx).Model(&d.Delivery{}).Where("bot_id = ? AND status IN ?", botID, []string{"pending", "retry", "processing"}).Count(&pending).Error; err != nil {
		return 0, 0, err
	}
	err := s.db.WithContext(ctx).Model(&d.Delivery{}).Where("bot_id = ? AND status = ?", botID, "unknown").Count(&unknown).Error
	return pending, unknown, err
}
func (s *Store) RecordSent(ctx context.Context, botID uint, generation uint64) error {
	return s.db.WithContext(ctx).Model(&d.Instance{}).Where("id = ? AND generation = ?", botID, generation).Update("last_sent_at", time.Now()).Error
}
