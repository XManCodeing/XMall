package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// TestLegacyRejectedReadyPreservesUnknown checks worker repair of old buffered content.
func TestLegacyRejectedReadyPreservesUnknown(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			fixture := newGuestGate(t)
			ctx := r.WithExecutionFence(context.Background(), guestFence{})
			buffered := prepareGuestTopic(t, fixture, ctx)
			source := guestMessage(1)
			source.HasProtectedContent = true
			raw, err := json.Marshal(source)
			mustGuest(t, err)
			cipher, err := fixture.manager.Seal(string(raw))
			mustGuest(t, err)
			mustGuest(t, fixture.db.Model(&buffered).Update("payload_cipher", cipher).Error)
			if pending {
				mustGuest(t, fixture.db.Create(&rd.Delivery{BotID: 1, Key: c.ReadyBusinessRef(buffered.SessionID, buffered.ID), Status: "unknown", PayloadCipher: "evidence"}).Error)
			}
			worker := a.ReadyWorker{Store: fixture.repo, Consumer: fixture.repo, Queue: guestQueue{rs.New(fixture.db)}, Builder: a.TransferReadyBuilder{}, NewClaim: func() string { return "reject-legacy" }}
			_, err = worker.Tick(ctx, 1, 1, 0, 10, time.Now().UTC())
			if pending && err == nil {
				t.Fatal("unknown dependency ignored")
			}
			if !pending {
				mustGuest(t, err)
				mustGuest(t, fixture.service.Reconcile(ctx, 1))
			}
			mustGuest(t, fixture.db.First(&buffered, buffered.ID).Error)
			if pending && buffered.PayloadCipher == "" {
				t.Fatal("unknown evidence deleted")
			}
			if !pending && (buffered.State != "rejected" || buffered.PayloadCipher != "") {
				t.Fatal("legacy unsafe content still blocks")
			}
		})
	}
}

// TestSupportProductionRequestsPrecedeMarketing uses real upstream/reply builders and queue claims.
// Ordinary topic messages intentionally have no reply reference.
func TestSupportProductionRequestsPrecedeMarketing(t *testing.T) {
	fixture := newGuestGate(t)
	ctx := r.WithExecutionFence(context.Background(), guestFence{})
	queue := rs.New(fixture.db)
	buffered := prepareGuestTopic(t, fixture, ctx)
	fixture.repo.WithReadyEncoder(c.EncodeReadyTransfer)
	now := time.Now().UTC()
	claimed, err := fixture.repo.ClaimReady(ctx, 1, buffered.SessionID, 1, "priority", now, time.Minute)
	mustGuest(t, err)
	prepared, err := fixture.repo.LoadReady(ctx, 1, claimed.ID, 1, "priority", now)
	mustGuest(t, err)
	envelope, err := c.EncodeReadyTransfer(prepared.Source, prepared.Binding, c.ReadyBusinessRef(buffered.SessionID, claimed.ID))
	mustGuest(t, err)
	request, err := fixture.repo.PrepareDispatch(ctx, 1, claimed.ID, 1, "priority", envelope, now)
	mustGuest(t, err)
	mustGuest(t, queue.EnqueueRequest(ctx, &request))
	replies := &a.ReplyService{Store: fixture.repo, Replies: fixture.repo, Members: guestMembers{}, Queue: guestQueue{queue}, Now: time.Now}
	_, err = replies.Handle(ctx, 1, r.Update{ID: 3, Type: "message", MessageKind: "text", ChatType: "supergroup", UserID: 55, ChatID: -100, ThreadID: 20, IsTopicMessage: true, MessageID: 80, Text: "ordinary staff reply"})
	mustGuest(t, err)
	// Completed setup notices are not part of the mixed backlog under test.
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose IN ?", []string{"support_gate", "support_gate_cleanup"}).Update("status", "sent").Error)
	mustGuest(t, queue.Enqueue(ctx, &rd.Delivery{BotID: 1, Key: "marketing", ChatID: 44, Priority: r.PriorityMarketing}))
	mustGuest(t, queue.Enqueue(ctx, &rd.Delivery{BotID: 1, Key: "transaction", ChatID: 45, Priority: r.PriorityTransaction}))
	for _, expected := range []string{"transaction", request.Key, "support:reply:1", "marketing"} {
		item, err := queue.ClaimDeliveryExcluding(ctx, 1, 1, time.Now().Add(time.Second), nil)
		mustGuest(t, err)
		if item == nil || item.Key != expected {
			t.Fatalf("expected %s, got %+v", expected, item)
		}
		mustGuest(t, queue.FinishDelivery(ctx, item.ID, item.Claim, "sent", 99, "", now))
	}
}

