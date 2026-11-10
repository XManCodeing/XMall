package domain

import "time"

// Kind 标识统一恢复任务的业务类型，不代表已实现平台执行器。
type Kind string

const (
	GroupReplace     Kind = "group_replace"
	BotReplace       Kind = "bot_replace"
	IdentityTransfer Kind = "identity_transfer"
)

// State 是父任务的持久状态；unknown 子步骤必须进入人工核验而非自动重试。
type State string

const (
	AwaitingConfirmation State = "awaiting_confirmation"
	Fencing              State = "fencing"
	Switching            State = "switching"
	Recovering           State = "recovering"
	NeedsReview          State = "needs_review"
	PartialFailed        State = "partial_failed"
	Completed            State = "completed"
	Cancelled            State = "cancelled"
)

// Scope 只包含受保护的业务 ID；所有非零资源分别占独占槽，跨 kind 也互斥。
// 应用必须从可信预检结果构造 scope；身份迁移必须包含旧、新身份对应资源。
type Scope struct {
	TargetIDs   []string `json:"target_ids"`
	ChatIDs     []string `json:"chat_ids"`
	BotIDs      []string `json:"bot_ids"`
	AccountIDs  []string `json:"account_ids"`
	TelegramIDs []string `json:"telegram_ids"`
}

// Endpoint 是无秘密的旧/新目标白名单；JSON ID 使用十进制字符串，避免 JS 精度丢失。
type Endpoint struct {
	TargetID   uint  `json:"target_id,string"`
	BotID      uint  `json:"bot_id,string"`
	ChatID     int64 `json:"chat_id,string"`
	AccountID  uint  `json:"account_id,string"`
	TelegramID int64 `json:"telegram_id,string"`
}

// Versions 保存需要应用在确认及执行前复核的版本，不替代业务资源的事务栅栏。
type Versions struct {
	Config      uint64 `json:"config,string"`
	Route       uint64 `json:"route,string"`
	Generation  uint64 `json:"generation,string"`
	Binding     uint64 `json:"binding,string"`
	Entitlement uint64 `json:"entitlement,string"`
}

// IdentityTarget is the immutable per-target membership version whitelist; no invitations are stored.
type IdentityTarget struct {
	TargetID           string `json:"target_id"`
	BotID              string `json:"bot_id"`
	ChatID             string `json:"chat_id"`
	EntitlementVersion string `json:"entitlement_version"`
	GroupVersion       string `json:"group_version"`
	RouteVersion       string `json:"route_version"`
}

// BotRoute is a secret-free immutable route inventory, including disabled source targets.
// It lives only in SnapshotJSON and adds no database columns or migration.
type BotRoute struct {
	TargetID     uint   `json:"target_id,string"`
	ChatID       int64  `json:"chat_id,string"`
	Version      uint64 `json:"version,string"`
	RouteVersion uint64 `json:"route_version,string"`
	Purpose      string `json:"purpose"`
	Enabled      bool   `json:"enabled"`
	NewChatID    int64  `json:"new_chat_id,string,omitempty"`
}

// DestinationChat returns the confirmed destination; absent legacy intent keeps the source chat.
// It performs no inference from mutable target rows and returns no credentials.
func (route BotRoute) DestinationChat() int64 {
	if route.NewChatID != 0 {
		return route.NewChatID
	}
	return route.ChatID
}

// BotModule preserves source module intent and CAS without copying credentials or content.
type BotModule struct {
	Partition string `json:"partition"`
	Version   uint64 `json:"version,string"`
	TargetID  uint   `json:"target_id,string"`
	Enabled   bool   `json:"enabled"`
}

