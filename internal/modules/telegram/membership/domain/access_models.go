package domain

import "time"

// InviteTerm 是平台邀请期限，不等同会员权益期限；历史空值只按有限期限解释。
type InviteTerm string

const (
	InviteTermLimited  InviteTerm = "limited"
	InviteTermLongTerm InviteTerm = "long_term"
)

// IsInviteTermValid 检查明确期限与截止时间组合；空枚举兼容历史有限邀请。
// term/expires 为持久值，at 为可信时钟；未知枚举及零截止有限邀请均拒绝。
func IsInviteTermValid(term InviteTerm, expires, at time.Time) bool {
	switch term {
	case InviteTermLongTerm:
		return expires.IsZero()
	case "", InviteTermLimited:
		return !expires.IsZero() && expires.After(at)
	default:
		return false
	}
}

// IsInviteUsableAt 判断期限及持久退休投影；调用方仍须检查状态、绑定、权益和路由。
func IsInviteUsableAt(invite AccessInvite, at time.Time) bool {
	return !invite.Retired && IsInviteTermValid(invite.Term, invite.ExpiresAt, at)
}

// IsInviteExpiredAt 只接受可信有限截止证据；长期/未知期限不能靠时钟清除责任。
func IsInviteExpiredAt(invite AccessInvite, at time.Time) bool {
	return (invite.Term == "" || invite.Term == InviteTermLimited) && invite.ExpiryTrusted && !invite.ExpiresAt.IsZero() && !invite.ExpiresAt.After(at)
}

// AccessInvite 是逐受益人/逻辑群/路由邀请历史；秘密仅保存密文与查找哈希。
type AccessInvite struct {
	// Retired 由持久清理项派生，禁止续费恢复入口；不增加数据库列。
	Retired            bool       `gorm:"-" json:"-"`
	ID                 uint       `gorm:"primaryKey"`
	AccountID          uint       `gorm:"not null;index"`
	TelegramID         int64      `gorm:"not null;index:idx_membership_invite_actor,priority:1"`
	TargetID           uint       `gorm:"not null;index:idx_membership_invite_actor,priority:2"`
	BotID              uint       `gorm:"not null;index:idx_membership_invite_hash,priority:1"`
	ChatID             int64      `gorm:"not null;index:idx_membership_invite_hash,priority:2"`
	RouteVersion       uint64     `gorm:"not null"`
	Generation         uint64     `gorm:"not null"`
	BindingVersion     uint       `gorm:"not null;default:0"` // 绑定行 ID 作为代次；零值无历史授权证据。
	EntitlementVersion uint64     `gorm:"not null"`
	SourceID           uint       `gorm:"not null"`
	State              string     `gorm:"size:16;not null;index"`
	LinkCipher         string     `gorm:"type:text" json:"-"`
	LinkHash           string     `gorm:"size:64;index:idx_membership_invite_hash,priority:3" json:"-"`
	Term               InviteTerm `gorm:"size:16;not null;default:limited"`
	ExpiryTrusted      bool       `gorm:"not null;default:false"`
	ExpiresAt          time.Time  `gorm:"not null;index"`
	CreatedAt          time.Time  `gorm:"not null"`
	UpdatedAt          time.Time  `gorm:"not null"`
}

// TableName 返回邀请历史表名，无参数。
func (AccessInvite) TableName() string { return "telegram_membership_access_invites" }

// AccessSlot 为当前邀请提供非空唯一槽位，未知邀请不允许无条件替换。
type AccessSlot struct {
	TelegramID int64  `gorm:"primaryKey;autoIncrement:false"`
	TargetID   uint   `gorm:"primaryKey;autoIncrement:false"`
	InviteID   uint   `gorm:"not null"`
	Version    uint64 `gorm:"not null"`
}

// TableName 返回邀请槽位表名，无参数。
func (AccessSlot) TableName() string { return "telegram_membership_access_slots" }

