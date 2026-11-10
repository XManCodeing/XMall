package routerecovery

import (
	"context"
	"fmt"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// frozenLegacy creates exact old keys and columns, never the new Go model schema.
func frozenLegacy(t *testing.T, db *gorm.DB) {
	t.Helper()
	must(t, db.Migrator().DropTable(Models()...))
	must(t, db.Exec("CREATE TABLE telegram_membership_route_recovery_runs (task_id integer PRIMARY KEY,target_id integer,bot_id integer,old_chat_id integer,new_chat_id integer,old_route_version integer,new_route_version integer,generation integer,version integer,verified boolean,remaining integer,unknown integer,scanned_at datetime)").Error)
	must(t, db.Exec("CREATE TABLE telegram_membership_route_recovery_evidence (task_id integer,kind varchar(24),reference varchar(128),state varchar(32),target_id integer,source_id integer,order_id integer,order_item_id integer,telegram_id integer,entitlement_version integer,term_kind text,expires_at datetime,scanned_at datetime,PRIMARY KEY(task_id,kind,reference))").Error)
}

// TestLegacyMigrationPagesAndNeverRegresses covers frozen schema, partial replay and multi-target keys.
func TestLegacyMigrationPagesAndNeverRegresses(t *testing.T) {
	_, db, _ := fixture(t)
	frozenLegacy(t, db)
	for index := 1; index <= 105; index++ {
		must(t, db.Exec("INSERT INTO telegram_membership_route_recovery_runs (task_id,target_id,bot_id,old_chat_id,new_chat_id,old_route_version,new_route_version,generation,version,verified,remaining,unknown) VALUES (?,2,1,-10,-20,4,5,7,3,1,2,1)", index).Error)
		must(t, db.Exec("INSERT INTO telegram_membership_route_recovery_evidence (task_id,kind,reference,state,target_id,source_id,order_id,order_item_id,telegram_id) VALUES (?,'source',?,'unknown',2,4,5,6,42)", index, fmt.Sprint(index)).Error)
	}
	for index := 0; index < 105; index++ {
		must(t, db.Exec("INSERT INTO telegram_membership_route_recovery_evidence (task_id,kind,reference,state,target_id,source_id,order_id,order_item_id,telegram_id) VALUES (1,'invite',?,'unknown',2,4,5,6,42)", fmt.Sprintf("%03d", index)).Error)
	}
	must(t, MigrateBotRecovery(db))
	var row Run
	must(t, db.First(&row, "task_id = ? AND target_id = ?", 1, 2).Error)
	if row.Verified || row.CompletionReceipt != "" || row.OldBotID != 0 || row.NewBotID != 0 {
		t.Fatal("fabricated dual bot proof")
	}
	must(t, db.Model(&Run{}).Where("task_id = ? AND target_id = ?", 1, 2).Updates(map[string]any{"version": 8, "remaining": 0, "unknown": 0}).Error)
	must(t, db.Model(&Evidence{}).Where("task_id = ?", 1).Update("state", "resolved").Error)
	must(t, db.Create(&Run{TaskID: 1, TargetID: 8, OldBotID: 1, NewBotID: 9, Version: 1}).Error)
	for range 2 {
		must(t, MigrateBotRecovery(db))
	}
	must(t, db.First(&row, "task_id = ? AND target_id = ?", 1, 2).Error)
	if row.Version != 8 || row.Remaining != 0 || row.Unknown != 0 {
		t.Fatal("replay regressed progress")
	}
	var evidence Evidence
	must(t, db.First(&evidence, "task_id = ?", 1).Error)
	if evidence.State != "resolved" {
		t.Fatal("replay overwrote evidence")
	}
	var count int64
	must(t, db.Model(&Run{}).Count(&count).Error)
	if count != 106 {
		t.Fatal(count)
	}
	must(t, db.Model(&Evidence{}).Count(&count).Error)
	if count != 210 {
		t.Fatalf("paged evidence lost: %d", count)
	}
	var original struct{ Verified bool }
	must(t, db.Table(legacyRuns).Where("task_id = ?", 1).Take(&original).Error)
	if !original.Verified {
		t.Fatal("legacy audit mutated")
	}
}

// TestLegacyMigrationRejectsWrongTarget resumes after a copied run but refuses mismatched evidence.
func TestLegacyMigrationRejectsWrongTarget(t *testing.T) {
	_, db, _ := fixture(t)
	frozenLegacy(t, db)
	must(t, db.Exec("INSERT INTO telegram_membership_route_recovery_runs (task_id,target_id,bot_id,version) VALUES (3,2,1,1)").Error)
	must(t, db.Exec("INSERT INTO telegram_membership_route_recovery_evidence (task_id,kind,reference,state,target_id) VALUES (3,'invite','1','unknown',9)").Error)
	if err := MigrateBotRecovery(db); err == nil {
		t.Fatal("cross-target legacy evidence copied")
	}
	must(t, db.Exec("UPDATE telegram_membership_route_recovery_evidence SET target_id=2").Error)
	must(t, MigrateBotRecovery(db))
	must(t, MigrateBotRecovery(db))
}

// inventoryFixture adds all tables required to prove absence rather than skipping missing tables.
func inventoryFixture(t *testing.T) (*BotRecovery, *gorm.DB, c.BotRecoveryInventory) {
	t.Helper()
	worker, db, input, _ := botFixture(t, true)
	must(t, db.AutoMigrate(&d.Reminder{}, &d.OrderItemSnapshot{}))
	return worker, db, c.BotRecoveryInventory{Identity: input, Current: []c.BotRecoveryRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4, Purpose: "membership"}}, History: []c.BotRecoveryRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4}}}
}

