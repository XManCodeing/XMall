package gormstore

import (
	"context"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// Cleanup removes sensitive payloads without removing deduplication tombstones.
// Retained dependencies must be released before their encrypted body is eligible.
func (s *Store) Cleanup(ctx context.Context, botID uint) error {
	db := s.db.WithContext(ctx)
	now := time.Now().UTC()
	eligible := "payload_held = ? AND (payload_retain_until IS NULL OR payload_retain_until <= ?)"
	if err := db.Model(&d.Inbox{}).Where(eligible, false, now).Where("bot_id = ? AND status = ? AND payload_cipher <> ?", botID, "done", "").Update("payload_cipher", "").Error; err != nil {
		return err
	}
	// Inbox chat IDs live only in encrypted payloads. Conservatively retain every
	// expired session for this bot while any inbox can still replay. This also
	// preserves LegacyInputThrough after a reply/enqueue failure, including failed
	// or future unknown states; only done proves consumption has completed.
	// Keep the dependency check inside the UPDATE, not a racy read-before-clear.
	unfinished := db.Model(&d.Inbox{}).Select("1").Where("bot_id = ? AND status <> ?", botID, "done")
	// SaveSession normalizes expiry to UTC. SQLite compares stored timestamp text,
	// so using local wall time here would prematurely erase live callback state.
	if err := db.Model(&d.Session{}).Where("bot_id = ? AND expires_at < ? AND data_cipher <> ?", botID, now, "").Where("NOT EXISTS (?)", unfinished).Update("data_cipher", "").Error; err != nil {
		return err
	}
	// Unknown outcomes are never age-purged. Other terminal rows retain the
	// seven-day floor in addition to any explicit dependency hold or deadline.
	if err := db.Model(&d.Delivery{}).Where(eligible, false, now).Where("bot_id = ? AND status IN ? AND updated_at < ? AND payload_cipher <> ?", botID, []string{"sent", "failed", "superseded"}, now.Add(-7*24*time.Hour), "").Update("payload_cipher", "").Error; err != nil {
		return err
	}
	return db.Model(&d.Inbox{}).Where(eligible, false, now).Where("bot_id = ? AND status = ? AND updated_at < ? AND payload_cipher <> ?", botID, "failed", now.Add(-7*24*time.Hour), "").Update("payload_cipher", "").Error
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
