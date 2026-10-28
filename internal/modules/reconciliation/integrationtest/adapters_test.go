package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	"github.com/XManCodeing/XMall/internal/modules/reconciliation/infrastructure/procurementreader"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"

	reconciliationcontract "github.com/XManCodeing/XMall/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/XManCodeing/XMall/internal/modules/reconciliation/domain"
	"github.com/XManCodeing/XMall/internal/modules/reconciliation/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/modules/reconciliation/infrastructure/upstreamreader"
	siteconnectiondomain "github.com/XManCodeing/XMall/internal/modules/siteconnection/domain"
	"github.com/XManCodeing/XMall/internal/upstream"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type procurementSourceStub struct{ order procurementdomain.Order }

func (s procurementSourceStub) ListByConnectionAndTimeRange(uint, time.Time, time.Time) ([]procurementdomain.Order, error) {
	return []procurementdomain.Order{s.order}, nil
}

func TestProcurementReaderPreservesFrozenOrderFields(t *testing.T) {
	reader := procurementreader.New(procurementSourceStub{order: procurementdomain.Order{
		ID: 2, ConnectionID: 7, ConnectionSnapshot: "frozen", UpstreamOrderExternalID: "opaque-order",
		UpstreamCurrency: "USD", UpstreamAmount: money.FromDecimal(decimal.NewFromInt(10)),
	}})
	orders, err := reader.ListByConnectionAndTimeRange(7, time.Time{}, time.Time{})
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	order := orders[0]
	if order.ID != 2 || order.ConnectionID != 7 || order.ConnectionSnapshot != "frozen" || order.UpstreamOrderExternalID != "opaque-order" || order.UpstreamCurrency != "USD" || !order.UpstreamAmount.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("lost frozen fields: %+v", order)
	}
}

type adapterStub struct {
	upstream.Adapter
	detail     *upstream.UpstreamOrderDetail
	err        error
	externalID string
}

func (s *adapterStub) GetOrder(context.Context, uint) (*upstream.UpstreamOrderDetail, error) {
	return s.detail, s.err
}
func (s *adapterStub) GetOrderExternal(_ context.Context, id string) (*upstream.UpstreamOrderDetail, error) {
	s.externalID = id
	return s.detail, s.err
}
func (s *adapterStub) CancelOrderExternal(context.Context, string) error { return nil }

type connectionProviderStub struct {
	connection *siteconnectiondomain.Connection
	adapter    upstream.Adapter
}

func (s connectionProviderStub) GetByID(uint) (*siteconnectiondomain.Connection, error) {
	return s.connection, nil
}
func (s connectionProviderStub) GetAdapter(*siteconnectiondomain.Connection) (upstream.Adapter, error) {
	return s.adapter, nil
}

func TestSessionRejectsEmptyDetailsAndMapsCurrency(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			detail *upstream.UpstreamOrderDetail
			err    error
		}{
			{"nil detail", nil, nil},
			{"query error", nil, errors.New("query failed")},
			{"valid detail", &upstream.UpstreamOrderDetail{Status: "completed", Amount: "0", Currency: "USD"}, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				adapter := &adapterStub{detail: tc.detail, err: tc.err}
				reader, err := upstreamreader.New(connectionProviderStub{connection: &siteconnectiondomain.Connection{}, adapter: adapter}).Open(1)
				if err != nil {
					t.Fatal(err)
				}
				get := reader.Get
				if external {
					get = func(ctx context.Context, _ uint) (*reconciliationcontract.UpstreamOrder, error) {
						return reader.(interface {
							GetExternal(context.Context, string) (*reconciliationcontract.UpstreamOrder, error)
						}).GetExternal(ctx, "opaque-order")
					}
				}
				detail, err := get(context.Background(), 1)
				if (err != nil) != (tc.detail == nil) {
					t.Fatalf("error: %v", err)
				}
				if tc.detail != nil && (detail == nil || detail.Currency != "USD" || detail.Amount != "0" || detail.Status != "completed") {
					t.Fatalf("detail: %+v", detail)
				}
				if external && adapter.externalID != "opaque-order" {
					t.Fatal("external ID lost")
				}
			})
		}
	}
}

func TestOpenRejectsMissingConnectionOrAdapter(t *testing.T) {
	for _, provider := range []connectionProviderStub{{}, {connection: &siteconnectiondomain.Connection{}}} {
		if reader, err := upstreamreader.New(provider).Open(1); err == nil || reader != nil {
			t.Fatalf("reader=%v err=%v", reader, err)
		}
	}
}

func TestItemAutoMigrateAddsDiagnosticsToLegacySchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE reconciliation_items (id integer PRIMARY KEY, job_id integer NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO reconciliation_items (id, job_id) VALUES (1, 7)").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.AutoMigrate(&reconciliationdomain.Item{}); err != nil {
			t.Fatal(err)
		}
	}
	store := gormstore.NewItemStore(db)
	legacy, err := store.GetByID(1)
	if err != nil || legacy.LocalCurrency != "" || legacy.UpstreamCurrency != "" || legacy.Diagnostic != "" {
		t.Fatalf("legacy row: %+v err=%v", legacy, err)
	}
	items := []reconciliationdomain.Item{{JobID: 7, LocalCurrency: "USD", UpstreamCurrency: "CNY", Diagnostic: "currency_mismatch"}}
	if err := store.BatchCreate(items); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetByID(items[0].ID)
	if err != nil || stored.LocalCurrency != "USD" || stored.UpstreamCurrency != "CNY" || stored.Diagnostic != "currency_mismatch" {
		t.Fatalf("stored row: %+v err=%v", stored, err)
	}
}
