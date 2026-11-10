package gormstore

import (
	"context"
	"encoding/json"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rstore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCleanupPreservesQueuedAlbumProof uses real runtime enqueue and cleanup
// before business acknowledgment, then verifies terminal ciphertext deletion.
func TestCleanupPreservesQueuedAlbumProof(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	if err := db.AutoMigrate(&rd.Inbox{}, &rd.Session{}); err != nil {
		t.Fatal(err)
	}
	leader, err := s.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	source.ID++
	source.MessageID++
	follower, err := s.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.DownstreamRequest(ctx, 1, leader.ID, now.Add(3*time.Second))
	if err != nil || request == nil {
		t.Fatal(request, err)
	}
	queue := rstore.New(db)
	if err = queue.EnqueueRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-8 * 24 * time.Hour)
	if err = db.Model(&rd.Delivery{}).Where("key = ?", request.Key).UpdateColumns(map[string]any{"status": "sent", "message_id": 700, "message_ids": "[700,701]", "updated_at": old}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&d.Downstream{}).Where("bot_id = 1").UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.Expire(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	if err = queue.Cleanup(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var proof rd.Delivery
	if err = db.Where("key = ?", request.Key).First(&proof).Error; err != nil || !proof.PayloadHeld || proof.PayloadCipher == "" {
		t.Fatal(proof.ID, err)
	}
	restarted := New(db, s.cipher)
	if state, err := restarted.ReconcileDownstream(ctx, 1, leader.ID, 1, now); err != nil || state != "delivered" {
		t.Fatal(state, err)
	}
	if err = restarted.Expire(ctx, 1, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{leader.ID, follower.ID} {
		var row d.Downstream
		if err = db.First(&row, id).Error; err != nil || row.SourceCipher != "" || row.EnvelopeCipher != "" || row.DestinationMessageID < 700 {
			t.Fatal(row.ID, err)
		}
	}
	if state, err := restarted.ReconcileDownstream(ctx, 1, leader.ID, 1, now.Add(8*24*time.Hour)); err != nil || state != "delivered" {
		t.Fatal(state, err)
	}
}

// TestRejectedAlbumCleanupKeepsRealProof demonstrates that late/overflow rows
// are permanent nonmembers: cleanup is paged without altering the leader proof.
func TestRejectedAlbumCleanupKeepsRealProof(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	leader, err := s.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	// Force the real byte-limit branch without fabricating a rejected state.
	if err = db.Model(&d.DownstreamAlbum{}).Where("id = ?", leader.AlbumID).UpdateColumn("bytes", 512*1024).Error; err != nil {
		t.Fatal(err)
	}
	source.ID++
	source.MessageID++
	rejected, err := s.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil || rejected.State != "rejected" {
		t.Fatal(err)
	}
	request, err := s.DownstreamRequest(ctx, 1, leader.ID, now.Add(3*time.Second))
	if err != nil || request == nil {
		t.Fatal(err)
	}
	queue := rstore.New(db)
	if err = queue.EnqueueRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	// More than one page of real late arrivals must not expand as album members.
	for index := 0; index < 100; index++ {
		source.ID++
		source.MessageID++
		row, err := s.AcceptDownstream(ctx, 1, source, false, now.Add(3*time.Second))
		if err != nil || row.State != "rejected" {
			t.Fatal(err)
		}
	}
	later := now.Add(8 * 24 * time.Hour)
	for _, remaining := range []int64{1, 0} {
		if err = s.Expire(ctx, 1, later); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err = db.Model(&d.Downstream{}).Where("state = 'rejected' AND source_cipher <> ''").Count(&count).Error; err != nil || count != remaining {
			t.Fatal(count, err)
		}
	}
	if request, err := s.DownstreamRequest(ctx, 1, rejected.ID, later); err != nil || request != nil {
		t.Fatal("rejected retry", err)
	}
	var current d.Downstream
	if err = db.First(&current, leader.ID).Error; err != nil || current.SourceCipher == "" || current.EnvelopeCipher == "" {
		t.Fatal("leader lost", err)
	}
	if err = db.Model(&rd.Delivery{}).Where("key = ?", request.Key).UpdateColumns(map[string]any{"status": "sent", "message_id": 700, "message_ids": "[700]"}).Error; err != nil {
		t.Fatal(err)
	}
	if state, err := s.ReconcileDownstream(ctx, 1, leader.ID, 1, later); err != nil || state != "delivered" {
		t.Fatal(state, err)
	}
}

func TestAlbumBoundedOrderedReceiptRecovery(t *testing.T) {
	s, db := fixture(t)
	s.cipher = app.NewManager(nil, nil, "album-test")
	s.WithReadyEncoder(c.EncodeReadyTransfer)
	ctx := context.Background()
	now := time.Now().UTC()
	var session uint
	for index, messageID := range []int64{12, 11} {
		source := r.Update{ID: int64(index + 1), Private: true, UserID: 10, ChatID: 10, MessageID: messageID, MessageKind: "photo", MediaGroupID: "group", Caption: "caption", Media: []r.Media{{Type: "photo", FileID: "large", Width: 100, Height: 100}}}
		raw, _ := json.Marshal(source)
		encrypted, e := s.cipher.Seal(string(raw))
		if e != nil {
			t.Fatal(e)
		}
		out, e := s.Accept(ctx, 1, source, encrypted, int64(len(raw)), "album-ref", c.DefaultPolicy(), now)
		if e != nil {
			t.Fatal(e)
		}
		session = out.Session.ID
	}
	challenge, err := s.Challenge(ctx, "album-ref")
	if err != nil {
		t.Fatal(err)
	}
	if result, e := s.Verify(ctx, 1, r.Update{ID: 3, UserID: 10, ChatID: 10}, c.VerificationAnswer(*challenge, challenge.AnswerIndex), c.DefaultPolicy(), now); e != nil || result.Reason != "passed" {
		t.Fatal(result, e)
	}
	binding := c.DeliveryBinding{ChatID: -100, ThreadID: 20, RouteVersion: 1}
	installTestTopic(t, s, db, session, binding)
	if row, e := s.ClaimReady(ctx, 1, session, 1, "early", now, time.Minute); e != nil || row != nil {
		t.Fatal("unbounded collection", row, e)
	}
	later := now.Add(3 * time.Second)
	leader, e := s.ClaimReady(ctx, 1, session, 1, "ready", later, time.Minute)
	if e != nil || leader == nil {
		t.Fatal(leader, e)
	}
	prepared, e := s.LoadReady(ctx, 1, leader.ID, 1, "ready", later)
	if e != nil || len(prepared.Sources) != 2 || prepared.Sources[0].MessageID != 11 {
		t.Fatal(prepared, e)
	}
	envelope, e := c.EncodeReadyAlbum(prepared.Sources, binding, c.ReadyBusinessRef(session, leader.ID))
	if e != nil {
		t.Fatal(e)
	}
	request, e := s.PrepareDispatch(ctx, 1, leader.ID, 1, "ready", envelope, later)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeTargetAt(ctx, 1, envelope, later); e != nil {
		t.Fatal(e)
	}
	rows, e := s.ScanReady(ctx, 1, 0, 100)
	if e != nil || len(rows) != 1 {
		t.Fatal("followers scheduled", rows, e)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: -100, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "unknown", MessageID: 70, MessageIDs: []int64{70}, PayloadHeld: true}
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileReady(ctx, 1, leader.ID, 1, later); e != nil || state != "unknown" {
		t.Fatal(state, e)
	}
	var count int64
	db.Model(&d.ReplyMapping{}).Count(&count)
	if count != 0 {
		t.Fatal("partial mapped", count)
	}
	if e = db.Model(&proof).Updates(map[string]any{"status": "sent", "message_ids": "[70,71]"}).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileReady(ctx, 1, leader.ID, 1, later); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	db.Model(&d.ReplyMapping{}).Count(&count)
	if count != 2 {
		t.Fatal(count)
	}
	var items []d.AlbumItem
	db.Order("source_message_id ASC").Find(&items)
	if items[0].DestinationMessageID != 70 || items[1].DestinationMessageID != 71 {
		t.Fatal(items)
	}
	var saved rd.Delivery
	db.First(&saved, proof.ID)
	if saved.PayloadHeld {
		t.Fatal("proof hold leaked")
	}
	db.Model(&d.BufferedMessage{}).Where("state = ?", "delivered").Count(&count)
	if count != 2 {
		t.Fatal(count)
	}
}

func TestSupportOnlyBlockExpiryAndClosedConsultation(t *testing.T) {
	s, db, original, _, envelope, now := preparedFixture(t)
	ctx := context.Background()
	expiry := now.Add(time.Minute)
	if e := s.SetSupportBlock(ctx, 1, original.SessionID, 55, 0, true, &expiry, "ticket:temporary", now); e != nil {
		t.Fatal(e)
	}
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal("blocked send", e)
	}
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, expiry); e != nil {
		t.Fatal("expired block", e)
	}
	var session d.Session
	if e := db.First(&session, original.SessionID).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.CloseConsultation(ctx, 1, session.ID, session.Version, expiry); e != nil {
		t.Fatal(e)
	}
	if e := s.AuthorizeTargetAt(ctx, 1, envelope, expiry); e != c.ErrDenied {
		t.Fatal("closed send", e)
	}
	if _, e := s.Accept(ctx, 1, r.Update{ID: 99, UserID: 10, ChatID: 10}, "encrypted", 9, "new", c.DefaultPolicy(), expiry); e != c.ErrDenied {
		t.Fatal("closed auto reopened", e)
	}
}

func TestAlbumLateArrivalAndOverflowNeverJoinExistingAttempt(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for index := 1; index <= 12; index++ {
		source := r.Update{ID: int64(index), Private: true, UserID: 10, ChatID: 10, MessageID: int64(index), MessageKind: "photo", MediaGroupID: "bounded", Media: []r.Media{{Type: "photo", FileID: "file"}}}
		at := now
		if index == 12 {
			at = now.Add(3 * time.Second)
		}
		if _, e := s.Accept(ctx, 1, source, "encrypted", 10, "ref", c.DefaultPolicy(), at); e != nil {
			t.Fatal(e)
		}
	}
	var album d.Album
	if e := db.First(&album).Error; e != nil || album.Count != 10 {
		t.Fatal(album, e)
	}
	var count int64
	if e := db.Model(&d.BufferedMessage{}).Where("state = ? AND reason = ?", "rejected", "album_late_or_limit").Count(&count).Error; e != nil || count != 2 {
		t.Fatal(count, e)
	}
}

func downstreamAlbumFixture(t *testing.T) (*Store, *gorm.DB, time.Time, r.Update) {
	t.Helper()
	s, db, original, request, _, now := preparedFixture(t)
	if e := db.AutoMigrate(&d.DownstreamAlbum{}); e != nil {
		t.Fatal(e)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: -100, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "sent", MessageID: 77, PayloadHeld: true}
	if e := db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileReady(context.Background(), 1, original.ID, 1, now); e != nil {
		t.Fatal(e)
	}
	source := r.Update{ID: 90, Type: "message", UserID: 55, ChatID: -100, MessageID: 88, ThreadID: 20, IsTopicMessage: true, MessageKind: "photo", MediaGroupID: "down-album", Media: []r.Media{{Type: "photo", FileID: "large"}}, Reply: &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 20}}
	return s, db, now, source
}

