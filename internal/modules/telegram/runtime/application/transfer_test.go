package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type transferRuntimeAPI struct {
	c.API
	cancel    context.CancelFunc
	result    c.TransferResult
	err       error
	calls     int
	protected bool
}

func (api *transferRuntimeAPI) Transfer(_ context.Context, _ string, request c.TransferRequest) (c.TransferResult, error) {
	api.calls++
	api.protected = request.ProtectContent
	if api.cancel != nil {
		api.cancel()
	}
	return api.result, api.err
}

type transferRuntimeStore struct {
	*deliveryGuardStore
	writes  int
	receipt c.DeliveryReceipt
}

func (store *transferRuntimeStore) FinishDeliveryReceipt(ctx context.Context, _ uint, _ string, status string, receipt c.DeliveryReceipt, _ string, _ time.Time) error {
	store.writes++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if store.writes == 1 {
		return errors.New("temporary persistence failure")
	}
	store.status, store.receipt = status, receipt
	store.cancel()
	return nil
}

func transferEnvelope() c.Message {
	return c.Message{TargetType: c.TargetSupportGroup, BusinessRef: "support:ready:1:2", RouteVersion: 1, ChatID: -100, ThreadID: 9, Operation: "send", Purpose: "support_ready", TargetKind: "album", Transfer: &c.TransferRequest{TargetType: c.TargetSupportGroup, BusinessRef: "support:ready:1:2", RouteVersion: 1, ChatID: -100, ThreadID: 9, Contents: []c.TransferContent{{Kind: "photo", FileID: "first"}, {Kind: "video", FileID: "second"}}}}
}

// TestSupportRateLimitDeferral preserves Telegram backoff without discarding queued work.
// t checks private challenges, topic forwards and cleanup using the shared classifier.
func TestSupportRateLimitDeferral(t *testing.T) {
	runtime := &Runtime{}
	for _, message := range []c.Message{
		{TargetType: c.TargetSupportPrivate, Purpose: "support_gate", Operation: "send"},
		{TargetType: c.TargetSupportPrivate, Purpose: "support_gate_cleanup", Operation: "delete"},
		transferEnvelope(),
	} {
		before := time.Now()
		status, _, available, nextBot := runtime.classifyDelivery(context.Background(), &d.Instance{ID: 1}, &d.Delivery{}, message, retry429{})
		if status != "retry" || available.Before(before.Add(5*time.Second)) || !nextBot.Equal(available) {
			t.Fatal("rate limit did not defer queued delivery and bot", status, available, nextBot)
		}
		limit := 8
		if message.Operation == "delete" {
			limit = 3
		}
		status, _, _, _ = runtime.classifyDelivery(context.Background(), &d.Instance{ID: 1}, &d.Delivery{Attempts: limit}, message, retry429{})
		if status != "failed" {
			t.Fatal("exhausted delivery retried without a bound", status)
		}
	}
}

func TestTransferDurableReceiptCancellationAndPartialOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		ids    []int64
		err    error
		status string
	}{
		{"complete", []int64{5, 6}, nil, "sent"}, {"partial", []int64{5}, nil, "unknown"},
		{"empty", nil, nil, "unknown"}, {"duplicate", []int64{5, 5}, nil, "unknown"},
		{"zero", []int64{5, 0}, nil, "unknown"}, {"partial429", []int64{5}, retry429{}, "unknown"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			store := &transferRuntimeStore{deliveryGuardStore: &deliveryGuardStore{cancel: cancel}}
			api := &transferRuntimeAPI{cancel: cancel, err: scenario.err}
			for _, id := range scenario.ids {
				api.result.Messages = append(api.result.Messages, c.DeliveryResult{MessageID: id})
			}
			manager := NewManager(store, api, "test-key")
			message := transferEnvelope()
			raw, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := manager.Seal(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			store.item = &d.Delivery{ID: 1, TargetType: "support_group", ChatID: -100, Purpose: "support_ready", Operation: "send", PayloadCipher: cipher}
			NewRuntime(manager, nil, &targetHandler{routedHandler: &routedHandler{}}).deliver(ctx, &d.Instance{ID: 1}, "dummy", &lifecycleLease{}, cancel)
			if api.calls != 1 || !api.protected || store.writes != 2 || store.status != scenario.status || !reflect.DeepEqual(store.receipt.MessageIDs, scenario.ids) {
				t.Fatalf("calls=%d protected=%v writes=%d status=%s ids=%v", api.calls, api.protected, store.writes, store.status, store.receipt.MessageIDs)
			}
		})
	}
}

// TestMissingTopicClassification retains only explicit rejection evidence; t
// verifies uncertain errors and unrelated targets cannot trigger topic recovery.
func TestMissingTopicClassification(t *testing.T) {
	runtime := &Runtime{}
	for _, scenario := range []struct {
		name               string
		uncertain, private bool
	}{
		{name: "missing"}, {name: "unknown", uncertain: true}, {name: "private", private: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			message := transferEnvelope()
			if scenario.private {
				message.TargetType = c.TargetSupportPrivate
			}
			status, detail, _, _ := runtime.classifyDelivery(context.Background(), &d.Instance{ID: 1}, &d.Delivery{}, message, applyingFailure{kind: "topic_missing", uncertain: scenario.uncertain})
			eligible := !scenario.uncertain && !scenario.private
			if (detail == "topic_missing") != eligible || (eligible && status != "failed") {
				t.Fatalf("classification %s %s", status, detail)
			}
		})
	}
}

type retry429 struct{}

func (retry429) Error() string     { return "rate limited" }
func (retry429) StatusCode() int   { return 429 }
func (retry429) RetrySeconds() int { return 5 }
func (retry429) Uncertain() bool   { return false }

func TestTransferEnvelopeRejectsMismatchAndMixedLegacyContent(t *testing.T) {
	for _, mutate := range []func(*c.Message){
		func(message *c.Message) { message.Transfer.ChatID = -200 }, func(message *c.Message) { message.Transfer.ThreadID = 10 },
		func(message *c.Message) { message.Transfer.RouteVersion = 2 }, func(message *c.Message) { message.Transfer.BusinessRef = "other" },
		func(message *c.Message) { message.Transfer.TargetType = c.TargetShopPrivate }, func(message *c.Message) { message.Text = "mixed" },
		func(message *c.Message) { message.Buttons = [][]c.Button{{{Text: "unauthorized", Data: "x"}}} }, func(message *c.Message) { message.PhotoURL = "https://invalid" },
		func(message *c.Message) { message.Purpose = "support_gate" }, func(message *c.Message) { message.TargetKind = "text" },
	} {
		message := transferEnvelope()
		mutate(&message)
		if _, err := deliveryTarget(&d.Delivery{TargetType: "support_group", ChatID: -100, Purpose: "support_ready", Operation: "send"}, message); err == nil {
			t.Fatal("accepted invalid envelope", message)
		}
	}
}

func TestTransferMissingOptionalCapabilitiesNeverSends(t *testing.T) {
	api := &transferRuntimeAPI{}
	runtime := NewRuntime(NewManager(&deliveryGuardStore{}, api, "test-key"), nil, nil)
	if _, _, err := runtime.executeTransfer(context.Background(), "dummy", transferEnvelope()); err == nil || api.calls != 0 {
		t.Fatal("sent without receipt storage")
	}
	runtime = NewRuntime(NewManager(&transferRuntimeStore{}, &receiptAPI{}, "test-key"), nil, nil)
	if _, _, err := runtime.executeTransfer(context.Background(), "dummy", transferEnvelope()); err == nil {
		t.Fatal("fell back to Apply")
	}
}
