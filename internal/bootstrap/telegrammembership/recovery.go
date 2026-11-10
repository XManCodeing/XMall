package telegrammembership

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/XManCodeing/XMall/internal/logger"
)

// RecoveryService 驱动会员账本恢复，不依赖 Bot 运行状态、联系人或 Redis 通知队列。
// recover 接受可取消的上下文，每轮必须只处理有限批次；持久 claim 由业务 store 管理。
type RecoveryService struct {
	recover  func(context.Context) error
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	stopping bool
	started  bool
	done     chan struct{}
}

// NewRecoveryService 构造进程生命周期适配器；interval 非正值时使用一分钟。
// recover 为空时 Start 返回错误，避免装配遗漏被误报为成功。
func NewRecoveryService(recover func(context.Context) error, interval time.Duration) *RecoveryService {
	if interval <= 0 {
		interval = time.Minute
	}
	return &RecoveryService{recover: recover, interval: interval, done: make(chan struct{})}
}

// Name 返回运行器日志使用的固定服务名。
func (*RecoveryService) Name() string { return "telegram-membership-ledger" }

// Start 首次启动立即恢复，然后按间隔执行；parent 取消或 Stop 后等待当前批次退出。
// 不在事务内执行网络调用，不把异常详情或身份写入日志；返回装配/重复启动错误。
func (service *RecoveryService) Start(parent context.Context) error {
	service.mu.Lock()
	if service.started {
		service.mu.Unlock()
		return errors.New("membership recovery already started")
	}
	service.started = true
	ctx, cancel := context.WithCancel(parent)
	service.cancel = cancel
	if service.stopping {
		cancel()
	}
	service.mu.Unlock()
	defer close(service.done)
	defer cancel()
	if service.recover == nil {
		return errors.New("membership recovery callback is required")
	}
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		batch, stopBatch := context.WithTimeout(ctx, 45*time.Second)
		err := service.recoverBatch(batch)
		stopBatch()
		if err != nil && ctx.Err() == nil {
			logger.Warnw("membership_ledger_recovery_failed", "retry_scheduled", true)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// recoverBatch isolates a panic in the idempotent, local-only ledger recovery callback.
// ctx bounds the batch; a sanitized error keeps the lifecycle loop alive for its next tick.
// No external action may run here: network-side unknown outcomes belong to runtime workers.
func (service *RecoveryService) recoverBatch(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("membership ledger recovery panic")
			logger.Errorw("membership_ledger_recovery_panic", "retry_scheduled", true)
		}
	}()
	return service.recover(ctx)
}

// Stop 请求取消当前批次并等待完成；ctx 截止时返回取消错误，不虚报已排空。
func (service *RecoveryService) Stop(ctx context.Context) error {
	service.mu.Lock()
	service.stopping = true
	if service.cancel != nil {
		service.cancel()
	}
	service.mu.Unlock()
	select {
	case <-service.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
