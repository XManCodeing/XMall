package gormstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FindManualConfirmation 返回支付尝试的不可变人工审计；不存在返回 nil。
func (s *Store) FindManualConfirmation(paymentID uint) (*paymentdomain.ManualConfirmation, error) {
	var record paymentdomain.ManualConfirmation
	err := s.db.Where("payment_id = ?", paymentID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &record, err
}

// FindManualRequest 按全局请求 ID 查询幂等结果；不存在返回 nil。
func (s *Store) FindManualRequest(requestID string) (*paymentdomain.ManualConfirmation, error) {
	var record paymentdomain.ManualConfirmation
	err := s.db.Where("request_id = ?", requestID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &record, err
}

// CreateManualConfirmation 原子追加审计，由非空唯一索引保护并发和请求复用。
func (s *Store) CreateManualConfirmation(record *paymentdomain.ManualConfirmation) error {
	return s.db.Create(record).Error
}

// RecordReceiptAnomaly 幂等保存额外流水事实，不覆盖原始支付证据。
func (s *Store) RecordReceiptAnomaly(record *paymentdomain.PaymentReceiptAnomaly) error {
	digest := sha256.Sum256([]byte(record.TransactionID))
	record.ReferenceHash = hex.EncodeToString(digest[:])
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(record).Error
}

// ClaimEvidence 在当前事务占用渠道内证据；重复同笔可重放，其他支付占用返回冲突。
func (s *Store) ClaimEvidence(channelID uint, reference string, paymentID uint) error {
	ownerID := paymentID
	if strings.HasPrefix(reference, "transaction:") {
		// 历史流水优先占位；不锁其他支付行，避免跨付款根反向锁死。
		var historical paymentdomain.Payment
		result := s.db.Where("channel_id = ? AND provider_ref = ?", channelID, strings.TrimPrefix(reference, "transaction:")).Order("CASE WHEN status = 'success' THEN 0 ELSE 1 END, id").Limit(1).Find(&historical)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			ownerID = historical.ID
		}
	}
	digest := sha256.Sum256([]byte(reference))
	hash := hex.EncodeToString(digest[:])
	claim := paymentdomain.PaymentEvidenceClaim{ChannelID: channelID, ReferenceHash: hash, PaymentID: ownerID, CreatedAt: time.Now()}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim).Error; err != nil {
		return err
	}
	var existing paymentdomain.PaymentEvidenceClaim
	if err := s.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("channel_id = ? AND reference_hash = ?", channelID, hash).First(&existing).Error; err != nil {
		return err
	}
	if existing.PaymentID != paymentID {
		return paymentcontract.ErrManualConfirmationConflict
	}
	return nil
}