// AccessJoin 保存接收与决策；approved 不等于实际加入，未知链接记录 needs_review。
type AccessJoin struct {
	ID                 uint   `gorm:"primaryKey"`
	ActorAdminID       uint   `gorm:"not null;default:0"`
	ReviewReason       string `gorm:"size:48"`
	ReviewedAt         *time.Time
	BotID              uint      `gorm:"not null;uniqueIndex:uidx_membership_join_update,priority:1"`
	UpdateID           int64     `gorm:"not null;uniqueIndex:uidx_membership_join_update,priority:2"`
	ChatID             int64     `gorm:"not null"`
	TelegramID         int64     `gorm:"not null"`
	InviteID           uint      `gorm:"not null"`
	LinkHash           string    `gorm:"size:64;not null" json:"-"`
	Decision           string    `gorm:"size:24;not null"`
	State              string    `gorm:"size:24;not null"`
	EntitlementVersion uint64    `gorm:"not null"`
	ReceivedAt         time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// TableName 返回申请审计表名，无参数。
func (AccessJoin) TableName() string { return "telegram_membership_access_joins" }

// AccessOperation 持久保存副作用意图；LeaseToken/Version 构成完成 CAS。
// creating 丢租只转 unknown，不能因为本地未确认便重放平台调用。
type AccessOperation struct {
	ID uint `gorm:"primaryKey"`
	// Lifecycle phases share the access queue but are claimed only by lifecycle workers.
	CleanupCycleID uint   `gorm:"not null;default:0;index"`
	LifecyclePhase string `gorm:"size:24;not null;default:''"`
	BanOutcome     string `gorm:"size:16;not null;default:''"`
	UnbanOutcome   string `gorm:"size:16;not null;default:''"`
	BanConfirmed   bool   `gorm:"not null;default:false"`
	BanBlocked     bool   `gorm:"not null;default:false"`
	BanConfirmedAt *time.Time
	// ParentOperationID links a read-only reconciliation to the original unknown approval.
	ParentOperationID  uint   `gorm:"not null;default:0;index"`
	ReviewReason       string `gorm:"size:48"`
	MemberStatus       string `gorm:"size:24"`
	MemberIsMember     bool   `gorm:"not null;default:false"`
	ObservedAt         *time.Time
	BusinessKey        string    `gorm:"size:128;not null;uniqueIndex"`
	Kind               string    `gorm:"size:16;not null"`
	InviteID           uint      `gorm:"not null;index"`
	JoinID             uint      `gorm:"not null"`
	AccountID          uint      `gorm:"not null"`
	TelegramID         int64     `gorm:"not null"`
	TargetID           uint      `gorm:"not null"`
	BotID              uint      `gorm:"not null;index:idx_membership_access_due,priority:1"`
	ChatID             int64     `gorm:"not null"`
	RouteVersion       uint64    `gorm:"not null"`
	Generation         uint64    `gorm:"not null"`
	BindingVersion     uint      `gorm:"not null;default:0"` // 绑定行 ID 作为代次；零值无历史授权证据。
	EntitlementVersion uint64    `gorm:"not null"`
	State              string    `gorm:"size:16;not null;index:idx_membership_access_due,priority:2"`
	Attempt            uint      `gorm:"not null"`
	Version            uint64    `gorm:"not null"`
	LeaseToken         string    `gorm:"size:64;not null"`
	LeaseUntil         time.Time `gorm:"not null;index"`
	LastAttemptAt      *time.Time
	NextAttemptAt      time.Time `gorm:"not null;index:idx_membership_access_due,priority:3"`
	ErrorClass         string    `gorm:"size:48"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// TableName 返回外部操作表名，无参数。
func (AccessOperation) TableName() string { return "telegram_membership_access_operations" }

// AccessResult 保留外部响应证据，失租的迟到响应不可覆盖当前操作但不能静默丢弃。
// 不接受平台原始错误正文；链接只保存密文与哈希，不自动协调 unknown。
type AccessResult struct {
	ActorAdminID uint      `gorm:"not null;default:0"`
	Action       string    `gorm:"size:24"`
	Reason       string    `gorm:"size:48"`
	OperationID  uint      `gorm:"primaryKey;autoIncrement:false"`
	LeaseVersion uint64    `gorm:"primaryKey;autoIncrement:false"`
	Generation   uint64    `gorm:"not null"`
	Outcome      string    `gorm:"size:16;not null"`
	ErrorClass   string    `gorm:"size:48;not null;default:''"` // 固定分类；不保存平台原始错误。
	LinkCipher   string    `gorm:"type:text" json:"-"`
	LinkHash     string    `gorm:"size:64" json:"-"`
	ReceivedAt   time.Time `gorm:"not null"`
}

// TableName 返回外部结果证据表名，无参数。
func (AccessResult) TableName() string { return "telegram_membership_access_results" }

// AccessMember 只纳管有账本来源的实际身份，保留平台成员事实与乱序保护水位。
type AccessMember struct {
	TargetID          uint      `gorm:"primaryKey;autoIncrement:false"`
	ChatID            int64     `gorm:"primaryKey;autoIncrement:false"`
	TelegramID        int64     `gorm:"primaryKey;autoIncrement:false"`
	BotID             uint      `gorm:"not null"`
	RouteVersion      uint64    `gorm:"not null"`
	SourceID          uint      `gorm:"not null;index"`
	Status            string    `gorm:"size:24;not null"`
	IsMember          bool      `gorm:"not null"`
	ObservedAt        time.Time `gorm:"not null"`
	Sequence          int64     `gorm:"not null"`
	ObservationSource string    `gorm:"size:24;not null"`
	ManagedAt         time.Time `gorm:"not null"`
}

// TableName 返回已知可信成员表名，无参数。
func (AccessMember) TableName() string { return "telegram_membership_access_members" }