func TestDownstreamAlbumRestartOrderedProofAndResolution(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	leader, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil {
		t.Fatal(e)
	}
	source.ID++
	source.MessageID--
	follower, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil {
		t.Fatal(e)
	}
	if request, e := s.DownstreamRequest(ctx, 1, leader.ID, now); e != nil || request != nil {
		t.Fatal("early send", request, e)
	}
	if request, e := s.DownstreamRequest(ctx, 1, follower.ID, now.Add(3*time.Second)); e != nil || request != nil {
		t.Fatal("follower sent", request, e)
	}
	restarted := New(db, s.cipher)
	allowTestRoute(restarted, c.DeliveryBinding{ChatID: -100, RouteVersion: 1})
	later := now.Add(3 * time.Second)
	request, e := restarted.DownstreamRequest(ctx, 1, leader.ID, later)
	if e != nil || request == nil {
		t.Fatal(request, e)
	}
	plain, e := s.cipher.Open(request.PayloadCipher)
	if e != nil {
		t.Fatal(e)
	}
	var envelope r.Message
	if e = json.Unmarshal([]byte(plain), &envelope); e != nil || len(envelope.Transfer.Contents) != 2 {
		t.Fatal(envelope, e)
	}
	if envelope.Transfer.SourceChatID != 0 || len(envelope.Transfer.SourceMessageIDs) != 0 || envelope.ChatID != 10 || envelope.ThreadID != 0 {
		t.Fatal("downstream album leaked upstream forwarding references", envelope.Transfer)
	}
	if _, e = restarted.CheckDownstream(ctx, 1, envelope, later); e != nil {
		t.Fatal(e)
	}
	// A changed reference on either item invalidates authorization of the whole album.
	if e = db.Model(&d.Downstream{}).Where("id = ?", follower.ID).Update("reply_mapping_id", 999).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.CheckDownstream(ctx, 1, envelope, later); e != c.ErrDenied {
		t.Fatal("untrusted follower", e)
	}
	if e = db.Model(&d.Downstream{}).Where("id = ?", follower.ID).Update("reply_mapping_id", leader.ReplyMappingID).Error; e != nil {
		t.Fatal(e)
	}
	verifyDownstreamAlbumResolution(t, s, db, leader.ID, follower.ID, *request, later)
}

