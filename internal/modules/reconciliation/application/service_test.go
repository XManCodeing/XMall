package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	reconciliationcontract "github.com/XManCodeing/XMall/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/XManCodeing/XMall/internal/modules/reconciliation/domain"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/shopspring/decimal"
)

type jobRepositoryStub struct {
	job          *reconciliationdomain.Job
	updates      []string
	updateErrors map[string]error
	created      []reconciliationdomain.Item
}

func (s *jobRepositoryStub) Create(job *reconciliationdomain.Job) error {
	job.ID = 99
	s.job = job
	return nil
}
func (s *jobRepositoryStub) GetByID(uint) (*reconciliationdomain.Job, error) { return s.job, nil }
func (s *jobRepositoryStub) Update(job *reconciliationdomain.Job) error {
	s.updates = append(s.updates, job.Status)
	return s.updateErrors[job.Status]
}
func (s *jobRepositoryStub) Claim(_ uint, startedAt time.Time) (bool, error) {
	s.job.Status, s.job.StartedAt = constants.ReconciliationJobStatusRunning, &startedAt
	return true, s.Update(s.job)
}
func (s *jobRepositoryStub) Complete(job *reconciliationdomain.Job, items []reconciliationdomain.Item) error {
	if err := s.Update(job); err != nil {
		return err
	}
	s.created = append(s.created, items...)
	return nil
}
func (s *jobRepositoryStub) Fail(job *reconciliationdomain.Job) error { return s.Update(job) }
func (s *jobRepositoryStub) List(reconciliationcontract.JobListFilter) ([]reconciliationdomain.Job, int64, error) {
	return nil, 0, nil
}

type itemRepositoryStub struct {
	created []reconciliationdomain.Item
}

func (s *itemRepositoryStub) BatchCreate(items []reconciliationdomain.Item) error {
	s.created = append(s.created, items...)
	return nil
}
func (s *itemRepositoryStub) GetByID(uint) (*reconciliationdomain.Item, error) { return nil, nil }
func (s *itemRepositoryStub) Update(*reconciliationdomain.Item) error          { return nil }
func (s *itemRepositoryStub) ListByJobID(uint, int, int) ([]reconciliationdomain.Item, int64, error) {
	return nil, 0, nil
}

type procurementReaderStub struct {
	orders []reconciliationcontract.ProcurementOrder
	err    error
}

func (s procurementReaderStub) ListByConnectionAndTimeRange(uint, time.Time, time.Time) ([]reconciliationcontract.ProcurementOrder, error) {
	return s.orders, s.err
}

type upstreamProviderStub struct {
	openCalls int
	reader    *upstreamReaderStub
	err       error
}

func (s *upstreamProviderStub) Open(uint) (reconciliationcontract.UpstreamOrderReader, error) {
	s.openCalls++
	if s.err != nil || s.reader == nil {
		return nil, s.err
	}
	return s.reader, nil
}

type upstreamReaderStub struct {
	calls     int
	details   map[uint]reconciliationcontract.UpstreamOrder
	err       error
	nilDetail bool
}

func (s *upstreamReaderStub) Get(_ context.Context, orderID uint) (*reconciliationcontract.UpstreamOrder, error) {
	s.calls++
	if s.err != nil || s.nilDetail {
		return nil, s.err
	}
	detail := s.details[orderID]
	return &detail, nil
}

type enqueuerStub struct{ jobIDs []uint }

func (s *enqueuerStub) Enqueue(jobID uint) error {
	s.jobIDs = append(s.jobIDs, jobID)
	return nil
}

type notifierStub struct{ calls int }

func (s *notifierStub) NotifyMismatch(*reconciliationdomain.Job) error {
	s.calls++
	return nil
}

func TestCreateAndEnqueueUsesCreatedJobIdentity(t *testing.T) {
	jobs := &jobRepositoryStub{}
	queue := &enqueuerStub{}
	svc := NewService(Options{
		Jobs: jobs, Items: &itemRepositoryStub{}, Procurements: procurementReaderStub{},
		Upstream: &upstreamProviderStub{reader: &upstreamReaderStub{}}, Queue: queue,
	})

	job, err := svc.CreateAndEnqueue(reconciliationcontract.RunInput{ConnectionID: 7, Type: constants.ReconciliationTypeFull})
	if err != nil {
		t.Fatalf("create and enqueue: %v", err)
	}
	if job.ID != 99 || len(queue.jobIDs) != 1 || queue.jobIDs[0] != 99 {
		t.Fatalf("expected persisted job id to be queued, job=%+v queued=%v", job, queue.jobIDs)
	}
}