// TestAlbumRejectionFeedback covers durable rejection lookup, queuing and later verification.
// Each fixture uses synthetic local data and never calls Telegram.
func TestAlbumRejectionFeedback(t *testing.T) {
	for _, scenario := range []string{"late", "count", "bytes"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newGuestGate(t)
			now := time.Now().UTC()
			fixture.service.Now = func() time.Time { return now }
			policy := c.DefaultPolicy()
			policy.MaxBytes = 4 * 1024 * 1024
			fixture.service.Policy = policy
			source := guestMessage(1)
			source.Text, source.MessageKind, source.MediaGroupID = "", "photo", "album"
			source.Media = []r.Media{{Type: "photo", FileID: "synthetic"}}
			size, count := int64(100), 1
			if scenario == "bytes" {
				size = 300 * 1024
			}
			if scenario == "count" {
				count = 10
			}
			for index := 0; index < count; index++ {
				source.ID, source.MessageID = int64(index+1), int64(index+1)
				_, err := fixture.repo.Accept(context.Background(), 1, source, "synthetic-cipher", size, fmt.Sprint("album-", index), policy, now)
				mustGuest(t, err)
			}
			if scenario == "late" {
				now = now.Add(3 * time.Second)
			}
			source.ID, source.MessageID = 20, 20
			_, err := fixture.repo.Accept(context.Background(), 1, source, "rejected-cipher", size, "extra", policy, now)
			mustGuest(t, err)
			var rejected d.BufferedMessage
			mustGuest(t, fixture.db.Where("update_id = 20").First(&rejected).Error)
			if rejected.State != "rejected" || rejected.Reason != "album_late_or_limit" || rejected.PayloadCipher != "" {
				t.Fatal(rejected.State, rejected.Reason)
			}
			notice, err := fixture.repo.Notice(context.Background(), 1, fmt.Sprintf("message:%d", rejected.ID))
			mustGuest(t, err)
			if notice.Reason != rejected.Reason {
				t.Fatal(notice)
			}
			other := guestMessage(30)
			other.ChatID, other.UserID = 43, 43
			_, err = fixture.service.Handle(context.Background(), 1, other)
			mustGuest(t, err)
			var deliveries []rd.Delivery
			mustGuest(t, fixture.db.Find(&deliveries).Error)
			feedback, later := false, false
			for _, delivery := range deliveries {
				envelope := gateEnvelope(t, fixture, delivery)
				mustGuest(t, fixture.service.AuthorizeTarget(context.Background(), 1, envelope))
				feedback = feedback || strings.Contains(envelope.Text, "拆分")
				later = later || delivery.ChatID == 43
			}
			if !feedback || !later {
				t.Fatal("rejected item blocked feedback/challenge", feedback, later)
			}
		})
	}
}

// TestPoisonNoticeIsolation preserves error evidence while allowing following notices through.
func TestPoisonNoticeIsolation(t *testing.T) {
	fixture := newGuestGate(t)
	now := time.Now().UTC()
	poison := d.BufferedMessage{BotID: 1, UserID: 42, ChatID: 42, UpdateID: 90, Reason: "legacy-invalid", State: "rejected", FeedbackPending: true, CreatedAt: now, UpdatedAt: now}
	mustGuest(t, fixture.db.Create(&poison).Error)
	_, err := fixture.service.Handle(context.Background(), 1, guestMessage(91))
	if err != nil {
		t.Fatal("bad unrelated notice must not fail committed intake", err)
	}
	mustGuest(t, fixture.db.First(&poison, poison.ID).Error)
	if poison.FeedbackPending || poison.ErrorClass != "notice_invalid" {
		t.Fatal("poison not quarantined")
	}
	var queued int64
	mustGuest(t, fixture.db.Model(&rd.Delivery{}).Count(&queued).Error)
	if queued != 1 {
		t.Fatal("later verification blocked", queued)
	}
	mustGuest(t, fixture.service.Reconcile(context.Background(), 1))
}

