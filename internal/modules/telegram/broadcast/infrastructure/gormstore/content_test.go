package broadcaststore

import (
	"context"
	"testing"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
)

type contentTestBridge struct{ store *Store }

func (b contentTestBridge) Prepare(ctx context.Context, _ app.CreateInput) (string, int, error) {
	var n int64
	// Like production's recipient queries, this uses the shared connection.
	err := b.store.db.WithContext(ctx).Model(&d.Broadcast{}).Count(&n).Error
	return "sealed-snapshot", 1, err
}
func (b contentTestBridge) Enqueue(ctx context.Context, job *d.Broadcast) error {
	_, err := b.store.GetByID(ctx, job.ID)
	return err
}
func (contentTestBridge) Progress(context.Context, uint, uint) (app.Progress, error) {
	return app.Progress{Total: 1, Pending: 1}, nil
}
func (contentTestBridge) ReadContent(string) (app.BroadcastContent, error) {
	return app.BroadcastContent{MessageHTML: "private body", AttachmentURL: "/uploads/private.png", AttachmentName: "private.png", Buttons: [][]app.Button{{{Text: "Private", URL: "https://example.test/private"}}}}, nil
}
func TestContentIsolationOnSingleConnectionSQLite(t *testing.T) {
	repo := setupStoreTest(t)
	sqlDB, err := repo.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	svc := app.NewRuntimeService(repo, nil, contentTestBridge{repo}, nil)
	input := app.CreateInput{BotID: 7, Title: "test", RecipientType: "all", MessageHTML: "private body", AttachmentURL: "/uploads/private.png", AttachmentName: "private.png"}
	preview, err := svc.PreviewBroadcast(ctx, input)
	if err != nil || preview.RecipientCount != 1 {
		t.Fatalf("%+v %v", preview, err)
	}
	var total int64
	if err := repo.db.WithContext(ctx).Model(&d.Broadcast{}).Count(&total).Error; err != nil || total != 0 {
		t.Fatal("preview persisted", err)
	}
	job, err := svc.CreateBroadcast(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetBroadcast(ctx, job.ID)
	if err != nil || detail.MessageHTML != input.MessageHTML || len(detail.Buttons) != 1 {
		t.Fatalf("%+v %v", detail, err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, job.ID)
	if err != nil || stored.MessageHTML != "" || stored.AttachmentName != "" || stored.AttachmentURL != "" || stored.PayloadCipher != "sealed-snapshot" {
		t.Fatalf("content isolation failed: %+v %v", stored, err)
	}
}
