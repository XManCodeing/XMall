package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// PaidWorker 只消费本地权益事件；不调用 Telegram，不读销售或机器人运行开关。
// ledger 必须保证同一付款来源幂等，覆盖授予成功后进程崩溃但尚未确认的窗口。
type PaidWorker struct {
	store  c.PaidEventStore
	ledger c.PaidGranter
	now    func() time.Time
	ttl    time.Duration
}

// NewPaidWorker 注入持久 store、幂等 ledger、时钟 now 和正租约 ttl；无隐式启动或调度。
// now 为 nil 时使用系统时钟；依赖缺失或非法 TTL 返回 ErrInvalid。
func NewPaidWorker(store c.PaidEventStore, ledger c.PaidGranter, now func() time.Time, ttl time.Duration) (*PaidWorker, error) {
	if store == nil || ledger == nil || ttl <= 0 {
		return nil, c.ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	return &PaidWorker{store: store, ledger: ledger, now: now, ttl: ttl}, nil
}

// RunOnce 至多消费 limit 条到期事件；ctx 控制取消，返回成功确认数量及聚合错误。
// 单条失败安排延后再试并继续其他记录，不让坏事件长期占据队首。
func (worker *PaidWorker) RunOnce(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, c.ErrInvalid
	}
	completed := 0
	var failures error
	for index := 0; index < limit; index++ {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(failures, err)
		}
		token := make([]byte, 24)
		if _, err := rand.Read(token); err != nil {
			return completed, errors.Join(failures, err)
		}
		owner := hex.EncodeToString(token)
		event, err := worker.store.ClaimPaid(ctx, owner, worker.now().UTC(), worker.ttl)
		if err != nil {
			return completed, errors.Join(failures, err)
		}
		if event == nil {
			break
		}
		if err := worker.consume(ctx, event); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		completed++
	}
	return completed, failures
}

// consume 以事件不可变 PaidAt 调用 ledger；租约超时取消调用，提交与 ack 之间允许安全重放。
// event 必须来自 ClaimPaid；只记录内部 ID，不输出业务负载或敏感错误内容。
func (worker *PaidWorker) consume(ctx context.Context, event *d.PaidEvent) error {
	grantCtx, cancel := context.WithTimeout(ctx, worker.ttl)
	defer cancel()
	err := worker.ledger.GrantPaid(grantCtx, c.PaidGrant{OrderID: event.OrderID, PaymentID: event.PaymentID, PaidAt: event.PaidAt})
	now := worker.now().UTC()
	if err != nil {
		delay := time.Second * time.Duration(1<<min(event.Attempts, 8))
		retryErr := worker.store.RetryPaid(ctx, event.ID, event.ClaimOwner, now, now.Add(delay))
		slog.WarnContext(ctx, "membership paid event retry", "event_id", event.ID, "attempt", event.Attempts)
		return errors.Join(err, retryErr)
	}
	if err := worker.store.FinishPaid(ctx, event.ID, event.ClaimOwner, now); err != nil {
		return err
	}
	slog.InfoContext(ctx, "membership paid event completed", "event_id", event.ID)
	return nil
}

// RecoverOnce 执行独立历史付款补漏分页；after/limit 由生产调度持有，不以时间窗口筛选。
// 返回下一游标及扫描数，空页后调度重置游标；此操作不轮询退款、不改变销售门控。
func (worker *PaidWorker) RecoverOnce(ctx context.Context, after uint, limit int) (uint, int, error) {
	return worker.store.RecoverPaid(ctx, after, limit)
}
