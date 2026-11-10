package telegrammembership

import (
	"context"
	"errors"
	"fmt"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	ga "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	gs "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	ms "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type probe struct {
	db    *gorm.DB
	calls atomic.Int32
	hook  func(context.Context) gc.ProbeResult
}

func (p *probe) Instance(context.Context, uint) (gc.Identity, error) {
	return gc.Identity{Running: true, TelegramID: 123, Generation: 1}, nil
}

// Check 在网络边界使用唯一数据库连接，事务仍被占用时必然超时。
func (p *probe) Check(ctx context.Context, _ uint, _ int64) gc.ProbeResult {
	p.calls.Add(1)
	var count int64
	if err := p.db.WithContext(ctx).Model(&gd.Target{}).Count(&count).Error; err != nil {
		return gc.ProbeResult{ErrorClass: "db_connection_held"}
	}
	if p.hook != nil {
		return p.hook(ctx)
	}
	return good()
}
func good() gc.ProbeResult {
	return gc.ProbeResult{Observation: gd.Observation{IsMember: true, ChatType: "supergroup", MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true, Generation: 1, BotTelegramID: 123}}
}

// fixture 构造单连接真实配置及群事务仓储，不添加生产销售绕过选项。
func fixture(t *testing.T, count int) (*Preflight, *probe, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "preflight.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sql.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&gd.Target{}, &gd.Operation{}, &gd.Route{}, &md.ProductConfig{}, &md.ProductTarget{}, &md.SKUTerm{}, &md.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	config := md.ProductConfig{ProductID: 1, Version: 1, Quantity: 1}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= count; index++ {
		target := gd.Target{ID: uint(index), Name: fmt.Sprint(index), OccupancySlot: fmt.Sprint(index), Purpose: "membership", Enabled: true, BotInstanceID: 1, TelegramChatID: -int64(index), RouteVersion: 1, Version: 1, Observation: gd.Observation{State: "unknown"}}
		if err := db.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&md.ProductTarget{ProductID: 1, TargetID: target.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &probe{db: db}
	groups := ga.New(gs.New(db), p)
	store := ms.New(db)
	return New(store, store, groups, func([]uint) error { return nil }), p, db
}
func lines() []c.CheckoutLine { return []c.CheckoutLine{{ProductID: 1, SKUID: 1, Quantity: 1}} }

func TestSingleConnectionFreshAndExpired(t *testing.T) {
	pre, p, _ := fixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proof, err := pre.Checkout(ctx, lines())
	if err != nil || proof.Routes[1] != 1 {
		t.Fatal(proof, err)
	}
	if _, err = pre.Checkout(ctx, lines()); err != nil || p.calls.Load() != 1 {
		t.Fatal(err, p.calls.Load())
	}
	pre.groups.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if _, err = pre.Checkout(ctx, lines()); err != nil || p.calls.Load() != 2 {
		t.Fatal(err, p.calls.Load())
	}
}
func TestOrdinaryAndSalesClosedNeverProbe(t *testing.T) {
	pre, p, _ := fixture(t, 1)
	pre.sale = func([]uint) error { return c.ErrSalesClosed }
	if _, err := pre.Checkout(context.Background(), []c.CheckoutLine{{ProductID: 999}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pre.Checkout(context.Background(), lines()); !errors.Is(err, c.ErrSalesClosed) {
		t.Fatal(err)
	}
	if err := pre.Payment(context.Background(), &od.Order{Items: []od.OrderItem{{ID: 999}}}); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("unexpected probe")
	}
}
func TestRouteChangeAnd429FailClosed(t *testing.T) {
	for _, kind := range []string{"route", "rate_limited", "permission_denied"} {
		t.Run(kind, func(t *testing.T) {
			pre, p, db := fixture(t, 1)
			p.hook = func(ctx context.Context) gc.ProbeResult {
				if kind == "route" {
					if err := db.WithContext(ctx).Model(&gd.Target{}).Where("id = ?", 1).Update("route_version", 2).Error; err != nil {
						t.Error(err)
					}
					return good()
				}
				return gc.ProbeResult{ErrorClass: kind, RetryAfter: 60}
			}
			if _, err := pre.Checkout(context.Background(), lines()); err == nil {
				t.Fatal("failure accepted")
			}
			if kind == "rate_limited" {
				if _, err := pre.Checkout(context.Background(), lines()); err == nil {
					t.Fatal("cooldown accepted")
				}
				if p.calls.Load() != 1 {
					t.Fatal("cooldown bypass")
				}
			}
		})
	}
}
func TestConcurrentCheckAndCancellation(t *testing.T) {
	pre, p, _ := fixture(t, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	p.hook = func(ctx context.Context) gc.ProbeResult {
		close(entered)
		select {
		case <-release:
			return good()
		case <-ctx.Done():
			return gc.ProbeResult{ErrorClass: "canceled"}
		}
	}
	done := make(chan error, 1)
	go func() { _, err := pre.Checkout(context.Background(), lines()); done <- err }()
	<-entered
	if _, err := pre.Checkout(context.Background(), lines()); err == nil {
		t.Fatal("concurrent operation accepted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("duplicate network call")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pre.Checkout(ctx, lines()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestBudgetIsRetryNotBusinessLimit(t *testing.T) {
	pre, p, _ := fixture(t, 21)
	for attempt := 0; attempt < 4; attempt++ {
		_, err := pre.Checkout(context.Background(), lines())
		if err == nil {
			if p.calls.Load() != 21 {
				t.Fatal(p.calls.Load())
			}
			return
		}
		if !errors.Is(err, c.ErrProbeRetry) {
			t.Fatal(err)
		}
	}
	t.Fatal("more than twenty targets could not finish across retries")
}
func TestPaymentUsesHistoricalTargets(t *testing.T) {
	pre, p, db := fixture(t, 1)
	snapshot := md.OrderItemSnapshot{OrderItemID: 10, ProductID: 1, SKUID: 1, AccountID: 7, Quantity: 1, Targets: []md.SnapshotTarget{{TargetID: 1, RouteVersion: 1}}}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("product_id = ?", 1).Delete(&md.ProductConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	order := &od.Order{ID: 2, UserID: 7, Items: []od.OrderItem{{ID: 10, ProductID: 1, SKUID: 1, Quantity: 1, OrderID: 2}}}
	if err := pre.Payment(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal(p.calls.Load())
	}
	if err := db.Model(&gd.Target{}).Where("id = ?", 1).Update("route_version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := pre.Payment(context.Background(), order); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("rerouted historical target probed")
	}
}

func TestOverloadRejectsWithoutWaiting(t *testing.T) {
	pre, p, _ := fixture(t, 1)
	pre.slots <- struct{}{}
	pre.slots <- struct{}{}
	defer func() { <-pre.slots; <-pre.slots }()
	if _, err := pre.Checkout(context.Background(), lines()); !errors.Is(err, c.ErrProbeRetry) {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("overload probed")
	}
}

func TestCancelInFlightLeavesNoReadyObservation(t *testing.T) {
	pre, p, _ := fixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.hook = func(context.Context) gc.ProbeResult { cancel(); return good() }
	if _, err := pre.Checkout(ctx, lines()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	target, err := pre.groups.Store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if ready(*target, pre.groups) {
		t.Fatal("canceled probe authorized checkout")
	}
}

func TestFreshMissingPermissionsDoesNotProbe(t *testing.T) {
	pre, p, db := fixture(t, 1)
	now := time.Now()
	if err := db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"obs_state": "missing_permissions", "obs_route_version": 1, "obs_observed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := pre.Checkout(context.Background(), lines()); !errors.Is(err, c.ErrCapability) {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("fresh denial unnecessarily probed")
	}
}

func TestProofRejectsConfigurationAndRouteChange(t *testing.T) {
	proof := &c.CapabilityProof{Versions: map[uint]uint64{1: 1}, Routes: map[uint]uint64{1: 1}}
	plan := &c.CheckoutPlan{Lines: []c.SnapshotInput{{ProductID: 1, ConfigVersion: 1}}, Targets: map[uint][]md.SnapshotTarget{1: {{TargetID: 1, RouteVersion: 1}}}}
	if err := proof.Validate(plan); err != nil {
		t.Fatal(err)
	}
	plan.Targets[1][0].RouteVersion = 2
	if !errors.Is(proof.Validate(plan), c.ErrConflict) {
		t.Fatal("route changed")
	}
	plan.Targets[1][0].RouteVersion = 1
	plan.Lines[0].ConfigVersion = 2
	if !errors.Is(proof.Validate(plan), c.ErrConflict) {
		t.Fatal("config changed")
	}
}