// verifyDownstreamAlbumResolution checks partial/forged receipts and audited CAS.
func verifyDownstreamAlbumResolution(t *testing.T, s *Store, db *gorm.DB, leaderID, followerID uint, request r.DeliveryRequest, later time.Time) {
	t.Helper()
	ctx := context.Background()
	var leader d.Downstream
	if err := db.First(&leader, leaderID).Error; err != nil {
		t.Fatal(err)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: 10, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "unknown", MessageID: 100, MessageIDs: []int64{100}, PayloadHeld: true}
	var e error
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileDownstream(ctx, 1, leader.ID, 1, later); e != nil || state != "unknown" {
		t.Fatal(state, e)
	}
	var current d.Downstream
	if e = db.First(&current, leader.ID).Error; e != nil {
		t.Fatal(e)
	}
	resolution := c.Resolution{ActorID: 9, ExpectedVersion: current.Version, Decision: "delivered", DeliveryID: proof.ID, Evidence: "ticket:receipt"}
	if e = s.ResolveDownstream(ctx, 1, leader.ID, 1, resolution, later); e != c.ErrDenied {
		t.Fatal("admin assertion delivered", e)
	}
	if e = db.Model(&proof).Update("status", "failed").Error; e != nil {
		t.Fatal(e)
	}
	resolution.Decision = "failed"
	if e = s.ResolveDownstream(ctx, 1, leader.ID, 1, resolution, later); e != c.ErrDenied {
		t.Fatal("partial retried", e)
	}
	if e = db.Model(&proof).Updates(map[string]any{"status": "sent", "message_ids": "[100,101]"}).Error; e != nil {
		t.Fatal(e)
	}
	resolution.Decision = "delivered"
	if e = s.ResolveDownstream(ctx, 1, leader.ID, 999, resolution, later); e != c.ErrConflict {
		t.Fatal("forged generation", e)
	}
	if e = s.ResolveDownstream(ctx, 1, leader.ID, 1, resolution, later); e != nil {
		t.Fatal(e)
	}
	if e = s.ResolveDownstream(ctx, 1, leader.ID, 1, resolution, later); e != c.ErrConflict {
		t.Fatal("CAS replay", e)
	}
	var parts []d.Downstream
	if e = db.Where("album_id = ?", leader.AlbumID).Order("source_message_id ASC").Find(&parts).Error; e != nil {
		t.Fatal(e)
	}
	if parts[0].ID != followerID || parts[0].DestinationMessageID != 100 || parts[1].DestinationMessageID != 101 || parts[0].State != "delivered" {
		t.Fatal(parts)
	}
	if e = db.First(&proof, proof.ID).Error; e != nil || proof.PayloadHeld {
		t.Fatal(proof, e)
	}
	var audits int64
	db.Model(&d.ResolutionAudit{}).Where("resource = ?", "downstream").Count(&audits)
	if audits != 1 {
		t.Fatal(audits)
	}
}

