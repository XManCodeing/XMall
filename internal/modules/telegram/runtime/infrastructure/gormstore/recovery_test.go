package gormstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// TestDeliveryKeySQLDialects exercises production queries with local-only connections.
// t receives failures when key operators lose dialect quoting or parameter binding.
func TestDeliveryKeySQLDialects(t *testing.T) {
	_, local, ctx, _ := setup(t)
	pool, err := local.DB()
	must(t, err)
	for _, dialect := range []gorm.Dialector{
		mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{Conn: pool}),
	} {
		t.Run(dialect.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
			must(t, err)
			var statements []string
			var values []any
			capture := func(tx *gorm.DB) {
				statements = append(statements, tx.Statement.SQL.String())
				values = append(values, tx.Statement.Vars...)
			}
			must(t, db.Callback().Query().After("gorm:query").Register("test:key_query", capture))
			must(t, db.Callback().Update().After("gorm:update").Register("test:key_update", capture))
			quoted := "`key`"
			if dialect.Name() == "postgres" {
				quoted = `"key"`
			}
			store := New(db)
			cases := []struct {
				name, operator, value string
				run                   func() error
			}{
				{"business", " = ", "support:reply:'quoted", func() error {
					_, err := store.BusinessDelivery(ctx, 1, "support:reply:'quoted")
					return err
				}},
				{"invite", " LIKE ", "membership-invite:8:%", func() error {
					_, err := store.InviteDelivery(ctx, 1, 8)
					return err
				}},
				{"enqueue", " <> ", "membership-invite:8:update:1:0", func() error {
					return guardInviteEnqueue(db, &d.Delivery{BotID: 1, Key: "membership-invite:8:update:1:0", Purpose: "panel"})
				}},
				{"expired_claim", " NOT LIKE ", "membership-invite:%", func() error {
					_, err := store.ClaimDelivery(ctx, 1, "claim", time.Now().Add(time.Minute))
					if errors.Is(err, c.ErrStale) { // DryRun never updates a real delivery row.
						return nil
					}
					return err
				}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					statements, values = nil, nil
					must(t, test.run())
					sql := strings.Join(statements, "\n")
					if !strings.Contains(sql, quoted+test.operator) || strings.Contains(sql, test.value) {
						t.Fatalf("key must be dialect-quoted and its value bound: %s", sql)
					}
					bound := false
					for _, value := range values {
						if value == test.value {
							bound = true
						}
					}
					if !bound {
						t.Fatalf("missing key parameter %q", test.value)
					}
					if test.name == "enqueue" && !strings.Contains(sql, quoted+" LIKE ") {
						t.Fatalf("enqueue prefix lost dialect quoting: %s", sql)
					}
				})
			}
		})
	}
}

// TestInvitePanelDeliveryEvidence distinguishes enqueue from a confirmed receipt and prevents unknown replay.
func TestInvitePanelDeliveryEvidence(t *testing.T) {
	for _, outcome := range []string{"sent", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			store, db, ctx, bot := setup(t)
			status, err := store.InviteDelivery(ctx, bot.ID, 8)
			must(t, err)
			if status.State != "not_requested" {
				t.Fatal(status)
			}
			row := &d.Delivery{BotID: bot.ID, Key: "membership-invite:8:update:1:0", ChatID: 99, TelegramUserID: 99, Purpose: "panel", Operation: "edit", PayloadCipher: "cipher", PanelEpoch: "epoch", PanelRevision: 1}
			must(t, store.Enqueue(ctx, row))
			status, err = store.InviteDelivery(ctx, bot.ID, 8)
			must(t, err)
			if status.State != "queued" {
				t.Fatal(status)
			}
			duplicate := *row
			duplicate.ID, duplicate.Key = 0, "membership-invite:8:update:2:0"
			if err := store.Enqueue(ctx, &duplicate); !errors.Is(err, c.ErrConflict) {
				t.Fatal("inflight replay", err)
			}
			claim, err := store.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute))
			must(t, err)
			if claim == nil {
				t.Fatal("missing claim")
			}
			id := int64(0)
			if outcome == "sent" {
				id = 123
			}
			must(t, store.FinishPanel(ctx, claim, outcome, c.DeliveryResult{MessageID: id}, "https://t.me/+must-not-log", time.Now()))
			status, err = store.InviteDelivery(ctx, bot.ID, 8)
			must(t, err)
			expected := outcome
			if outcome == "sent" {
				expected = "delivered"
			}
			if status.State != expected || status.MessageID != id {
				t.Fatal(status)
			}
			var saved d.Delivery
			must(t, db.First(&saved, row.ID).Error)
			if saved.LastError == "https://t.me/+must-not-log" {
				t.Fatal("secret retained")
			}
			err = store.Enqueue(ctx, &duplicate)
			if outcome == "unknown" {
				if !errors.Is(err, c.ErrConflict) {
					t.Fatal("unknown replay", err)
				}
			} else {
				must(t, err)
			}
		})
	}
}

