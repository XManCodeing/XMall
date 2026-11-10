package identitymigration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// commitFixture adds actual migration/access tables and explicit administrator verification.
func commitFixture(t *testing.T) (*Store, *gorm.DB, time.Time, c.IdentityMigrationCommit) {
	t.Helper()
	store, db, at, input := fixture(t)
	if err := db.AutoMigrate(&d.IdentityMigration{}, &d.AccessInvite{}, &d.AccessOperation{}); err != nil {
		t.Fatal(err)
	}
	return store, db, at, c.IdentityMigrationCommit{IdentityMigrationInput: input, ActorAdminID: 3, HumanVerified: true, ExpectedBindingVersion: "1", ExpectedRoutes: map[string]string{"1": "11"}, ExpectedGroups: map[string]string{"1": "9"}}
}

// TestCommitRequiresReviewAndExactVersions rejects stale/unreviewed commands without any partial mapping.
func TestCommitRequiresReviewAndExactVersions(t *testing.T) {
	for _, variant := range []string{"human", "actor", "binding", "route", "group", "version"} {
		t.Run(variant, func(t *testing.T) {
			store, db, at, input := commitFixture(t)
			switch variant {
			case "human":
				input.HumanVerified = false
			case "actor":
				input.ActorAdminID = 0
			case "binding":
				input.ExpectedBindingVersion = "2"
			case "route":
				input.ExpectedRoutes["1"] = "2"
			case "group":
				input.ExpectedGroups["1"] = "2"
			case "version":
				input.ExpectedVersions["1"] = "2"
			}
			if err := store.CommitIdentityMigration(context.Background(), input, at); err == nil {
				t.Fatal("invalid commit accepted")
			}
			var count int64
			if err := db.Model(&d.IdentityMigration{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("partial mapping: %d %v", count, err)
			}
		})
	}
}

// TestCommitConcurrentAndProvenance preserves immutable facts and only permits one winning reviewer.
func TestCommitConcurrentAndProvenance(t *testing.T) {
	store, db, at, input := commitFixture(t)
	var before d.LedgerSource
	if err := db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- store.CommitIdentityMigration(ctx, input, at) }()
	}
	workers.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("winners %d", success)
	}
	var after d.LedgerSource
	if err := db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("source rewritten")
	}
	var row d.IdentityMigration
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "awaiting_member" || row.AdmittedAt != nil {
		t.Fatal("review pretended admission")
	}
}

// TestAdmissionEvidenceGate excludes old, wrong-route, approval-only and nonmember evidence.
func TestAdmissionEvidenceGate(t *testing.T) {
	store, db, at, input := commitFixture(t)
	if err := store.CommitIdentityMigration(context.Background(), input, at); err != nil {
		t.Fatal(err)
	}
	base := c.AccessObservation{TargetID: 1, BotID: 5, ChatID: -10001, RouteVersion: 11, TelegramID: 9007199254740995, Status: "member", IsMember: true, ObservedAt: at.Add(time.Second), Source: "query"}
	for _, variant := range []string{"old", "route", "left", "restricted"} {
		event := base
		switch variant {
		case "old":
			event.ObservedAt = at
		case "route":
			event.RouteVersion = 12
		case "left":
			event.Status = "left"
			event.IsMember = false
		case "restricted":
			event.Status = "restricted"
			event.IsMember = false
		}
		if err := Advance(db, event); err != nil {
			t.Fatal(err)
		}
		var row d.IdentityMigration
		if err := db.First(&row).Error; err != nil || row.State != "awaiting_member" {
			t.Fatalf("premature switch %s: %v", variant, err)
		}
	}
	if err := Advance(db, base); err != nil {
		t.Fatal(err)
	}
	var row d.IdentityMigration
	if err := db.First(&row).Error; err != nil || row.State != "cleanup_pending" {
		t.Fatalf("missing switch: %+v %v", row, err)
	}
}