func TestDownstreamAlbumBoundsAndParallelDedup(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, e := s.AcceptDownstream(ctx, 1, source, false, now); e != nil {
				t.Error(e)
			}
		}()
	}
	wait.Wait()
	for index := 1; index <= 11; index++ {
		next := source
		next.ID += int64(index)
		next.MessageID += int64(index)
		at := now
		if index == 11 {
			at = now.Add(3 * time.Second)
		}
		row, e := s.AcceptDownstream(ctx, 1, next, false, at)
		if e != nil {
			t.Fatal(e)
		}
		if index >= 10 && row.State != "rejected" {
			t.Fatal("overflow accepted", row)
		}
	}
	var album d.DownstreamAlbum
	if e := db.First(&album).Error; e != nil || album.Count != 10 {
		t.Fatal(album, e)
	}
	rows, e := s.ScanDownstream(ctx, 1, 0, 100)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	source.ID = 200
	source.MessageID = 200
	source.MediaGroupID = "bytes"
	source.Caption = strings.Repeat("x", 270*1024)
	first, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil || first.State != "collecting" {
		t.Fatal(first, e)
	}
	source.ID++
	source.MessageID++
	second, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil || second.State != "rejected" {
		t.Fatal(second, e)
	}
}

func TestDownstreamAlbumRejectsMixedActorsAndDuplicateReceipts(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	leader, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil {
		t.Fatal(e)
	}
	source.ID++
	source.MessageID++
	source.UserID++
	if _, e = s.AcceptDownstream(ctx, 1, source, false, now); e != c.ErrDenied {
		t.Fatal("mixed actor", e)
	}
	source.UserID--
	if _, e = s.AcceptDownstream(ctx, 1, source, false, now); e != nil {
		t.Fatal(e)
	}
	later := now.Add(3 * time.Second)
	request, e := s.DownstreamRequest(ctx, 1, leader.ID, later)
	if e != nil || request == nil {
		t.Fatal(request, e)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: 10, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "sent", MessageID: 100, MessageIDs: []int64{100, 100}, PayloadHeld: true}
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReconcileDownstream(ctx, 1, leader.ID, 1, later); e != c.ErrDenied {
		t.Fatal("duplicate destination accepted", e)
	}
	var count int64
	if e = db.Model(&d.Downstream{}).Where("destination_message_id > 0").Count(&count).Error; e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if e = db.First(&proof, proof.ID).Error; e != nil || !proof.PayloadHeld {
		t.Fatal(proof, e)
	}
	if e = db.Model(&proof).Update("message_ids", "[100,101]").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Table("telegram_bot_instances").Where("id = 1").Update("generation", 2).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileDownstream(ctx, 1, leader.ID, 2, later); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	if state, e := s.ReconcileDownstream(ctx, 1, leader.ID, 2, later); e != nil || state != "delivered" {
		t.Fatal("idempotent ack", state, e)
	}
}