func TestExecuteOpensUpstreamOnceAndPersistsMismatch(t *testing.T) {
	jobs := &jobRepositoryStub{job: &reconciliationdomain.Job{
		ID: 1, ConnectionID: 7, Type: constants.ReconciliationTypeFull,
		Status: constants.ReconciliationJobStatusPending,
	}}
	items := &itemRepositoryStub{}
	upstreamOrders := &upstreamReaderStub{details: map[uint]reconciliationcontract.UpstreamOrder{
		11: {Status: "completed", Amount: "10.00"},
		12: {Status: "failed", Amount: "12.00"},
	}}
	upstreamProvider := &upstreamProviderStub{reader: upstreamOrders}
	notifier := &notifierStub{}
	svc := NewService(Options{
		Jobs:  jobs,
		Items: items,
		Procurements: procurementReaderStub{orders: []reconciliationcontract.ProcurementOrder{
			{ID: 21, UpstreamOrderID: 11, Status: constants.ProcurementStatusCompleted, UpstreamAmount: money.FromDecimal(decimal.NewFromInt(10))},
			{ID: 22, UpstreamOrderID: 12, Status: constants.ProcurementStatusCompleted, UpstreamAmount: money.FromDecimal(decimal.NewFromInt(10))},
		}},
		Upstream: upstreamProvider, Notifications: notifier,
	})

	if err := svc.Execute(context.Background(), 1); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if upstreamProvider.openCalls != 1 || upstreamOrders.calls != 2 {
		t.Fatalf("expected one upstream session and two reads, open=%d get=%d", upstreamProvider.openCalls, upstreamOrders.calls)
	}
	if len(jobs.created) != 1 || jobs.created[0].ProcurementOrderID != 22 || jobs.created[0].MismatchType != constants.MismatchTypeBoth {
		t.Fatalf("unexpected mismatches: %+v", jobs.created)
	}
	if jobs.job.Status != constants.ReconciliationJobStatusCompleted || jobs.job.TotalCount != 2 || jobs.job.MatchedCount != 1 || jobs.job.MismatchedCount != 1 {
		t.Fatalf("unexpected completed job: %+v", jobs.job)
	}
	if len(jobs.updates) != 2 || jobs.updates[0] != constants.ReconciliationJobStatusRunning || jobs.updates[1] != constants.ReconciliationJobStatusCompleted {
		t.Fatalf("unexpected status transitions: %v", jobs.updates)
	}
	if notifier.calls != 1 {
		t.Fatalf("expected one mismatch notification, got %d", notifier.calls)
	}
}

type snapshotProviderStub struct {
	*upstreamProviderStub
	frozen        reconciliationcontract.UpstreamOrderReader
	snapshotErr   error
	snapshotCalls int
	connectionID  uint
	snapshot      string
}

func (s *snapshotProviderStub) OpenSnapshot(id uint, snapshot string) (reconciliationcontract.UpstreamOrderReader, error) {
	s.snapshotCalls++
	s.connectionID, s.snapshot = id, snapshot
	return s.frozen, s.snapshotErr
}

