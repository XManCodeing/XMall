package integrationtest

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	reconciliationapp "github.com/dujiao-next/internal/modules/reconciliation/application"
	reconciliationcontract "github.com/dujiao-next/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/dujiao-next/internal/modules/reconciliation/domain"
	"github.com/dujiao-next/internal/modules/reconciliation/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type executionOrders struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *executionOrders) ListByConnectionAndTimeRange(uint, time.Time, time.Time) ([]reconciliationcontract.ProcurementOrder, error) {
	s.calls.Add(1)
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	return []reconciliationcontract.ProcurementOrder{{ID: 31, Status: "unknown"}}, nil
}

type unusedProvider struct{}

func (unusedProvider) Open(uint) (reconciliationcontract.UpstreamOrderReader, error) {
	return nil, errors.New("unexpected upstream call")
}

func executionDB(t *testing.T) *gorm.DB {
	t.Helper()
	// A temporary file and multiple SQL connections exercise database arbitration,
	// not a single-connection pool acting as an application mutex.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reconciliation-test.db")), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&reconciliationdomain.Job{}, &reconciliationdomain.Item{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func executionService(db *gorm.DB, orders *executionOrders) *reconciliationapp.Service {
	return reconciliationapp.NewService(reconciliationapp.Options{
		Jobs: gormstore.NewJobStore(db), Items: gormstore.NewItemStore(db),
		Procurements: orders, Upstream: unusedProvider{},
	})
}

