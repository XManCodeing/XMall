package broadcaststore

import (
	"context"
	"testing"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

type auditBridge struct{ enqueued, checked int }

// Prepare is unused by recovery and returns no audience.
func (*auditBridge) Prepare(context.Context, app.CreateInput) (string, int, error) { return "", 0, nil }

// Enqueue records unexpected repeated materialization of an already complete ledger.
func (bridge *auditBridge) Enqueue(context.Context, *d.Broadcast) error {
	bridge.enqueued++
	return nil
}

// Progress simulates an active complete delivery ledger without external requests.
func (bridge *auditBridge) Progress(context.Context, uint, uint) (app.Progress, error) {
	bridge.checked++
	return app.Progress{Total: 1, Pending: 1}, nil
}

// TestReconcilePagesActiveJobsWithoutReenqueue verifies status filtering and cursor fairness.
func TestReconcilePagesActiveJobsWithoutReenqueue(t *testing.T) {
	store := setupStoreTest(t)
	ctx := context.Background()
	for index := 0; index < 120; index++ {
		status := "pending"
		if index < 60 {
			status = "failed"
		}
		if err := store.Create(ctx, &d.Broadcast{BotID: 1, Status: status, DeliveryVersion: 1, PayloadCipher: "synthetic", RecipientCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := store.List(ctx, c.ListFilter{ActiveOnly: true, Keyset: true, PageSize: 50})
	if err != nil || len(rows) != 50 || rows[0].ID != 61 || rows[49].ID != 110 {
		t.Fatal("active keyset page incorrect", len(rows), err)
	}
	bridge := &auditBridge{}
	service := app.NewRuntimeService(store, nil, bridge, nil)
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if bridge.checked != 50 || bridge.enqueued != 0 {
		t.Fatal("tick was unbounded or repeated enqueue", bridge.checked, bridge.enqueued)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if bridge.checked != 60 || bridge.enqueued != 0 {
		t.Fatal("cursor starved later jobs", bridge.checked, bridge.enqueued)
	}
}