// TestInviteEditLeaseExpiryNeverReplays verifies crash recovery does not treat invite edits as ordinary retryable markup.
func TestInviteEditLeaseExpiryNeverReplays(t *testing.T) {
	store, db, ctx, bot := setup(t)
	must(t, db.Model(bot).Update("generation", 1).Error)
	must(t, store.Enqueue(ctx, &d.Delivery{BotID: bot.ID, Key: "membership-invite:8:update:1:0", ChatID: 99, TelegramUserID: 99, Purpose: "panel", Operation: "edit", TargetMessageID: 88, PayloadCipher: "cipher"}))
	now := time.Now().UTC()
	claim, err := store.ClaimDeliveryExcluding(ctx, bot.ID, 1, now, nil)
	must(t, err)
	if claim == nil {
		t.Fatal("missing claim")
	}
	claim, err = store.ClaimDeliveryExcluding(ctx, bot.ID, 1, now.Add(2*time.Minute), nil)
	must(t, err)
	if claim != nil {
		t.Fatal("uncertain edit replayed")
	}
	status, err := store.InviteDelivery(ctx, bot.ID, 8)
	must(t, err)
	if status.State != "unknown" {
		t.Fatal(status)
	}
}

// TestRetryFailedDeliveryRejectsUnknownAndBindings uses real enqueue, claim and
// receipt operations; mutation cases verify all trusted binding fields are exact.
func TestRetryFailedDeliveryRejectsUnknownAndBindings(t *testing.T) {
	for _, status := range []string{"failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			store, db, ctx, bot := setup(t)
			must(t, db.Model(bot).Updates(map[string]any{"generation": 1, "state": "running", "enabled": true}).Error)
			request := c.DeliveryRequest{BotID: bot.ID, Key: "support:reply:1", ChatID: 99, TelegramUserID: 99, TargetType: "support_private", Purpose: "support_reply", Operation: "send", PayloadCipher: "encrypted"}
			must(t, store.EnqueueRequest(ctx, &request))
			claim, err := store.ClaimDeliveryExcluding(ctx, bot.ID, 1, time.Now().UTC(), nil)
			must(t, err)
			if claim == nil {
				t.Fatal("missing delivery")
			}
			must(t, store.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, status, c.DeliveryReceipt{}, "", time.Now().UTC()))
			input := c.DeliveryRetry{BotID: bot.ID, DeliveryID: claim.ID, Generation: 1, Key: request.Key, ChatID: 99, TelegramUserID: 99, TargetType: request.TargetType, Purpose: request.Purpose, PayloadCipher: request.PayloadCipher, Now: time.Now().UTC()}
			for _, mutate := range []func(*c.DeliveryRetry){
				func(in *c.DeliveryRetry) { in.Generation++ },
				func(in *c.DeliveryRetry) { in.Key += "x" },
				func(in *c.DeliveryRetry) { in.ChatID++ },
				func(in *c.DeliveryRetry) { in.TelegramUserID++ },
				func(in *c.DeliveryRetry) { in.PayloadCipher += "x" },
				func(in *c.DeliveryRetry) { in.Purpose = "support_ready" },
			} {
				forged := input
				mutate(&forged)
				if err = store.RetryFailedDelivery(ctx, forged); err == nil {
					t.Fatal("forged retry accepted")
				}
			}
			err = store.RetryFailedDelivery(ctx, input)
			if status == "unknown" {
				if !errors.Is(err, c.ErrConflict) {
					t.Fatal(err)
				}
			} else {
				must(t, err)
				if err = store.RetryFailedDelivery(ctx, input); !errors.Is(err, c.ErrConflict) {
					t.Fatal("duplicate retry", err)
				}
			}
			var persisted d.Delivery
			must(t, db.First(&persisted, claim.ID).Error)
			if persisted.Attempts != 1 || persisted.Key != request.Key || persisted.PayloadCipher != request.PayloadCipher {
				t.Fatal("retry mutated immutable attempt")
			}
		})
	}
}

