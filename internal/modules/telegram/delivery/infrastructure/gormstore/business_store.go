package gormstore

import (
	"errors"
	"strconv"
	"strings"
	"time"

	identitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"

	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BusinessStore struct{ db *gorm.DB }

func NewBusinessStore(db *gorm.DB) *BusinessStore { return &BusinessStore{db: db} }

var _ delivery.BusinessStore = (*BusinessStore)(nil)
var _ delivery.DispatcherStore = (*BusinessStore)(nil)
var _ delivery.CurrentSourceResolver = (*BusinessStore)(nil)

// ResolveCurrentSource deliberately ignores login claims, binding history, and
// old orders. All reads and row locks use the supplied business transaction.
func (s *BusinessStore) ResolveCurrentSource(userID, botID uint) (*delivery.Source, error) {
	if userID == 0 {
		return nil, nil
	}
	var user userdomain.User
	err := s.db.Select("id", "status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", userID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if user.Status != "active" {
		return nil, nil
	}
	var identities []identitydomain.Identity
	if err := s.db.Select("id", "provider_user_id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND provider = ?", userID, "telegram").Limit(2).Find(&identities).Error; err != nil {
		return nil, err
	}
	if len(identities) != 1 {
		return nil, nil
	}
	tg, err := strconv.ParseInt(identities[0].ProviderUserID, 10, 64)
	if err != nil || tg <= 0 || strconv.FormatInt(tg, 10) != identities[0].ProviderUserID {
		return nil, nil
	}
	var contacts []runtimedomain.Contact
	query := s.db.Model(&runtimedomain.Contact{}).
		Select("telegram_bot_contacts.*").
		Joins("JOIN telegram_bot_instances AS bot ON bot.id = telegram_bot_contacts.bot_id").
		Where("telegram_bot_contacts.user_id = ? AND telegram_bot_contacts.telegram_user_id = ? AND telegram_bot_contacts.chat_id = ? AND telegram_bot_contacts.reachable = ?", userID, tg, tg, true).
		Where("bot.enabled = ? AND bot.mode = ?", true, "internal")
	if botID != 0 {
		query = query.Where("bot.id = ?", botID)
	}
	if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Limit(2).Find(&contacts).Error; err != nil {
		return nil, err
	}
	if len(contacts) != 1 {
		return nil, nil
	}
	return &delivery.Source{BotID: contacts[0].BotID, TelegramID: tg, PrivateChatID: contacts[0].ChatID}, nil
}

func (s *BusinessStore) FindOperation(key string) (*delivery.OrderOperation, error) {
	var row delivery.OrderOperation
	err := s.db.Where("operation_key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}
func (s *BusinessStore) ReserveOperation(key, hash string, userID uint) (*delivery.OrderOperation, bool, error) {
	row := delivery.OrderOperation{OperationKey: key, RequestHash: hash, UserID: userID}
	result := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "operation_key"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return &row, true, nil
	}
	existing, err := s.FindOperation(key)
	if err != nil {
		return nil, false, err
	}
	if existing == nil || existing.RequestHash != hash || existing.UserID != userID {
		return nil, false, delivery.ErrOperationConflict
	}
	return existing, false, nil
}
func (s *BusinessStore) CompleteOperation(key string, orderID uint) error {
	return s.db.Model(&delivery.OrderOperation{}).Where("operation_key = ? AND order_id = 0", key).Update("order_id", orderID).Error
}
func (s *BusinessStore) SaveSource(orderID, userID uint, source delivery.Source) error {
	if err := source.Validate(); err != nil {
		return err
	}
	return s.db.Create(&delivery.OrderSource{OrderID: orderID, UserID: userID, BotID: source.BotID, TelegramID: source.TelegramID, PrivateChatID: source.PrivateChatID}).Error
}
func (s *BusinessStore) FindSource(orderID uint) (*delivery.OrderSource, error) {
	var source delivery.OrderSource
	err := s.db.Where("order_id = ?", orderID).First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &source, err
}
func (s *BusinessStore) Append(event *delivery.Event) error {
	if event == nil || strings.TrimSpace(event.EventKey) == "" || event.UserID == 0 {
		return delivery.ErrInvalidSource
	}
	if err := (delivery.Source{BotID: event.BotID, TelegramID: event.TelegramID, PrivateChatID: event.PrivateChatID}).Validate(); err != nil {
		return err
	}
	now := time.Now()
	event.Status = "pending"
	event.AvailableAt = now
	event.CreatedAt = now
	event.UpdatedAt = now
	return s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_key"}}, DoNothing: true}).Create(event).Error
}
func ready(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Where("(status = ? AND available_at <= ?) OR (status = ? AND lease_until <= ?)", "pending", now, "processing", now)
}
func (s *BusinessStore) Scan(now time.Time, limit int) ([]delivery.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var events []delivery.Event
	err := ready(s.db, now).Order("id ASC").Limit(limit).Find(&events).Error
	return events, err
}
func (s *BusinessStore) ScanBot(botID uint, now time.Time, limit int) ([]delivery.Event, error) {
	if botID == 0 {
		return nil, delivery.ErrInvalidSource
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var events []delivery.Event
	err := ready(s.db.Where("bot_id = ?", botID), now).Order("id ASC").Limit(limit).Find(&events).Error
	return events, err
}

func (s *BusinessStore) Claim(id uint, owner string, now time.Time, ttl time.Duration) (bool, error) {
	if owner == "" || ttl <= 0 {
		return false, delivery.ErrLeaseLost
	}
	result := ready(s.db.Model(&delivery.Event{}).Where("id = ?", id), now).Updates(map[string]any{"status": "processing", "lease_owner": owner, "lease_until": now.Add(ttl), "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
	return result.RowsAffected == 1, result.Error
}
func (s *BusinessStore) finish(id uint, owner string, now time.Time, updates map[string]any) error {
	updates["lease_owner"] = ""
	updates["lease_until"] = nil
	updates["updated_at"] = now
	result := s.db.Model(&delivery.Event{}).Where("id = ? AND status = ? AND lease_owner = ? AND lease_until > ?", id, "processing", owner, now).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return delivery.ErrLeaseLost
	}
	return nil
}
func (s *BusinessStore) MarkDelivered(id uint, owner string, now time.Time) error {
	return s.finish(id, owner, now, map[string]any{"status": "delivered", "delivered_at": now})
}
func (s *BusinessStore) Retry(id uint, owner string, now, availableAt time.Time) error {
	return s.finish(id, owner, now, map[string]any{"status": "pending", "available_at": availableAt})
}