func TestExecuteSnapshotDoesNotRequireCurrentConnection(t *testing.T) {
	reader := &upstreamReaderStub{details: map[uint]reconciliationcontract.UpstreamOrder{11: {Status: "completed", Amount: "0", Currency: "USD"}}}
	provider := &snapshotProviderStub{upstreamProviderStub: &upstreamProviderStub{err: errors.New("deleted current connection")}, frozen: reader}
	job := &reconciliationdomain.Job{ID: 1, ConnectionID: 7, Type: constants.ReconciliationTypeFull}
	svc := NewService(Options{Jobs: &jobRepositoryStub{job: job}, Items: &itemRepositoryStub{}, Upstream: provider,
		Procurements: procurementReaderStub{orders: []reconciliationcontract.ProcurementOrder{
			{ID: 1, ConnectionID: 9, ConnectionSnapshot: "frozen", UpstreamOrderID: 11, Status: constants.ProcurementStatusCompleted, UpstreamCurrency: "USD"},
		}},
	})
	if err := svc.Execute(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if provider.openCalls != 0 || provider.snapshotCalls != 1 || provider.connectionID != 9 || provider.snapshot != "frozen" || reader.calls != 1 {
		t.Fatalf("snapshot routing failed: %+v reads=%d", provider, reader.calls)
	}
	if job.MatchedCount != 1 || job.MismatchedCount != 0 {
		t.Fatalf("job: %+v", job)
	}
}

func TestExecuteUncertainOrdersAreNotMatched(t *testing.T) {
	secretErr := errors.New("https://user:secret@upstream.invalid?api_key=secret")
	for _, tc := range []struct {
		name       string
		order      reconciliationcontract.ProcurementOrder
		provider   reconciliationcontract.UpstreamOrderProvider
		diagnostic string
	}{
		{"missing reference", reconciliationcontract.ProcurementOrder{Status: "unknown"}, &upstreamProviderStub{}, "missing_upstream_reference"},
		{"nil detail", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1}, &upstreamProviderStub{reader: &upstreamReaderStub{nilDetail: true}}, "upstream_query_failed"},
		{"query error", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1}, &upstreamProviderStub{reader: &upstreamReaderStub{err: secretErr}}, "upstream_query_failed"},
		{"connection error", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1}, &upstreamProviderStub{err: secretErr}, "connection_unavailable"},
		{"nil connection reader", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1}, &upstreamProviderStub{}, "connection_unavailable"},
		{"snapshot unsupported", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1, ConnectionSnapshot: "secret"}, &upstreamProviderStub{}, "snapshot_unavailable"},
		{"snapshot error", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1, ConnectionSnapshot: "secret"}, &snapshotProviderStub{upstreamProviderStub: &upstreamProviderStub{}, snapshotErr: secretErr}, "snapshot_unavailable"},
		{"nil snapshot reader", reconciliationcontract.ProcurementOrder{UpstreamOrderID: 1, ConnectionSnapshot: "secret"}, &snapshotProviderStub{upstreamProviderStub: &upstreamProviderStub{}}, "snapshot_unavailable"},
		{"external unsupported", reconciliationcontract.ProcurementOrder{UpstreamOrderExternalID: "opaque"}, &upstreamProviderStub{reader: &upstreamReaderStub{}}, "external_query_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &reconciliationdomain.Job{ID: 1, Type: constants.ReconciliationTypeFull}
			items := &itemRepositoryStub{}
			jobs := &jobRepositoryStub{job: job}
			svc := NewService(Options{Jobs: jobs, Items: items, Upstream: tc.provider,
				Procurements: procurementReaderStub{orders: []reconciliationcontract.ProcurementOrder{tc.order}}})
			if err := svc.Execute(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			if job.TotalCount != 1 || job.MatchedCount != 0 || job.MismatchedCount != 1 || len(jobs.created) != 1 {
				t.Fatalf("job=%+v items=%+v", job, jobs.created)
			}
			if jobs.created[0].Diagnostic != tc.diagnostic || jobs.created[0].MismatchType != constants.MismatchTypeBoth {
				t.Fatalf("item: %+v", jobs.created[0])
			}
			encoded, _ := json.Marshal(jobs.created)
			if strings.Contains(string(encoded)+job.ResultJSON, "secret") {
				t.Fatal("diagnostics leaked secret")
			}
			var result map[string]int
			if err := json.Unmarshal([]byte(job.ResultJSON), &result); err != nil || result["errors"] != 1 || result["skipped"] != 0 {
				t.Fatalf("result: %s", job.ResultJSON)
			}
		})
	}
}

func TestExecuteSkipsOnlyUnsubmittedTerminalOrPendingOrders(t *testing.T) {
	provider := &upstreamProviderStub{}
	orders := []reconciliationcontract.ProcurementOrder{}
	for _, status := range []string{constants.ProcurementStatusPending, constants.ProcurementStatusRejected, constants.ProcurementStatusCanceled} {
		orders = append(orders, reconciliationcontract.ProcurementOrder{Status: status})
	}
	job := &reconciliationdomain.Job{ID: 1, Type: constants.ReconciliationTypeFull}
	svc := NewService(Options{Jobs: &jobRepositoryStub{job: job}, Items: &itemRepositoryStub{}, Upstream: provider, Procurements: procurementReaderStub{orders: orders}})
	if err := svc.Execute(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if provider.openCalls != 0 || job.TotalCount != 0 || job.MatchedCount != 0 {
		t.Fatalf("job=%+v opens=%d", job, provider.openCalls)
	}
}

func TestCompareOrderAmountsAndCurrencies(t *testing.T) {
	for _, tc := range []struct{ name, local, upstream, localCurrency, upstreamCurrency, diagnostic string }{
		{"equal zero", "0", "0", "USD", "USD", ""},
		{"local zero", "0", "1", "USD", "USD", "amount_mismatch"},
		{"upstream zero", "1", "0", "USD", "USD", "amount_mismatch"},
		{"empty amount", "0", "", "", "", "invalid_upstream_amount"},
		{"malformed amount", "1", "secret", "USD", "USD", "invalid_upstream_amount"},
		{"negative upstream", "1", "-1", "", "", "invalid_upstream_amount"},
		{"negative local", "-1", "1", "", "", "invalid_local_amount"},
		{"currency mismatch", "1", "1", "USD", "CNY", "currency_mismatch"},
		{"currency normalization", "1", "1", " usd ", "USD", ""},
		{"legacy upstream", "1", "1", "USD", "", ""},
		{"legacy local", "1", "1", "", "USD", ""},
		{"legacy differing amounts", "0", "1", "USD", "", "amount_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := &reconciliationcontract.ProcurementOrder{Status: "completed", UpstreamAmount: money.FromDecimal(decimal.RequireFromString(tc.local)), UpstreamCurrency: tc.localCurrency}
			detail := &reconciliationcontract.UpstreamOrder{Status: "completed", Amount: tc.upstream, Currency: tc.upstreamCurrency}
			item := compareOrder(&reconciliationdomain.Job{Type: constants.ReconciliationTypeFull}, order, detail)
			if tc.diagnostic == "" {
				if item != nil {
					t.Fatalf("unexpected mismatch: %+v", item)
				}
				return
			}
			if item == nil || item.Diagnostic != tc.diagnostic || item.MismatchType != constants.MismatchTypeAmount {
				t.Fatalf("item: %+v", item)
			}
			if item.LocalCurrency != tc.localCurrency || item.UpstreamCurrency != tc.upstreamCurrency {
				t.Fatalf("currencies: %+v", item)
			}
			if item := compareOrder(&reconciliationdomain.Job{Type: constants.ReconciliationTypeStatus}, order, detail); item != nil {
				t.Fatalf("status-only compared amounts: %+v", item)
			}
		})
	}
}

