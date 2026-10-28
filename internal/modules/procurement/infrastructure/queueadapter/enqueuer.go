package queueadapter

import (
	"context"
	"errors"
	"time"

	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	"github.com/XManCodeing/XMall/internal/queue"

	"github.com/hibiken/asynq"
)

type contextEnqueuer struct {
	ctx    context.Context
	client interface {
		EnqueueProcurementSubmitContext(context.Context, queue.ProcurementSubmitPayload, ...asynq.Option) error
	}
}

func (e *Enqueuer) WithContext(ctx context.Context) (procurementcontract.Enqueuer, error) {
	client, ok := e.client.(interface {
		EnqueueProcurementSubmitContext(context.Context, queue.ProcurementSubmitPayload, ...asynq.Option) error
	})
	if !ok {
		return nil, errors.New("procurement queue does not support cancellation")
	}
	return &contextEnqueuer{ctx, client}, nil
}
func (e *contextEnqueuer) EnqueueSubmit(id uint, delay time.Duration) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	return e.client.EnqueueProcurementSubmitContext(e.ctx, queue.ProcurementSubmitPayload{ProcurementOrderID: id}, asynq.ProcessIn(delay))
}
func (e *contextEnqueuer) EnqueuePoll(uint, time.Duration) error {
	return errors.New("recovery cannot enqueue polling")
}

type Client interface {
	EnqueueProcurementSubmit(payload queue.ProcurementSubmitPayload, opts ...asynq.Option) error
	EnqueueProcurementPollStatus(payload queue.ProcurementPollStatusPayload, delay time.Duration) error
}

type Enqueuer struct{ client Client }

var _ procurementcontract.Enqueuer = (*Enqueuer)(nil)

func New(client Client) procurementcontract.Enqueuer {
	if client == nil {
		return nil
	}
	return &Enqueuer{client: client}
}

func (e *Enqueuer) EnqueueSubmit(orderID uint, delay time.Duration) error {
	if delay > 0 {
		return e.client.EnqueueProcurementSubmit(queue.ProcurementSubmitPayload{ProcurementOrderID: orderID}, asynq.ProcessIn(delay))
	}
	return e.client.EnqueueProcurementSubmit(queue.ProcurementSubmitPayload{ProcurementOrderID: orderID})
}

func (e *Enqueuer) EnqueuePoll(orderID uint, delay time.Duration) error {
	return e.client.EnqueueProcurementPollStatus(queue.ProcurementPollStatusPayload{ProcurementOrderID: orderID}, delay)
}
