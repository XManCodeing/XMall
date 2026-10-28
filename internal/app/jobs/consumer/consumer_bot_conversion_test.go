package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/XManCodeing/XMall/internal/queue"
	"github.com/hibiken/asynq"
)

type legacyConversionFake struct {
	calls int
	id    uint
	err   error
}

func (f *legacyConversionFake) ConvertLegacyNotification(_ context.Context, id uint, _ queue.BotNotifyPayload) error {
	f.calls++
	f.id = id
	return f.err
}

type legacyHistoryFake struct {
	id  uint
	err error
}

func (f legacyHistoryFake) ChannelForNotification(context.Context, queue.BotNotifyPayload) (uint, error) {
	return f.id, f.err
}
func TestLegacyConversionRequiresDurableSuccessAndExactHistory(t *testing.T) {
	ctx := context.Background()
	task := asynq.NewTask("test", []byte(`{"order_id":1,"telegram_user_id":"11"}`))
	converter := &legacyConversionFake{err: errors.New("durable write failed")}
	router := &RoutedLegacyNotify{Guard: legacyRouterFake{}, History: legacyHistoryFake{id: 7}, Converter: converter}
	if err := handleRoutedBotNotify(ctx, task, router); err == nil || errors.Is(err, asynq.SkipRetry) {
		t.Fatal("conversion failure incorrectly acknowledged/archived", err)
	}
	converter.err = nil
	if err := handleRoutedBotNotify(ctx, task, router); err != nil || converter.calls != 2 || converter.id != 7 {
		t.Fatal("history conversion failed", err)
	}
	router.History = nil
	if err := handleRoutedBotNotify(ctx, task, router); !errors.Is(err, asynq.SkipRetry) || converter.calls != 2 {
		t.Fatal("unmapped task guessed a bot", err)
	}
	router.Converter = nil
	explicit := asynq.NewTask("test", []byte(`{"order_id":1,"telegram_user_id":"11","channel_id":7}`))
	if err := handleRoutedBotNotify(ctx, explicit, router); !errors.Is(err, asynq.SkipRetry) {
		t.Fatal("missing converter was not isolated", err)
	}
}