func TestDownstreamFailureResolutionKeepsOriginalRuntimeKey(t *testing.T) {
	s, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	source.MediaGroupID = ""
	row, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil {
		t.Fatal(e)
	}
	request, e := s.DownstreamRequest(ctx, 1, row.ID, now)
	if e != nil {
		t.Fatal(e)
	}
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: 10, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "unknown", PayloadHeld: true}
	if e = db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReconcileDownstream(ctx, 1, row.ID, 1, now); e != nil {
		t.Fatal(e)
	}
	if e = db.First(row, row.ID).Error; e != nil {
		t.Fatal(e)
	}
	version := row.Version
	if _, e = s.ReconcileDownstream(ctx, 1, row.ID, 1, now); e != nil {
		t.Fatal(e)
	}
	if e = db.First(row, row.ID).Error; e != nil || row.Version != version {
		t.Fatal("poll invalidates CAS", row, e)
	}
	if e = db.Model(&proof).Update("status", "failed").Error; e != nil {
		t.Fatal(e)
	}
	in := c.Resolution{ActorID: 8, ExpectedVersion: version, Decision: "failed", DeliveryID: proof.ID, Evidence: "ticket:no-effect"}
	if e = s.ResolveDownstream(ctx, 1, row.ID, 1, in, now); e != nil {
		t.Fatal(e)
	}
	if request, e = s.DownstreamRequest(ctx, 1, row.ID, now); e != c.ErrDenied || request != nil {
		t.Fatal("failed task must require explicit retry", request, e)
	}
	if e = s.ResolveDownstream(ctx, 1, row.ID, 1, in, now); e != c.ErrConflict {
		t.Fatal(e)
	}
}