// Snapshot 是预检唯一可持久化输入，不接受任意 JSON、token、邀请链接或平台响应。
type Snapshot struct {
	BotHistory      []BotRoute       `json:"bot_history,omitempty"`
	BotRoutes       []BotRoute       `json:"bot_routes,omitempty"`
	BotModules      []BotModule      `json:"bot_modules,omitempty"`
	IdentityTargets []IdentityTarget `json:"identity_targets,omitempty"`
	Scope           Scope            `json:"scope"`
	Old             Endpoint         `json:"old"`
	New             Endpoint         `json:"new"`
	ExpectedOld     Versions         `json:"expected_old"`
	ExpectedNew     Versions         `json:"expected_new"`
}

// Task 同时承载预检和确认后的父任务，确认只更新本行，永不另建父任务。
type Task struct {
	ID               uint       `gorm:"primaryKey" json:"id,string"`
	Kind             Kind       `gorm:"size:32;not null;index" json:"kind"`
	State            State      `gorm:"size:32;not null;index" json:"state"`
	RequestKey       string     `gorm:"size:128;not null;uniqueIndex" json:"request_key"`
	RequestHash      string     `gorm:"size:64;not null" json:"-"`
	PreflightRef     string     `gorm:"size:64;not null;uniqueIndex" json:"preflight_ref"`
	SnapshotJSON     string     `gorm:"type:text;not null" json:"-"`
	ActorID          uint       `gorm:"not null" json:"actor_id,string"`
	Reason           string     `gorm:"size:512;not null" json:"reason"`
	VerificationNote string     `gorm:"size:1024;not null" json:"verification_note"`
	Version          uint64     `gorm:"not null" json:"version,string"`
	ConfirmedVersion uint64     `gorm:"not null" json:"-"`
	ConfirmedActorID uint       `gorm:"not null" json:"confirmed_actor_id,string"`
	CancelledVersion uint64     `gorm:"not null" json:"-"`
	CancelledActorID uint       `gorm:"not null" json:"cancelled_actor_id,string"`
	ExpiresAt        time.Time  `gorm:"not null;index" json:"expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at,omitempty"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName 无参数，返回统一恢复任务表名。
func (Task) TableName() string { return "telegram_recovery_tasks" }

// ScopeSlot 用非空主键表达当前占用，释放时删除行，不依赖 NULL unique 语义。
// CHECK 使用表名限定列：MySQL 将点后的 key 识别为列名，同时兼容 PostgreSQL/SQLite，
// 不使用仅适用于 MySQL/SQLite 的反引号，也不改变已有列名或约束语义。
type ScopeSlot struct {
	Key    string `gorm:"primaryKey;size:128;not null;check:recovery_scope_nonempty,telegram_recovery_scope_slots.key <> ''"`
	TaskID uint   `gorm:"not null;index"`
}

// TableName 无参数，返回资源占用表名。
func (ScopeSlot) TableName() string { return "telegram_recovery_scope_slots" }

// StepState 仅 pending 可领取，租约失效变 unknown，禁止自动再次执行。
type StepState string

const (
	StepPending   StepState = "pending"
	StepRunning   StepState = "running"
	StepSucceeded StepState = "succeeded"
	StepFailed    StepState = "failed"
	StepUnknown   StepState = "unknown"
)

// Step 保存外部动作的持久意图与结果，不保存原始请求/响应或秘密。
type Step struct {
	ID          uint       `gorm:"primaryKey" json:"id,string"`
	TaskID      uint       `gorm:"not null;uniqueIndex:uidx_recovery_step_business,priority:1;index" json:"task_id,string"`
	BusinessKey string     `gorm:"size:128;not null;uniqueIndex:uidx_recovery_step_business,priority:2" json:"business_key"`
	Operation   string     `gorm:"size:64;not null" json:"operation"`
	State       StepState  `gorm:"size:16;not null;index" json:"state"`
	Version     uint64     `gorm:"not null" json:"version,string"`
	LeaseToken  string     `gorm:"size:64;not null" json:"-"`
	LeaseUntil  *time.Time `gorm:"index" json:"lease_until,omitempty"`
	ResultCode  string     `gorm:"size:64;not null" json:"result_code"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName 无参数，返回恢复任务子步骤表名。
func (Step) TableName() string { return "telegram_recovery_steps" }
