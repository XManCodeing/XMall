package contract

// TargetTitle is a read-only title projection for consumers of group route history.
// ObservationTitle belongs only to the exact current physical route; Name labels the logical target.
type TargetTitle struct {
	ID               uint
	Name             string
	BotInstanceID    uint
	TelegramChatID   int64
	RouteVersion     uint64
	ObservationTitle string `gorm:"column:obs_title"`
}

// TableName returns the existing target relation for transaction-bound read adapters.
func (TargetTitle) TableName() string { return "telegram_group_targets" }