// TestRejectedContentFeedback checks protected and unsupported sources stay body-free.
func TestRejectedContentFeedback(t *testing.T) {
	for _, scenario := range []string{"protected", "unsupported", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newGuestGate(t)
			source := guestMessage(1)
			if scenario == "protected" {
				source.HasProtectedContent = true
			} else {
				source.Text, source.MessageKind = "", "video_note"
				source.Media = []r.Media{{Type: "video_note", FileID: "synthetic"}}
			}
			if scenario == "unknown" {
				source.Text, source.MessageKind, source.Media = "", "unknown", nil
			}
			for range 2 {
				_, err := fixture.dispatcher.Handle(context.Background(), 1, source)
				mustGuest(t, err)
			}
			var rows []d.BufferedMessage
			mustGuest(t, fixture.db.Find(&rows).Error)
			if len(rows) != 1 || rows[0].State != "rejected" || rows[0].PayloadCipher != "" {
				t.Fatal("unsafe content retained")
			}
			var deliveries []rd.Delivery
			mustGuest(t, fixture.db.Find(&deliveries).Error)
			if len(deliveries) != 1 {
				t.Fatal("feedback lost/duplicated", len(deliveries))
			}
			envelope := gateEnvelope(t, fixture, deliveries[0])
			if !strings.Contains(envelope.Text, "未保存或送达") {
				t.Fatal(envelope.Text)
			}
			mustGuest(t, fixture.service.AuthorizeTarget(context.Background(), 1, envelope))
		})
	}
}

// TestRejectedUpstreamRetention keeps audit metadata and unresolved album/transport evidence.
func TestRejectedUpstreamRetention(t *testing.T) {
	fixture := newGuestGate(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	rows := []d.BufferedMessage{}
	for index, reason := range []string{"wrong", "album_late_or_limit", "wrong", "wrong", "wrong", "wrong"} {
		row := d.BufferedMessage{BotID: 1, UpdateID: int64(index + 1), SessionID: 1, UserID: 42, ChatID: 42, State: "rejected", Reason: reason, PayloadCipher: "retained", CreatedAt: old, UpdatedAt: old, RetainUntil: old}
		mustGuest(t, fixture.db.Create(&row).Error)
		rows = append(rows, row)
	}
	album := d.Album{BotID: 1, ChatID: 42, SessionID: 1, LeaderID: rows[2].ID, MediaGroupID: "held", Count: 2}
	mustGuest(t, fixture.db.Create(&album).Error)
	for _, index := range []int{2, 3} {
		mustGuest(t, fixture.db.Create(&d.AlbumItem{AlbumID: album.ID, BufferedMessageID: rows[index].ID, SourceMessageID: int64(index + 1)}).Error)
	}
	mustGuest(t, fixture.db.Model(&rows[3]).UpdateColumn("state", "unknown").Error)
	mustGuest(t, fixture.db.Create(&d.ReadyDispatch{BotID: 1, MessageID: rows[4].ID, PayloadCipher: "prepared"}).Error)
	mustGuest(t, fixture.db.Create(&rd.Delivery{BotID: 1, Key: c.ReadyBusinessRef(1, rows[5].ID), Status: "unknown", PayloadCipher: "unknown"}).Error)
	for range 2 {
		mustGuest(t, fixture.repo.Expire(context.Background(), 1, now))
	}
	for index := range rows {
		mustGuest(t, fixture.db.First(&rows[index], rows[index].ID).Error)
		if (rows[index].PayloadCipher == "") != (index < 2) {
			t.Fatalf("row %d dependency/retention mismatch", index)
		}
		if rows[index].Reason == "" || !rows[index].UpdatedAt.Equal(old) {
			t.Fatal("audit changed")
		}
	}
}