func createExecutionJob(t *testing.T, db *gorm.DB) reconciliationdomain.Job {
	t.Helper()
	job := reconciliationdomain.Job{ConnectionID: 1, Type: constants.ReconciliationTypeFull}
	if err := gormstore.NewJobStore(db).Create(&job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestExecuteRealStoreRepeatedExecutionIsIdempotent(t *testing.T) {
	db := executionDB(t)
	orders := &executionOrders{}
	svc := executionService(db, orders)
	job := createExecutionJob(t, db)
	for range 3 {
		if err := svc.Execute(context.Background(), job.ID); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := gormstore.NewJobStore(db).GetByID(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := gormstore.NewItemStore(db).ListByJobID(job.ID, 1, 20)
	if err != nil || total != 1 || len(items) != 1 || orders.calls.Load() != 1 || stored.MismatchedCount != 1 || stored.Status != constants.ReconciliationJobStatusCompleted {
		t.Fatalf("stored=%+v items=%+v total=%d reads=%d err=%v", stored, items, total, orders.calls.Load(), err)
	}
}

func TestExecuteRealStoreRollbackAndRetryPreservesResolved(t *testing.T) {
	for _, fault := range []string{"item insert", "completion update"} {
		t.Run(fault, func(t *testing.T) {
			db := executionDB(t)
			job := createExecutionJob(t, db)
			adminID := uint(8)
			resolvedAt := time.Now().Truncate(time.Microsecond)
			resolved := reconciliationdomain.Item{JobID: job.ID, ProcurementOrderID: 32, Resolved: true, ResolvedBy: &adminID, ResolvedAt: &resolvedAt, Remark: "keep audit evidence"}
			unresolved := reconciliationdomain.Item{JobID: job.ID, ProcurementOrderID: 31, Diagnostic: "old_attempt"}
			if err := db.Create(&resolved).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&unresolved).Error; err != nil {
				t.Fatal(err)
			}
			trigger := "CREATE TRIGGER reject_result BEFORE INSERT ON reconciliation_items BEGIN SELECT RAISE(ABORT, 'injected item failure'); END"
			if fault == "completion update" {
				trigger = "CREATE TRIGGER reject_result BEFORE UPDATE ON reconciliation_jobs WHEN NEW.status = 'completed' BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END"
			}
			if err := db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			svc := executionService(db, &executionOrders{})
			if err := svc.Execute(context.Background(), job.ID); err == nil {
				t.Fatal("expected transaction failure")
			}
			stored, err := gormstore.NewJobStore(db).GetByID(job.ID)
			if err != nil || stored.Status != constants.ReconciliationJobStatusFailed || stored.MismatchedCount != 0 {
				t.Fatalf("job=%+v err=%v", stored, err)
			}
			old, err := gormstore.NewItemStore(db).GetByID(unresolved.ID)
			if err != nil || old.Diagnostic != "old_attempt" {
				t.Fatalf("rollback lost old item: %+v %v", old, err)
			}
			if err := db.Exec("DROP TRIGGER reject_result").Error; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := svc.Execute(context.Background(), job.ID); err != nil {
					t.Fatal(err)
				}
			}
			items, total, err := gormstore.NewItemStore(db).ListByJobID(job.ID, 1, 20)
			if err != nil || total != 2 {
				t.Fatalf("items=%+v total=%d err=%v", items, total, err)
			}
			kept, err := gormstore.NewItemStore(db).GetByID(resolved.ID)
			if err != nil || !kept.Resolved || kept.Remark != resolved.Remark || kept.ResolvedBy == nil || *kept.ResolvedBy != adminID || kept.ResolvedAt == nil || !kept.ResolvedAt.Equal(resolvedAt) {
				t.Fatalf("resolved audit lost: %+v %v", kept, err)
			}
			stored, err = gormstore.NewJobStore(db).GetByID(job.ID)
			if err != nil || stored.Status != constants.ReconciliationJobStatusCompleted || stored.MismatchedCount != 1 {
				t.Fatalf("job=%+v err=%v", stored, err)
			}
		})
	}
}

func TestExecuteRealStoreConcurrentWorkersHaveOneOwner(t *testing.T) {
	db := executionDB(t)
	job := createExecutionJob(t, db)
	orders := &executionOrders{entered: make(chan struct{}), release: make(chan struct{})}
	owner := make(chan error, 1)
	go func() { owner <- executionService(db, orders).Execute(context.Background(), job.ID) }()
	<-orders.entered
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- executionService(db, orders).Execute(context.Background(), job.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, reconciliationcontract.ErrJobRunning) {
			t.Errorf("competing worker: %v", err)
		}
	}
	close(orders.release)
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if orders.calls.Load() != 1 {
		t.Fatalf("executions=%d", orders.calls.Load())
	}
	_, count, err := gormstore.NewItemStore(db).ListByJobID(job.ID, 1, 20)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestRealStoreAtomicClaimAndStaleOwner(t *testing.T) {
	db := executionDB(t)
	job := createExecutionJob(t, db)
	startedAt := time.Now().Truncate(time.Microsecond)
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := gormstore.NewJobStore(db).Claim(job.ID, startedAt)
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if claimed {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	store := gormstore.NewJobStore(db)
	job.StartedAt = &startedAt
	job.ResultJSON = `{"error":"reconciliation_failed"}`
	if err := store.Fail(&job); err != nil {
		t.Fatal(err)
	}
	nextAttempt := startedAt.Add(time.Second)
	if claimed, err := store.Claim(job.ID, nextAttempt); !claimed || err != nil {
		t.Fatalf("retry claim=%v err=%v", claimed, err)
	}
	if err := store.Complete(&job, []reconciliationdomain.Item{{ProcurementOrderID: 31}}); !errors.Is(err, reconciliationcontract.ErrJobRunning) {
		t.Fatalf("stale complete: %v", err)
	}
	if err := store.Fail(&job); !errors.Is(err, reconciliationcontract.ErrJobRunning) {
		t.Fatalf("stale failure: %v", err)
	}
	job.StartedAt = &nextAttempt
	if err := store.Complete(&job, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRealStoreResolvedMismatchNotReopenedAndDeletedItemNotResurrected(t *testing.T) {
	db := executionDB(t)
	job := createExecutionJob(t, db)
	item := reconciliationdomain.Item{JobID: job.ID, ProcurementOrderID: 31, Resolved: true, Remark: "already handled"}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := executionService(db, &executionOrders{}).Execute(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	items, total, err := gormstore.NewItemStore(db).ListByJobID(job.ID, 1, 20)
	if err != nil || total != 1 || items[0].ID != item.ID || !items[0].Resolved || items[0].Remark != item.Remark {
		t.Fatalf("items=%+v total=%d err=%v", items, total, err)
	}
	stale := reconciliationdomain.Item{JobID: job.ID, ProcurementOrderID: 33}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&stale).Error; err != nil {
		t.Fatal(err)
	}
	stale.Resolved = true
	if err := gormstore.NewItemStore(db).Update(&stale); !errors.Is(err, reconciliationcontract.ErrItemNotFound) {
		t.Fatalf("stale resolve: %v", err)
	}
}
