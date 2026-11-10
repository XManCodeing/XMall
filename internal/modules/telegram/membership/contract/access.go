package contract

import (
	"context"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// JoinAbuse is a secret-free projection of confirmed refusal and its original owner.
// Window uses trusted event time, never retry time; zero InviteID is a scan-only tombstone.
type JoinAbuse struct {
	ID, InviteID, AccountID, BotID, TargetID uint
	TelegramID, ChatID                       int64
	BindingVersion, RouteVersion, Generation uint64
	Window                                   time.Time
}

// JoinAbuseRepository reuses immutable join/operation/result evidence, not a new queue.
// Reads prove an alien applicant, acknowledged decline, current original binding and rights.
type JoinAbuseRepository interface {
	JoinAbuses(context.Context, uint, uint, int, time.Time) ([]JoinAbuse, error)
	JoinAbuse(context.Context, uint, time.Time) (*JoinAbuse, error)
}

// AccessIdentity 必须来自已认证网站会话或可信 Bot actor；不读取联系人、不创建账号。
// TelegramID 必须与历史来源及当前账号绑定同时匹配，BotID 为当前执行实例。
type AccessIdentity struct {
	AccountID  uint
	TelegramID int64
	BotID      uint
	// Authorization 为本次敏感操作的预期快照；仓储入队必须在同事务复核。
	Authorization *AccessEligibility
	// Route 限定当前物理群封禁观测；历史 account=0 观测可省略。
	Route *AccessRoute
}

// AccessRoute 复用 groups 的代次绑定路由值。Resolver 必须验证会员用途、enabled、
// 当前路由、新鲜管理员权限和邀请/审批能力；不能仅凭数据库 running 位返回成功。
type AccessRoute groups.SupportRoute

// AccessRoutes 每次动作前重新取得当前会员路由；网络探测不得在仓储事务中进行。
type AccessRoutes interface {
	MembershipRoute(context.Context, uint) (AccessRoute, error)
}

// AccessAuthorizer 提供事务外初筛；不能作为敏感授权快照或群封禁判断。
// 最终账号/当前绑定与权益必须由 AccessRepository 同事务读取，平台查询在事务外。
// 不得依赖 Bot 联系人或创建商城账号，BotID 不可作为跳过绑定检查的依据。
type AccessAuthorizer interface {
	AuthorizeMembership(context.Context, AccessIdentity, uint) error
}

// AccessCipher 可直接注入既有 runtime Manager 的 Seal/Open，实现邀请密文保护。
type AccessCipher interface {
	Seal(string) (string, error)
	Open(string) (string, error)
}

// AccessAPIResult 明确区分成功、明确失败、可重试失败和未知；空值按未知处理。
// ErrorClass 仅允许固定脱敏分类；不得放 URL/token/平台原始响应。
type AccessAPIResult struct {
	Outcome    string
	ErrorClass string
	RetryAfter time.Duration
}

const (
	AccessSucceeded = "succeeded"
	AccessFailed    = "failed"
	AccessRetryable = "retryable"
	AccessUnknown   = "unknown"
)

// AccessCreateInvite 强制申请式邀请，平台适配器必须设置 creates_join_request=true，
// 不得设置 member_limit；ExpiresAt 是服务端决定的有效期，Name 仅含不敏感操作引用。
type AccessCreateInvite struct {
	Route     AccessRoute
	Name      string
	Term      d.InviteTerm
	ExpiresAt time.Time
}

// AccessInviteResult 仅在成功时提供真实 Telegram 邀请 URL，不接受提示页 URL。
type AccessInviteResult struct {
	AccessAPIResult
	Link string
}

// AccessJoinAction 指向可信申请者与物理群，不从 username 或 user_chat_id 推断身份。
// Telegram 只接收 chat/user，无法原子绑定本地申请 ID；最终检查后仍有外部竞态。
type AccessJoinAction struct {
	Route      AccessRoute
	TelegramID int64
}

// AccessMemberResult 是定点查询结果；不能证明某次拒绝/撤销成功。
type AccessMemberResult struct {
	AccessAPIResult
	Status   string
	IsMember bool
}

// AccessPlatform 是 B6a 平台窄接口。实现须使用既有受管实例凭据与执行栅栏，
// 超时/连接断开归 unknown，确定未执行才可 retryable；不实现踢人和退款。
type AccessPlatform interface {
	CreateMembershipInvite(context.Context, AccessCreateInvite) AccessInviteResult
	ApproveMembershipJoin(context.Context, AccessJoinAction) AccessAPIResult
	DeclineMembershipJoin(context.Context, AccessJoinAction) AccessAPIResult
	RevokeMembershipInvite(context.Context, AccessRoute, string) AccessAPIResult
	GetMembershipMember(context.Context, AccessJoinAction) AccessMemberResult
}

// AccessGroup 是无邀请秘密的列表白名单；所有 ID 使用 JSON 十进制字符串。
type AccessGroup struct {
	Title        string     `json:"title"`
	TargetID     uint       `json:"target_id,string"`
	BotID        uint       `json:"bot_id,string"`
	TelegramID   int64      `json:"telegram_id,string"`
	Active       bool       `json:"active"`
	Kind         string     `json:"kind"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	InviteID     uint       `json:"invite_id,string"`
	InviteState  string     `json:"invite_state"`
	MemberStatus string     `json:"member_status,omitempty"`
}

// AccessEntry 只在已授权的单入口查询中返回明文链接，列表及日志禁止携带。
type AccessEntry struct {
	AccessGroup
	Link string `json:"link,omitempty"`
}

// AccessReader 是网站/Bot 共用入口，调用方必须从会话提取身份，不接收 body 伪造账号。
type AccessReader interface {
	ListMemberships(context.Context, AccessIdentity, uint, int) ([]AccessGroup, error)
	RequestMembershipInvite(context.Context, AccessIdentity, uint) (*AccessEntry, error)
}

// AccessEligibility 从所有账本来源实时重算，Version 用于外部动作前后核验。
type AccessEligibility struct {
	// BindingVersion 复用同事务读取的当前绑定行 ID 作为代次；account=0 历史观测时为零。
	BindingVersion uint
	Active         bool
	Kind           string
	ExpiresAt      *time.Time
	Version        uint64
	SourceID       uint
	AccountID      uint
}

// AccessJoinRequest 由可信 inbox update 适配，不接受公开 HTTP 直接提交。
// ReceivedAt 必须为 Telegram 申请事件时间（非重放时间），同秒由 UpdateID 排序。
type AccessJoinRequest struct {
	BotID      uint
	UpdateID   int64
	ChatID     int64
	TelegramID int64
	InviteLink string
	ReceivedAt time.Time
	// Authorization 是批准决策的同事务账号/绑定与权益快照；拒绝及水位不使用。
	Authorization *AccessEligibility
}

// AccessObservation 来自可信 chat_member 或定点查询；同时间使用 Sequence 排序。
// Source 仅可 chat_member/query；无可信账本关联的成员不会被纳管。
type AccessObservation struct {
	// ActorIsManagedBot is set only by a trusted adapter comparing event actor with the managed bot identity.
	// Unattributed chat_member kicks conservatively block automatic lifecycle unban.
	ActorIsManagedBot bool
	TargetID          uint
	BotID             uint
	ChatID            int64
	RouteVersion      uint64
	TelegramID        int64
	Status            string
	IsMember          bool
	ObservedAt        time.Time
	Sequence          int64
	Source            string
}

// AccessRepository 的事务方法仅使用同一绑定连接；网络和跨模块授权全部在事务外。
// Claim 在 creating 租约过期时转 unknown，绝不自动重新发送。
type AccessRepository interface {
	Eligibility(context.Context, AccessIdentity, uint, time.Time) (AccessEligibility, error)
	ListAccess(context.Context, AccessIdentity, uint, int, time.Time) ([]AccessGroup, error)
	EnsureInvite(context.Context, AccessIdentity, AccessRoute, time.Time, time.Time) (*d.AccessInvite, error)
	Invite(context.Context, uint) (*d.AccessInvite, error)
	InviteByHash(context.Context, uint, int64, string) (*d.AccessInvite, error)
	EnqueueRevoke(context.Context, AccessIdentity, uint, time.Time) (*d.AccessOperation, error)
	// EnqueueJoin 的 needs_review 首次调用先推进水位，决策可由相同 inbox 重放补齐。
	EnqueueJoin(context.Context, AccessJoinRequest, *d.AccessInvite, string, time.Time) (*d.AccessOperation, error)
	// CurrentJoin 校验同 bot/chat/user 最新水位及操作租约；旧申请返回 ErrConflict。
	CurrentJoin(context.Context, *d.AccessOperation, time.Time) (*d.AccessJoin, error)
	ClaimAccess(context.Context, uint, string, time.Time, time.Duration) (*d.AccessOperation, error)
	RenewAccess(context.Context, *d.AccessOperation, time.Time, time.Duration) error
	FinishAccess(context.Context, *d.AccessOperation, AccessAPIResult, string, string, time.Time) error
	ClaimAccessReconcile(context.Context, uint, string, time.Time, time.Duration) (*d.AccessOperation, error)
	ReconcileAccessEligibility(context.Context, *d.AccessOperation, time.Time) (AccessEligibility, error)
	FinishAccessReconcile(context.Context, *d.AccessOperation, AccessMemberResult, time.Time, time.Time) error
	ListAccessReconciliations(context.Context, uint, uint, int) ([]AccessReconciliation, error)
	ObserveAccess(context.Context, AccessObservation) (bool, error)
}

// AccessReconciliation is a secret-free recovery read model. Original approval remains unknown.
// Readers must enforce administrative authorization; this port does not expose a public HTTP route.
type AccessReconciliation struct {
	ID                 uint       `json:"id,string"`
	ParentOperationID  uint       `json:"parent_operation_id,string"`
	TargetID           uint       `json:"target_id,string"`
	BotID              uint       `json:"bot_id,string"`
	ChatID             int64      `json:"chat_id,string"`
	TelegramID         int64      `json:"telegram_id,string"`
	RouteVersion       uint64     `json:"route_version,string"`
	State              string     `json:"state"`
	Attempt            uint       `json:"attempt"`
	NextAttemptAt      time.Time  `json:"next_attempt_at"`
	ErrorClass         string     `json:"error_class"`
	ReviewReason       string     `json:"review_reason"`
	MemberStatus       string     `json:"member_status"`
	MemberIsMember     bool       `json:"member_is_member"`
	ObservedAt         *time.Time `json:"observed_at,omitempty"`
	EntitlementVersion uint64     `json:"entitlement_version,string"`
}
