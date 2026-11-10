package domain

import "time"

// Target owns the non-null physical chat slot independently of purpose and Bot.
// Historical routes never compete for that slot and are never deleted.
type Target struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:120;not null" json:"name"`
	Purpose string `gorm:"size:20;not null" json:"purpose"`
	// RecoveryTaskID preserves Enabled intent while exclusively fencing recovery.
	RecoveryTaskID uint        `gorm:"not null;default:0;index" json:"recovery_task_id"`
	Enabled        bool        `json:"enabled"`
	Version        uint64      `gorm:"not null" json:"version"`
	RouteVersion   uint64      `gorm:"not null" json:"route_version"`
	BotInstanceID  uint        `gorm:"index:idx_tg_group_due,priority:1;index" json:"bot_instance_id"`
	TelegramChatID int64       `gorm:"not null" json:"telegram_chat_id,string"`
	OccupancySlot  string      `gorm:"size:80;not null;uniqueIndex" json:"-"`
	ObservationSeq uint64      `gorm:"not null" json:"observation_seq"`
	Observation    Observation `gorm:"embedded;embeddedPrefix:obs_" json:"observation"`
	NextCheckAt    time.Time   `gorm:"index:idx_tg_group_due,priority:2" json:"next_check_at"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

func (Target) TableName() string { return "telegram_group_targets" }

type Observation struct {
	RouteVersion       uint64     `json:"route_version"`
	Sequence           uint64     `json:"sequence"`
	BotTelegramID      int64      `json:"bot_telegram_id,string"`
	Generation         uint64     `json:"generation"`
	ChatType           string     `gorm:"size:24" json:"chat_type"`
	Title              string     `gorm:"size:255" json:"title"`
	IsMember           bool       `json:"is_member"`
	IsForum            bool       `json:"is_forum"`
	MemberStatus       string     `gorm:"size:24" json:"member_status"`
	CanManageTopics    bool       `json:"can_manage_topics"`
	CanInviteUsers     bool       `json:"can_invite_users"`
	CanRestrictMembers bool       `json:"can_restrict_members"`
	State              string     `gorm:"size:32;index" json:"state"`
	ErrorClass         string     `gorm:"size:32" json:"error_class"`
	Source             string     `gorm:"size:24" json:"source"`
	CheckedAt          *time.Time `json:"checked_at"`
	ObservedAt         *time.Time `json:"observed_at"`
	LastSuccessAt      *time.Time `json:"last_success_at"`
	LastFailureAt      *time.Time `json:"last_failure_at"`
	RetryAfterSeconds  int        `json:"retry_after_seconds"`
}

type Route struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	TargetID       uint       `gorm:"not null;uniqueIndex:idx_tg_route_version" json:"target_id"`
	RouteVersion   uint64     `gorm:"not null;uniqueIndex:idx_tg_route_version" json:"route_version"`
	BotInstanceID  uint       `gorm:"index" json:"bot_instance_id"`
	TelegramChatID int64      `json:"telegram_chat_id,string"`
	EffectiveAt    time.Time  `json:"effective_at"`
	RetiredAt      *time.Time `json:"retired_at"`
}

func (Route) TableName() string { return "telegram_group_routes" }

// Operation currently records only read-only capability checks, not B7 tasks.
type Operation struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	TargetID     uint       `gorm:"index;uniqueIndex:idx_tg_check_sequence" json:"target_id"`
	RouteVersion uint64     `json:"route_version"`
	Sequence     uint64     `gorm:"uniqueIndex:idx_tg_check_sequence" json:"sequence"`
	ActorAdminID uint       `json:"actor_admin_id"`
	Source       string     `gorm:"size:24" json:"source"`
	State        string     `gorm:"size:24;index" json:"state"`
	ErrorClass   string     `gorm:"size:32" json:"error_class"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func (Operation) TableName() string { return "telegram_group_operations" }

type ModuleConfig struct {
	ID            uint      `gorm:"primaryKey" json:"-"`
	BotInstanceID uint      `gorm:"not null;uniqueIndex:idx_tg_module_partition" json:"bot_instance_id"`
	Partition     string    `gorm:"size:24;not null;uniqueIndex:idx_tg_module_partition" json:"partition"`
	Version       uint64    `gorm:"not null" json:"version"`
	Enabled       bool      `json:"enabled"`
	TargetID      uint      `json:"target_id"`
	UpdatedBy     uint      `json:"updated_by"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (ModuleConfig) TableName() string { return "telegram_bot_module_configs" }
