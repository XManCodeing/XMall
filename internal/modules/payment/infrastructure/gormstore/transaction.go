package gormstore

import (
	"strings"

	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	ordergormstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"

	"gorm.io/gorm"
)

type transaction struct {
	ordercontract.Transaction
	db                    *gorm.DB
	guestCredentialSecret string
}

var _ paymentcontract.Transaction = transaction{}

// UseTransaction 将调用方已打开的数据库事务适配为支付工作单元。
func UseTransaction(tx *gorm.DB, guestCredentialSecret string) paymentcontract.Transaction {
	if tx == nil {
		return nil
	}
	secret := strings.TrimSpace(guestCredentialSecret)
	if secret == "" {
		panic("payment transaction: guest credential secret is required")
	}
	return transaction{
		Transaction:           ordergormstore.UseTransaction(tx, secret),
		db:                    tx,
		guestCredentialSecret: secret,
	}
}

func (tx transaction) Payments() paymentcontract.Store {
	return New(tx.db, tx.guestCredentialSecret)
}

func (tx transaction) PaymentChannels() paymentcontract.ChannelStore {
	return NewChannelStore(tx.db)
}

func (s *Store) WithinTransaction(fn func(paymentcontract.Transaction) error) error {
	if s == nil || s.db == nil || fn == nil {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		unit := UseTransaction(tx, s.guestCredentialSecret).(transaction)
		if s.businessEventsEnabled {
			unit.Transaction = ordergormstore.UseTransactionWithBusinessEvents(tx, s.guestCredentialSecret)
		}
		return fn(unit)
	})
}
