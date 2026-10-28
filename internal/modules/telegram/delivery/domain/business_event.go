package domain

import "time"

// OrderOperation is committed with its order; a key can never reserve a second order.
type OrderOperation struct {
	ID           uint   `gorm:"primaryKey"`
	OperationKey string `gorm:"size:160;not null;uniqueIndex"`
	RequestHash  string `gorm:"size:64;not null"`
	UserID       uint   `gorm:"not null"`
	OrderID      uint   `gorm:"not null"`
	CreatedAt    time.Time
}

func (OrderOperation) TableName() string { return "telegram_order_operations" }

// OrderSource is an explicit private-chat purchase origin, not an OAuth binding.
type OrderSource struct {
	ID            uint  `gorm:"primaryKey"`
	OrderID       uint  `gorm:"not null;uniqueIndex"`
	UserID        uint  `gorm:"not null;index"`
	BotID         uint  `gorm:"not null;index"`
	TelegramID    int64 `gorm:"not null"`
	PrivateChatID int64 `gorm:"not null"`
	CreatedAt     time.Time
}

func (OrderSource) TableName() string { return "telegram_order_sources" }

// BusinessEvent contains references only. Delivery reads existing fulfillment data;
// credentials/card secrets must never be copied into this table.
type BusinessEvent struct {
	ID            uint   `gorm:"primaryKey"`
	EventKey      string `gorm:"size:200;not null;uniqueIndex"`
	Kind          string `gorm:"size:64;not null"`
	OrderID       uint   `gorm:"index"`
	ParentOrderID uint
	PaymentID     uint
	RechargeID    uint
	FulfillmentID uint
	UserID        uint   `gorm:"not null"`
	BotID         uint   `gorm:"not null;index"`
	TelegramID    int64  `gorm:"not null"`
	PrivateChatID int64  `gorm:"not null"`
	Status        string `gorm:"size:24;not null;index:idx_telegram_business_ready"`
	Attempts      int
	AvailableAt   time.Time  `gorm:"not null;index:idx_telegram_business_ready"`
	LeaseOwner    string     `gorm:"size:128"`
	LeaseUntil    *time.Time `gorm:"index"`
	DeliveredAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (BusinessEvent) TableName() string { return "telegram_business_events" }