func TestExecuteCompletedUpdateFailureSuppressesNotification(t *testing.T) {
	storageErr := errors.New("secret completion storage failure")
	job := &reconciliationdomain.Job{ID: 1, Type: constants.ReconciliationTypeFull}
	jobs := &jobRepositoryStub{job: job, updateErrors: map[string]error{constants.ReconciliationJobStatusCompleted: storageErr}}
	items := &itemRepositoryStub{}
	notifier := &notifierStub{}
	svc := NewService(Options{
		Jobs: jobs, Items: items, Notifications: notifier, Upstream: &upstreamProviderStub{},
		Procurements: procurementReaderStub{orders: []reconciliationcontract.ProcurementOrder{{ID: 2, Status: "unknown"}}},
	})
	err := svc.Execute(context.Background(), 1)
	if !errors.Is(err, storageErr) {
		t.Fatalf("expected completion storage error, got %v", err)
	}
	if len(jobs.created) != 0 || job.Status != constants.ReconciliationJobStatusFailed {
		t.Fatalf("completion must roll back and fail, items=%+v job=%+v", jobs.created, job)
	}
	if notifier.calls != 0 {
		t.Fatalf("notification sent before completion persisted: %d", notifier.calls)
	}
	if len(jobs.updates) != 3 || jobs.updates[0] != constants.ReconciliationJobStatusRunning || jobs.updates[1] != constants.ReconciliationJobStatusCompleted || jobs.updates[2] != constants.ReconciliationJobStatusFailed {
		t.Fatalf("updates: %v", jobs.updates)
	}
	if strings.Contains(job.ResultJSON, "secret") {
		t.Fatalf("result leaked error: %s", job.ResultJSON)
	}
}

func TestExecuteFailedUpdatePreservesBothErrors(t *testing.T) {
	executionErr := errors.New("secret execution failure")
	storageErr := errors.New("secret failed-state storage failure")
	job := &reconciliationdomain.Job{ID: 1, Type: constants.ReconciliationTypeFull}
	jobs := &jobRepositoryStub{job: job, updateErrors: map[string]error{constants.ReconciliationJobStatusFailed: storageErr}}
	notifier := &notifierStub{}
	svc := NewService(Options{
		Jobs: jobs, Items: &itemRepositoryStub{}, Notifications: notifier,
		Upstream: &upstreamProviderStub{}, Procurements: procurementReaderStub{err: executionErr},
	})
	err := svc.Execute(context.Background(), 1)
	if !errors.Is(err, executionErr) || !errors.Is(err, storageErr) {
		t.Fatalf("expected both errors, got %v", err)
	}
	if notifier.calls != 0 {
		t.Fatalf("notification sent on failure: %d", notifier.calls)
	}
	if len(jobs.updates) != 2 || jobs.updates[0] != constants.ReconciliationJobStatusRunning || jobs.updates[1] != constants.ReconciliationJobStatusFailed {
		t.Fatalf("updates: %v", jobs.updates)
	}
	if job.ResultJSON != `{"error":"reconciliation_failed"}` {
		t.Fatalf("unsafe failure result: %s", job.ResultJSON)
	}
}

func TestExecuteFailureResultDoesNotExposeRepositoryError(t *testing.T) {
	job := &reconciliationdomain.Job{ID: 1}
	svc := NewService(Options{Jobs: &jobRepositoryStub{job: job}, Items: &itemRepositoryStub{}, Upstream: &upstreamProviderStub{}, Procurements: procurementReaderStub{err: errors.New("secret SQL value")}})
	if err := svc.Execute(context.Background(), 1); err == nil {
		t.Fatal("expected failure")
	}
	if job.Status != constants.ReconciliationJobStatusFailed || strings.Contains(job.ResultJSON, "secret") {
		t.Fatalf("job: %+v", job)
	}
}
