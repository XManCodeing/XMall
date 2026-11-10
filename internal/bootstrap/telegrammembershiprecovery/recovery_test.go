package telegrammembershiprecovery

import (
	"context"
	"fmt"
	"testing"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/routerecovery"
	bot "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// neverRecover confirms an empty current membership inventory invokes no target action.
type neverRecover struct{ calls int }

// RecoverBotRoute counts unexpected target calls; no network is available in this test.
func (worker *neverRecover) RecoverBotRoute(context.Context, c.BotRouteRecoveryRequest) (c.BotRouteRecoveryResult, error) {
	worker.calls++
	return c.BotRouteRecoveryResult{}, c.ErrInvalid
}

// moduleFixture creates only synthetic tables with the exact dual runtime fences.
func moduleFixture(t *testing.T) (*Modules, *gorm.DB, bot.ModuleRequest) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	models := append(s.Models(), &r.Instance{}, &r.GenerationExit{}, &r.Delivery{}, &g.Target{}, &g.Route{}, &d.Reminder{}, &d.OrderItemSnapshot{}, &d.AccessInvite{}, &d.AccessOperation{}, &d.AccessMember{}, &d.AccessJoin{}, &d.AccessResult{}, &d.IdentityMigration{}, &d.LedgerSource{}, &d.LedgerProjection{}, &d.LedgerRevocation{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{1, 9} {
		if err := db.Create(&r.Instance{ID: id, TelegramID: int64(id), Mode: "internal", Version: 4, RecoveryTaskID: 3, RecoveryVersion: 4}).Error; err != nil {
			t.Fatal(err)
		}
	}
	store, err := s.New(db)
	if err != nil {
		t.Fatal(err)
	}
	request := bot.ModuleRequest{TaskID: 3, ActorID: 7, Snapshot: recovery.Snapshot{Old: recovery.Endpoint{BotID: 1}, New: recovery.Endpoint{BotID: 9}, ExpectedOld: recovery.Versions{Config: 4}, ExpectedNew: recovery.Versions{Config: 4}}}
	return &Modules{Store: store, Recovery: &neverRecover{}}, db, request
}

// TestModuleEmptyProofIsPersistedAndMembershipOnly proves a real absence scan, not a stub success.
func TestModuleEmptyProofIsPersistedAndMembershipOnly(t *testing.T) {
	modules, db, request := moduleFixture(t)
	proofs, err := modules.ContinueBot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 1 || proofs[0].Module != "membership" || !proofs[0].LocalComplete || proofs[0].Receipt == "" {
		t.Fatal(proofs)
	}
	var stored s.ModuleReceipt
	if err := db.First(&stored, 3).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Receipt != proofs[0].Receipt {
		t.Fatal("missing persisted receipt")
	}
	second, err := modules.ContinueBot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Receipt != proofs[0].Receipt {
		t.Fatal("replay proof changed")
	}
	if modules.Recovery.(*neverRecover).calls != 0 {
		t.Fatal("empty scope executed target recovery")
	}
}

// TestResolveBotRouteCompatibility rejects conflicting immutable mapping sources.
func TestResolveBotRouteCompatibility(t *testing.T) {
	for _, scenario := range []string{"legacy_same", "per_route", "legacy_endpoint", "matching", "conflict", "duplicate", "positive", "partial_endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := recovery.Snapshot{BotRoutes: []recovery.BotRoute{{TargetID: 2, ChatID: -10, RouteVersion: 4, Purpose: "membership"}}}
			want := int64(-10)
			invalid := false
			switch scenario {
			case "per_route":
				snapshot.BotRoutes[0].NewChatID = -20
				want = -20
			case "legacy_endpoint", "matching", "conflict":
				snapshot.Old = recovery.Endpoint{TargetID: 2, ChatID: -10}
				snapshot.New = recovery.Endpoint{TargetID: 2, ChatID: -20}
				want = -20
				if scenario == "matching" {
					snapshot.BotRoutes[0].NewChatID = -20
				}
				if scenario == "conflict" {
					snapshot.BotRoutes[0].NewChatID = -30
					invalid = true
				}
			case "duplicate":
				snapshot.BotRoutes = append(snapshot.BotRoutes, snapshot.BotRoutes[0])
				invalid = true
			case "positive":
				snapshot.BotRoutes[0].NewChatID = 20
				invalid = true
			case "partial_endpoint":
				snapshot.Old = recovery.Endpoint{TargetID: 2, ChatID: -10}
				invalid = true
			}
			route, err := ResolveBotRoute(snapshot, 2)
			if invalid {
				if err == nil {
					t.Fatal("forged mapping accepted")
				}
				return
			}
			if err != nil || route.DestinationChat() != want {
				t.Fatalf("destination %d err %v", route.DestinationChat(), err)
			}
			inventory, err := BotInventory(snapshot, c.BotRouteRecoveryRequest{})
			if err != nil || len(inventory.Current) != 1 {
				t.Fatal(err)
			}
		})
	}
}

// TestModuleCannotIgnoreHistoryOrOrphans refuses empty success when a legacy member fact survives.
func TestModuleCannotIgnoreHistoryOrOrphans(t *testing.T) {
	modules, db, request := moduleFixture(t)
	if err := db.Create(&d.AccessInvite{ID: 1, TargetID: 22, BotID: 1, ChatID: -10, State: "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	proofs, err := modules.ContinueBot(context.Background(), request)
	if err == nil || len(proofs) != 0 {
		t.Fatal("orphan history produced empty proof")
	}
}
