package domain

import "time"

// ProductShareCode stores one permanent, opaque code per product; neither bot nor URL is cached.
type ProductShareCode struct {
	ProductID uint   `gorm:"primaryKey;autoIncrement:false"`
	Code      string `gorm:"size:12;not null;uniqueIndex:idx_membership_product_share_code"`
}

// TableName returns the persistent product-code mapping table.
func (ProductShareCode) TableName() string { return "telegram_membership_product_share_codes" }

// ProductConfig 是现有商品的会员扩展；不复制价格、库存或履约账本。
// Version 对目标集合和全部 SKU 期限整体执行 CAS，Enabled 当前必须为 false。
type ProductConfig struct {
	ProductID uint            `gorm:"primaryKey;autoIncrement:false" json:"product_id"`
	Version   uint64          `gorm:"not null" json:"version"`
	Enabled   bool            `gorm:"not null;default:false" json:"enabled"`
	Quantity  int             `gorm:"not null;default:1" json:"quantity"`
	UpdatedAt time.Time       `json:"updated_at"`
	Targets   []ProductTarget `gorm:"foreignKey:ProductID;references:ProductID" json:"targets"`
	Terms     []SKUTerm       `gorm:"foreignKey:ProductID;references:ProductID" json:"terms"`
}

// TableName 返回商品权益扩展表名，无参数。
func (ProductConfig) TableName() string { return "telegram_membership_product_configs" }

// ProductTarget 仅保存逻辑目标集合，不重复保存可冲突的 Bot 归属。
type ProductTarget struct {
	ProductID uint `gorm:"primaryKey;autoIncrement:false" json:"product_id"`
	TargetID  uint `gorm:"primaryKey;autoIncrement:false" json:"target_id"`
}

// TableName 返回商品目标关联表名，无参数。
func (ProductTarget) TableName() string { return "telegram_membership_product_targets" }

// SKUTerm 关联原 ProductSKU，包括隐藏 DEFAULT；Months 在永久类型下必须为零。
type SKUTerm struct {
	ProductID uint   `gorm:"primaryKey;autoIncrement:false" json:"product_id"`
	SKUID     uint   `gorm:"column:sku_id;primaryKey;autoIncrement:false" json:"sku_id"`
	Kind      string `gorm:"size:16;not null" json:"kind"`
	Months    int64  `gorm:"not null" json:"months"`
}

// TableName 返回 SKU 期限表名，无参数。
func (SKUTerm) TableName() string { return "telegram_membership_sku_terms" }

// SnapshotTarget 保留购买时的逻辑目标、物理路由及 Bot，不随配置编辑改变。
// 后续灾备只能通过显式映射解析，不能回写这些历史字段。
type SnapshotTarget struct {
	TargetID       uint   `json:"target_id"`
	RouteVersion   uint64 `json:"route_version"`
	BotInstanceID  uint   `json:"bot_instance_id"`
	TelegramChatID int64  `json:"telegram_chat_id,string"`
}

// OrderItemSnapshot 是插入后不可变的订单项扩展，不是权益授予或 B5 账本。
// 此包只提供插入/读取接口；外部编排须在可信订单事务内核对身份及订单归属。
type OrderItemSnapshot struct {
	OrderItemID           uint             `gorm:"primaryKey;autoIncrement:false" json:"order_item_id"`
	ProductID             uint             `gorm:"not null;index" json:"product_id"`
	SKUID                 uint             `gorm:"column:sku_id;not null" json:"sku_id"`
	AccountID             uint             `gorm:"not null;index" json:"account_id"`
	BeneficiaryTelegramID int64            `gorm:"not null" json:"beneficiary_telegram_id,string"`
	IdentityVersion       uint64           `gorm:"not null" json:"identity_version"`
	ConfigVersion         uint64           `gorm:"not null" json:"config_version"`
	Kind                  string           `gorm:"size:16;not null" json:"kind"`
	Months                int64            `gorm:"not null" json:"months"`
	DurationSeconds       int64            `gorm:"not null" json:"duration_seconds"`
	Quantity              int              `gorm:"not null" json:"quantity"`
	Targets               []SnapshotTarget `gorm:"serializer:json;type:text;not null" json:"targets"`
	CreatedAt             time.Time        `json:"created_at"`
}

// TableName 返回不可变订单项快照表名，无参数。
func (OrderItemSnapshot) TableName() string { return "telegram_membership_order_item_snapshots" }