func TestDownstreamDurableReplyAndNotes(t *testing.T) {
	s, db, original, request, _, now := preparedFixture(t)
	ctx := context.Background()
	proof := rd.Delivery{BotID: 1, Key: request.Key, ChatID: -100, TelegramUserID: 10, TargetType: request.TargetType, Purpose: request.Purpose, Operation: request.Operation, PayloadCipher: request.PayloadCipher, Status: "sent", MessageID: 77, PayloadHeld: true}
	if e := db.Create(&proof).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileReady(ctx, 1, original.ID, 1, now); e != nil {
		t.Fatal(e)
	}
	source := r.Update{ID: 90, Type: "message", UserID: 55, ChatID: -100, MessageID: 88, ThreadID: 20, IsTopicMessage: true, MessageKind: "text", Text: "staff answer", Reply: &r.ReplyReference{ChatID: -100, MessageID: 77, ThreadID: 20}}
	reply, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := s.AcceptDownstream(ctx, 1, source, false, now)
	if e != nil || duplicate.ID != reply.ID {
		t.Fatal(duplicate, e)
	}
	source.Text = "injected"
	if _, e = s.AcceptDownstream(ctx, 1, source, false, now); e != c.ErrDenied {
		t.Fatal(e)
	}
	source.Text = "staff answer"
	down, e := s.DownstreamRequest(ctx, 1, reply.ID, now)
	if e != nil || down == nil {
		t.Fatal(down, e)
	}
	plain, e := s.cipher.Open(down.PayloadCipher)
	if e != nil {
		t.Fatal(e)
	}
	var envelope r.Message
	if e = json.Unmarshal([]byte(plain), &envelope); e != nil {
		t.Fatal(e)
	}
	if envelope.Transfer == nil || !envelope.Transfer.ProtectContent || envelope.Transfer.Contents[0].Text != source.Text {
		t.Fatal(envelope)
	}
	if _, e = s.CheckDownstream(ctx, 1, envelope, now); e != nil {
		t.Fatal(e)
	}
	envelope.Transfer.ChatID = 99
	if _, e = s.CheckDownstream(ctx, 1, envelope, now); e != c.ErrDenied {
		t.Fatal(e)
	}
	envelope.Transfer.ChatID = 10
	source.ID++
	source.MessageID++
	note, e := s.AcceptDownstream(ctx, 1, source, true, now)
	if e != nil || note.State != "internal" {
		t.Fatal(note, e)
	}
	if _, e = s.DownstreamRequest(ctx, 1, note.ID, now); e != c.ErrDenied {
		t.Fatal("note enqueued", e)
	}
	sent := rd.Delivery{BotID: 1, Key: down.Key, ChatID: 10, TelegramUserID: 10, TargetType: down.TargetType, Purpose: down.Purpose, Operation: down.Operation, PayloadCipher: down.PayloadCipher, Status: "sent", MessageID: 100, MessageIDs: []int64{100}, PayloadHeld: true}
	if e = db.Create(&sent).Error; e != nil {
		t.Fatal(e)
	}
	if state, e := s.ReconcileDownstream(ctx, 1, reply.ID, 1, now); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	if e = db.First(&sent, sent.ID).Error; e != nil || sent.PayloadHeld {
		t.Fatal(sent, e)
	}
}

