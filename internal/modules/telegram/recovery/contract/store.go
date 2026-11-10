package contract

import (
	"context"
	"errors"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

var (
	ErrInvalid   = errors.New("recovery: invalid input")
	ErrNotFound  = errors.New("recovery: not found")
	ErrConflict  = errors.New("recovery: idempotency content conflict")
	ErrStale     = errors.New("recovery: stale version or state")
	ErrExpired   = errors.New("recovery: preflight expired")
	ErrScopeBusy = errors.New("recovery: scope occupied")
)

// PreflightInput 为已完成可信只读检查的输入；调用方不得传凭据或平台原始文本。
// TTL 限制 1 秒至 1 小时；相同 request_key 必须具有完全相同的语义输入。
type PreflightInput struct {
	Kind             d.Kind
	RequestKey       string
	Snapshot         d.Snapshot
	ActorID          uint
	Reason           string
	VerificationNote string
	TTL              time.Duration
}

// Confirmation 绑定随机预检引用、预期任务版本与可信操作人；资源版本由 application 再核验。
// 确认不执行外部动作；相同 actor/version 的重放返回同一任务，其余返回 ErrStale。
type Confirmation struct {
	PreflightRef    string
	ExpectedVersion uint64
	ActorID         uint
}

// Cancellation 只允许确认前撤销；相同 actor/version 重放返回原任务。
type Cancellation struct {
	TaskID          uint
	ExpectedVersion uint64
	ActorID         uint
}

// Transition 是执行器持久推进状态的 CAS；本底座不提供执行器或真实平台调用。
type Transition struct {
	TaskID          uint
	ExpectedVersion uint64
	State           d.State
}

// ListFilter 使用 ID 游标分页；Limit 1..100，零值采用 20。
type ListFilter struct {
	Kind    d.Kind
	State   d.State
	AfterID uint
	Limit   int
}

// TaskPage 为父任务分页结果，NextAfterID=0 表示没有下一页。
type TaskPage struct {
	*PageInfo
	Items       []d.Task `json:"items"`
	NextAfterID uint     `json:"next_after_id,string"`
}

// Detail 保留 typed 预检白名单；步骤通过独立游标接口读取，避免无限响应。
type Detail struct {
	Task     d.Task     `json:"task"`
	Snapshot d.Snapshot `json:"snapshot"`
}

// StepInput 创建不可变步骤意图，business_key 在 task 内唯一。
type StepInput struct {
	TaskID      uint
	BusinessKey string
	Operation   string
}

// StepClaim 用版本领取步骤；网络调用必须在成功返回后、事务外进行。
type StepClaim struct {
	StepID          uint
	ExpectedVersion uint64
	Lease           time.Duration
}

// StepCompletion 是租约和版本双重栅栏，ResultCode 仅接受脱敏固定分类代码。
type StepCompletion struct {
	StepID          uint
	ExpectedVersion uint64
	LeaseToken      string
	Outcome         d.StepState
	ResultCode      string
}

// StepPage 为步骤游标分页结果。
type StepPage struct {
	*PageInfo
	Items       []d.Step `json:"items"`
	NextAfterID uint     `json:"next_after_id,string"`
}

// Store 是 application 的纯持久端口；ctx 控制取消，输入明确版本/身份，返回数据及错误。
// 所有方法禁止网络 I/O。确认只占用 scope，不等于业务切换；业务版本栅栏由 application 实现。
type Store interface {
	CreatePreflight(context.Context, PreflightInput) (*d.Task, error)
	Confirm(context.Context, Confirmation) (*d.Task, error)
	Cancel(context.Context, Cancellation) (*d.Task, error)
	Transition(context.Context, Transition) (*d.Task, error)
	Get(context.Context, uint) (*Detail, error)
	List(context.Context, ListFilter) (TaskPage, error)
	ListPage(context.Context, ListFilter, PageQuery) (TaskPage, error)
	ListStepsPage(context.Context, uint, PageQuery) (StepPage, error)
	EnsureStep(context.Context, StepInput) (*d.Step, error)
	ClaimStep(context.Context, StepClaim) (*d.Step, error)
	CompleteStep(context.Context, StepCompletion) (*d.Step, error)
	ExpireLeases(context.Context, int) (int64, error)
	ListSteps(context.Context, uint, uint, int) (StepPage, error)
}