// TestInventoryHistoryAndDurableReceipt rejects omitted old routes and replays genuine module proof.
func TestInventoryHistoryAndDurableReceipt(t *testing.T) {
	worker, db, input := inventoryFixture(t)
	must(t, worker.Store.CheckBotInventory(context.Background(), input))
	_, err := worker.RecoverBotRoute(context.Background(), input.Identity)
	must(t, err)
	result, err := worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if !result.Complete || result.CompletionReceipt == "" {
		t.Fatal(result)
	}
	second, err := worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if second.CompletionReceipt != result.CompletionReceipt {
		t.Fatal("receipt changed on replay")
	}
	var stored ModuleReceipt
	must(t, db.First(&stored, input.Identity.TaskID).Error)
	if stored.Receipt != result.CompletionReceipt {
		t.Fatal("receipt not persisted")
	}
	input.History = nil
	if err := worker.Store.CheckBotInventory(context.Background(), input); err == nil {
		t.Fatal("omitted history accepted")
	}
}

// TestInventoryNoCurrentMembersStillScansHistory refuses an unknown invite left on a prior target.
func TestInventoryNoCurrentMembersStillScansHistory(t *testing.T) {
	worker, db, input := inventoryFixture(t)
	must(t, db.Exec("UPDATE telegram_group_targets SET bot_instance_id=22, recovery_task_id=0").Error)
	input.Current = nil
	must(t, db.Create(&d.AccessInvite{ID: 1, TargetID: 2, BotID: 1, ChatID: -10, RouteVersion: 4, State: "unknown"}).Error)
	result, err := worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if result.Complete || result.Unknown == 0 || result.CompletionReceipt != "" {
		t.Fatal("ignored historical invitation")
	}
}

// TestHistoricalEmptyExposureCanResume proves no unconditional historical sentinel remains.
func TestHistoricalEmptyExposureCanResume(t *testing.T) {
	worker, db, input := inventoryFixture(t)
	must(t, db.Exec("UPDATE telegram_group_targets SET bot_instance_id=22,recovery_task_id=0").Error)
	input.Current = nil
	result, err := worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if !result.Complete || result.CompletionReceipt == "" {
		t.Fatalf("empty historical exposure cannot resume: %+v", result)
	}
	must(t, db.Create(&d.LedgerSource{TargetID: 2, OrderItemID: 4, BeneficiaryTelegramID: 42}).Error)
	result, err = worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if result.Complete || result.Unknown == 0 {
		t.Fatal("unproven historical ledger handoff completed")
	}
}

// TestInventoryTrueEmptyRequiresActualTablesAndScan proves an empty receipt and rejects orphan facts.
func TestInventoryTrueEmptyRequiresActualTablesAndScan(t *testing.T) {
	worker, db, input := inventoryFixture(t)
	must(t, db.Exec("DELETE FROM telegram_group_routes").Error)
	must(t, db.Exec("DELETE FROM telegram_group_targets").Error)
	input.Current, input.History = nil, nil
	result, err := worker.Store.CompleteBotInventory(context.Background(), input)
	must(t, err)
	if !result.Complete || result.CompletionReceipt == "" {
		t.Fatal(result)
	}
	must(t, db.Create(&d.AccessInvite{ID: 1, TargetID: 99, BotID: 1, ChatID: -99, State: "unknown"}).Error)
	result, err = worker.Store.CompleteBotInventory(context.Background(), input)
	if err == nil || result.Complete {
		t.Fatal("orphan invitation passed empty proof")
	}
}