// TestUnquotedAlbumReusesDeliveryAndAuthorization validates text-free media,
// restart reconstruction, immutable source checks and closed-session denial.
func TestUnquotedAlbumReusesDeliveryAndAuthorization(t *testing.T) {
	store, db, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	source.Reply = nil
	leader, err := store.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil {
		t.Fatal(err)
	}
	source.ID++
	source.MessageID++
	source.Reply = &r.ReplyReference{ChatID: source.ChatID, MessageID: source.ThreadID}
	if _, err = store.AcceptDownstream(ctx, 1, source, false, now); err != nil {
		t.Fatal(err)
	}
	restarted := New(db, store.cipher).WithRouteReader(store.routes)
	request, err := restarted.DownstreamRequest(ctx, 1, leader.ID, now.Add(3*time.Second))
	if err != nil || request == nil {
		t.Fatal("album not prepared", err)
	}
	plain, err := store.cipher.Open(request.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	var envelope r.Message
	if err = json.Unmarshal([]byte(plain), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ChatID != 10 || len(envelope.Transfer.Contents) != 2 {
		t.Fatal("album destination/content mismatch")
	}
	if _, err = restarted.CheckDownstream(ctx, 1, envelope, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&d.Session{}).Where("id = ?", leader.SessionID).Update("state", "closed").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.CheckDownstream(ctx, 1, envelope, now.Add(3*time.Second)); err != c.ErrDenied {
		t.Fatal("closed session sent", err)
	}
}

// TestDirectReplyMissingRouteAndCommands checks store-boundary safeguards
// independently of dispatcher filtering, including an explicit internal note.
func TestDirectReplyMissingRouteAndCommands(t *testing.T) {
	store, _, now, source := downstreamAlbumFixture(t)
	ctx := context.Background()
	source.Reply, source.MediaGroupID = nil, ""
	source.Caption = "/note private"
	if _, err := store.AcceptDownstream(ctx, 1, source, false, now); err != c.ErrDenied {
		t.Fatal("caption command escaped", err)
	}
	note, err := store.AcceptDownstream(ctx, 1, source, true, now)
	if err != nil || note.State != "internal" {
		t.Fatal("explicit note rejected", err)
	}
	if request, err := store.DownstreamRequest(ctx, 1, note.ID, now); err != c.ErrDenied || request != nil {
		t.Fatal("note entered queue", err)
	}
	source.ID++
	source.Caption = "photo answer"
	row, err := store.AcceptDownstream(ctx, 1, source, false, now)
	if err != nil || row.ReplyMappingID != 0 {
		t.Fatal(err)
	}
	store.routes = nil // Missing production route tables must fail closed.
	if _, err = store.DownstreamRequest(ctx, 1, row.ID, now); err == nil {
		t.Fatal("missing route bypassed")
	}
	source.ID++
	if _, err = store.AcceptDownstream(ctx, 1, source, false, now); err == nil {
		t.Fatal("missing route accepted reply")
	}
}
