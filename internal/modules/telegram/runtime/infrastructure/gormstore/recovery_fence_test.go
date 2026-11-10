package gormstore

import (
	"errors"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

// TestRecoveryFencePersistsAcrossStores checks durable admission and exact-owner CAS.
func TestRecoveryFencePersistsAcrossStores(t *testing.T) {
	store, db, ctx, bot := setup(t)
	worker, err := store.BeginGeneration(ctx, bot.ID, bot.Version)
	must(t, err)
	fence := c.RecoveryFence{BotID: bot.ID, TaskID: 91, Version: worker.Version, Generation: worker.Generation}
	remote := New(db)
	must(t, remote.BlockRecovery(ctx, fence))
	must(t, store.BlockRecovery(ctx, fence))
	if _, err := store.BeginGeneration(ctx, bot.ID, bot.Version); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("admitted fenced generation: %v", err)
	}
	if err := remote.Configure(ctx, bot.ID, bot.Version, map[string]interface{}{"enabled": true}); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("enabled fenced bot: %v", err)
	}
	if err := store.CheckRecovery(ctx, bot.ID, worker.Version, worker.Generation); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("old worker allowed: %v", err)
	}
	if _, err := store.ClaimInbox(ctx, bot.ID, "claim", time.Now().Add(time.Minute)); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("inbox claim allowed: %v", err)
	}
	if _, err := store.ClaimDelivery(ctx, bot.ID, "claim", time.Now().Add(time.Minute)); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("delivery claim allowed: %v", err)
	}
	if err := store.Heartbeat(ctx, bot.ID, worker.Generation, "running", "", worker.Version); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("heartbeat allowed: %v", err)
	}
	if err := store.Accept(ctx, bot.ID, worker.Generation, nil, 1); !errors.Is(err, c.ErrStale) {
		t.Fatalf("accept allowed: %v", err)
	}
	stale := fence
	stale.TaskID++
	if err := remote.ReleaseRecovery(ctx, stale); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("wrong task released fence: %v", err)
	}
	if err := remote.RecoveryDrained(ctx, fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("unfinished worker drained: %v", err)
	}
	must(t, store.RecordGenerationExit(ctx, bot.ID, worker.Version, worker.Generation))
	must(t, remote.RecoveryDrained(ctx, fence))
	must(t, remote.ReleaseRecovery(ctx, fence))
	current, err := remote.Get(ctx, bot.ID)
	must(t, err)
	if current.Version != bot.Version || !current.Enabled {
		t.Fatal("fence changed normal configuration")
	}
	_, err = store.BeginGeneration(ctx, bot.ID, bot.Version)
	must(t, err)
}

// TestRecoveryRequiresAllGenerations rejects expiry/legacy state as drain evidence.
func TestRecoveryRequiresAllGenerations(t *testing.T) {
	store, db, ctx, bot := setup(t)
	first, err := store.BeginGeneration(ctx, bot.ID, bot.Version)
	must(t, err)
	second, err := store.BeginGeneration(ctx, bot.ID, bot.Version)
	must(t, err)
	must(t, store.RecordGenerationExit(ctx, bot.ID, second.Version, second.Generation))
	fence := c.RecoveryFence{BotID: bot.ID, TaskID: 92, Version: bot.Version, Generation: second.Generation}
	must(t, store.BlockRecovery(ctx, fence))
	must(t, store.Heartbeat(ctx, bot.ID, second.Generation, "stopped", "", second.Version))
	if err := store.ReleaseRecovery(ctx, fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("old leased worker omitted: %v", err)
	}
	must(t, store.RecordGenerationExit(ctx, bot.ID, first.Version, first.Generation))
	must(t, store.RecoveryDrained(ctx, fence))
	// A missing legacy admission remains blocked even with a stopped heartbeat.
	must(t, db.Delete(&d.GenerationExit{}, "bot_id = ? AND generation = ?", bot.ID, first.Generation).Error)
	if err := store.RecoveryDrained(ctx, fence); !errors.Is(err, c.ErrRecoveryBlocked) {
		t.Fatalf("missing proof accepted: %v", err)
	}
}

// TestRecoveryRejectsStaleSnapshots ensures a task cannot fence a replacement.
func TestRecoveryRejectsStaleSnapshots(t *testing.T) {
	store, _, ctx, bot := setup(t)
	fence := c.RecoveryFence{BotID: bot.ID, TaskID: 93, Version: bot.Version}
	_, err := store.BeginGeneration(ctx, bot.ID, bot.Version)
	must(t, err)
	if err := store.BlockRecovery(ctx, fence); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	fence.Generation = 1
	fence.Version++
	if err := store.BlockRecovery(ctx, fence); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
}