// TestExpiredSupportClaimAcceptsOnlyOriginalRuntimeReceipt exercises sweep ->
// unknown -> late receipt without SQL state fabrication or accepting admin IDs.
func TestExpiredSupportClaimAcceptsOnlyOriginalRuntimeReceipt(t *testing.T) {
	for _, outcome := range []string{"sent", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			store, db, ctx, bot := setup(t)
			must(t, db.Model(bot).Update("generation", 1).Error)
			request := c.DeliveryRequest{BotID: bot.ID, Key: "support:reply:late", ChatID: 99, TargetType: "support_private", Purpose: "support_reply", Operation: "send", PayloadCipher: "cipher"}
			must(t, store.EnqueueRequest(ctx, &request))
			now := time.Now().UTC()
			claim, err := store.ClaimDeliveryExcluding(ctx, bot.ID, 1, now, nil)
			must(t, err)
			if claim == nil {
				t.Fatal("missing claim")
			}
			next, err := store.ClaimDeliveryExcluding(ctx, bot.ID, 1, now.Add(2*time.Minute), nil)
			must(t, err)
			if next != nil {
				t.Fatal("unknown resent")
			}
			receipt := c.DeliveryReceipt{}
			if outcome == "sent" {
				receipt.MessageIDs = []int64{99}
			}
			if err = store.FinishDeliveryReceipt(ctx, claim.ID, "forged", outcome, receipt, "", now); !errors.Is(err, c.ErrStale) {
				t.Fatal(err)
			}
			must(t, store.FinishDeliveryReceipt(ctx, claim.ID, claim.Claim, outcome, receipt, "", now))
			var row d.Delivery
			must(t, db.First(&row, claim.ID).Error)
			if row.Status != outcome {
				t.Fatal(row.Status)
			}
		})
	}
}

// TestRetryFailedDeliveryRollsBackWithOwnerAudit proves a transaction-bound runtime
// port shares rollback with its owner. ctx is local; no external sends occur.
func TestRetryFailedDeliveryRollsBackWithOwnerAudit(t *testing.T) {
	store, db, ctx, bot := setup(t)
	must(t, db.Model(bot).Updates(map[string]any{"generation": 1, "state": "running", "enabled": true}).Error)
	request := c.DeliveryRequest{BotID: bot.ID, Key: "support:reply:1", ChatID: 99, TelegramUserID: 99, TargetType: "support_private", Purpose: "support_reply", Operation: "send", PayloadCipher: "encrypted"}
	must(t, store.EnqueueRequest(ctx, &request))
	claim, err := store.ClaimDeliveryExcluding(ctx, bot.ID, 1, time.Now().UTC(), nil)
	must(t, err)
	if claim == nil {
		t.Fatal("missing delivery")
	}
	must(t, store.FinishDeliveryReceipt(context.Background(), claim.ID, claim.Claim, "failed", c.DeliveryReceipt{}, "", time.Now().UTC()))
	transaction := db.Begin()
	must(t, transaction.Error)
	input := c.DeliveryRetry{BotID: bot.ID, DeliveryID: claim.ID, Generation: 1, Key: request.Key, ChatID: 99, TelegramUserID: 99, TargetType: request.TargetType, Purpose: request.Purpose, PayloadCipher: request.PayloadCipher, Now: time.Now().UTC()}
	must(t, New(transaction).RetryFailedDelivery(ctx, input))
	must(t, transaction.Rollback().Error)
	var persisted d.Delivery
	must(t, db.First(&persisted, claim.ID).Error)
	if persisted.Status != "failed" {
		t.Fatal("owner rollback lost", persisted.Status)
	}
}
