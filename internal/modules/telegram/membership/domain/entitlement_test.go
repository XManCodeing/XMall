package domain_test

import (
	"errors"
	"math"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// entitlementFixture 返回独立测试作用域、固定付款时刻和首笔一个月来源，无外部依赖。
func entitlementFixture() (d.EntitlementScope, time.Time, d.EntitlementSource) {
	scope := d.EntitlementScope{BeneficiaryTelegramID: 12345, TargetID: 7}
	paidAt := time.Date(2026, 1, 31, 12, 0, 0, 123, time.UTC)
	return scope, paidAt, d.EntitlementSource{
		Key:    d.SourceKey{EntitlementScope: scope, OrderItemID: 10},
		PaidAt: paidAt, Kind: d.TermMonths, Months: 1, State: d.SourceActive,
	}
}

// nextEntitlementSource 根据 original 创建指定订单项、付款日偏移与期限的副本，返回独立值。
func nextEntitlementSource(original d.EntitlementSource, itemID uint, dayOffset int, kind string, months int64) d.EntitlementSource {
	original.Key.OrderItemID = itemID
	original.PaidAt = original.PaidAt.Add(time.Duration(dayOffset) * 24 * time.Hour)
	original.Kind, original.Months = kind, months
	return original
}

// requireEntitlement 校验实际 result 的资格和预期截止时间；失败通过 t 终止，无返回值。
func requireEntitlement(t *testing.T, result d.EntitlementCalculation, kind string, expiry *time.Time) {
	t.Helper()
	if result.Effective.Active != (kind != "") || result.Effective.Kind != kind {
		t.Fatalf("effective=%+v, want kind=%q", result.Effective, kind)
	}
	if expiry == nil {
		if result.Effective.ExpiresAt != nil {
			t.Fatalf("unexpected effective expiry: %v", result.Effective.ExpiresAt)
		}
	} else if result.Effective.ExpiresAt == nil || !result.Effective.ExpiresAt.Equal(*expiry) {
		t.Fatalf("effective expiry=%v, want %v", result.Effective.ExpiresAt, expiry)
	}
}

// TestEntitlementPaidAtAndRenewal 使用 t 验证首次付款起算、保留余期、断档和无宽限期，无返回值。
func TestEntitlementPaidAtAndRenewal(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	for _, test := range []struct {
		name       string
		sources    []d.EntitlementSource
		atDays     int
		expiryDays int
		active     bool
	}{
		{"first payment not replay time", []d.EntitlementSource{first}, 20, 30, true},
		{"renew before expiry", []d.EntitlementSource{first, nextEntitlementSource(first, 20, 10, d.TermMonths, 2)}, 20, 90, true},
		{"renew after gap", []d.EntitlementSource{first, nextEntitlementSource(first, 20, 40, d.TermMonths, 1)}, 40, 70, true},
		{"exact expiry", []d.EntitlementSource{first}, 30, 30, false},
		{"already expired replay", []d.EntitlementSource{first}, 100, 30, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := d.RecalculateEntitlement(scope, test.sources, nil, paidAt.Add(time.Duration(test.atDays)*24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			expiry := paidAt.Add(time.Duration(test.expiryDays) * 24 * time.Hour)
			if result.LimitedExpiresAt == nil || !result.LimitedExpiresAt.Equal(expiry) {
				t.Fatalf("limited expiry=%v", result.LimitedExpiresAt)
			}
			if test.active {
				requireEntitlement(t, result, d.TermMonths, &expiry)
			} else {
				requireEntitlement(t, result, "", nil)
			}
		})
	}
}

// TestEntitlementPermanentAndRevocation 使用 t 验证永久覆盖、不冻结以及多个永久逐来源撤销，无返回值。
func TestEntitlementPermanentAndRevocation(t *testing.T) {
	scope, paidAt, limited := entitlementFixture()
	permanent := nextEntitlementSource(limited, 20, 5, d.TermPermanent, 0)
	another := nextEntitlementSource(limited, 30, 6, d.TermPermanent, 0)
	for _, test := range []struct {
		name    string
		sources []d.EntitlementSource
		revoked []d.SourceKey
		atDays  int
		kind    string
	}{
		{"upgrade", []d.EntitlementSource{limited, permanent}, nil, 10, d.TermPermanent},
		{"old limited expired", []d.EntitlementSource{limited, permanent}, nil, 40, d.TermPermanent},
		{"revoke permanent remaining limited", []d.EntitlementSource{limited, permanent}, []d.SourceKey{permanent.Key}, 10, d.TermMonths},
		{"revoke permanent expired limited", []d.EntitlementSource{limited, permanent}, []d.SourceKey{permanent.Key}, 40, ""},
		{"one of two permanents revoked", []d.EntitlementSource{limited, permanent, another}, []d.SourceKey{permanent.Key}, 40, d.TermPermanent},
		{"both permanents revoked", []d.EntitlementSource{limited, permanent, another}, []d.SourceKey{permanent.Key, another.Key}, 40, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := d.RecalculateEntitlement(scope, test.sources, test.revoked, paidAt.Add(time.Duration(test.atDays)*24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			expiry := paidAt.Add(30 * 24 * time.Hour)
			if result.LimitedExpiresAt == nil || !result.LimitedExpiresAt.Equal(expiry) {
				t.Fatalf("limited time frozen or lost: %v", result.LimitedExpiresAt)
			}
			if test.kind == d.TermMonths {
				requireEntitlement(t, result, test.kind, &expiry)
			} else {
				requireEntitlement(t, result, test.kind, nil)
			}
			if len(result.Sources) != len(test.sources) {
				t.Fatal("historical sources lost")
			}
		})
	}
}

// TestEntitlementRevokeOldContribution 使用 t 验证续费后撤回旧贡献和显式 revoked 状态，无返回值。
func TestEntitlementRevokeOldContribution(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	renewal := nextEntitlementSource(first, 20, 10, d.TermMonths, 1)
	for _, revokeByState := range []bool{false, true} {
		sources := []d.EntitlementSource{first, renewal}
		revocations := []d.SourceKey{first.Key}
		if revokeByState {
			sources[0].State = d.SourceRevoked
			revocations = nil
		}
		result, err := d.RecalculateEntitlement(scope, sources, revocations, paidAt.Add(35*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		expiry := paidAt.Add(40 * 24 * time.Hour)
		requireEntitlement(t, result, d.TermMonths, &expiry)
		if len(result.Sources) != 2 {
			t.Fatal("revoked contribution history lost")
		}
	}
}

// TestEntitlementFuturePayment 使用 t 验证未来来源保留但不提前延长/授权、付款瞬间生效，无返回值。
func TestEntitlementFuturePayment(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	future := nextEntitlementSource(first, 20, 10, d.TermMonths, 1)
	permanent := nextEntitlementSource(first, 30, 20, d.TermPermanent, 0)
	for _, test := range []struct {
		days, expiryDays int
		kind             string
	}{
		{-1, 0, ""}, {0, 30, d.TermMonths}, {9, 30, d.TermMonths}, {10, 60, d.TermMonths}, {20, 60, d.TermPermanent},
	} {
		result, err := d.RecalculateEntitlement(scope, []d.EntitlementSource{permanent, future, first}, nil, paidAt.Add(time.Duration(test.days)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sources) != 3 {
			t.Fatal("future source lost")
		}
		if test.kind == d.TermMonths {
			expiry := paidAt.Add(time.Duration(test.expiryDays) * 24 * time.Hour)
			requireEntitlement(t, result, test.kind, &expiry)
		} else {
			requireEntitlement(t, result, test.kind, nil)
		}
		if test.days < 0 && result.LimitedExpiresAt != nil {
			t.Fatal("future limited source accumulated early")
		}
	}
}

// TestEntitlementRevocationTombstone 使用 t 验证退款撤权先于授予且墓碑可显式传递重放，无返回值。
func TestEntitlementRevocationTombstone(t *testing.T) {
	scope, paidAt, source := entitlementFixture()
	initial, err := d.RecalculateEntitlement(scope, nil, []d.SourceKey{source.Key, source.Key}, paidAt)
	if err != nil {
		t.Fatal(err)
	}
	requireEntitlement(t, initial, "", nil)
	if len(initial.Revocations) != 1 {
		t.Fatal("missing or duplicate tombstone")
	}
	late, err := d.RecalculateEntitlement(scope, []d.EntitlementSource{source}, initial.Revocations, paidAt)
	if err != nil {
		t.Fatal(err)
	}
	requireEntitlement(t, late, "", nil)
	if len(late.Sources) != 1 || len(late.Revocations) != 1 {
		t.Fatal("audit source or tombstone lost")
	}
	if late.LimitedExpiresAt != nil {
		t.Fatal("revoked grant resurrected")
	}
}

// TestEntitlementReplayDeterministic 使用 t 验证乱序、稳定订单项排序、同源去重与 UTC 等价，无返回值。
func TestEntitlementReplayDeterministic(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	sources := []d.EntitlementSource{first, nextEntitlementSource(first, 9, 0, d.TermMonths, 2), nextEntitlementSource(first, 30, 4, d.TermMonths, 1)}
	duplicate := first
	duplicate.PaidAt = paidAt.In(time.FixedZone("east", 8*3600))
	sources = append(sources, duplicate)
	original := append([]d.EntitlementSource(nil), sources...)
	want, err := d.RecalculateEntitlement(scope, sources, nil, paidAt.Add(5*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, sources) {
		t.Fatal("input mutated")
	}
	if len(want.Sources) != 3 || want.Sources[0].Key.OrderItemID != 9 {
		t.Fatalf("unstable or duplicate sources: %+v", want.Sources)
	}
	expiry := paidAt.Add(120 * 24 * time.Hour)
	requireEntitlement(t, want, d.TermMonths, &expiry)
	random := rand.New(rand.NewSource(42))
	for iteration := 0; iteration < 100; iteration++ {
		random.Shuffle(len(sources), func(left, right int) { sources[left], sources[right] = sources[right], sources[left] })
		got, err := d.RecalculateEntitlement(scope, sources, nil, paidAt.Add(5*24*time.Hour))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("replay %d differs: %+v, %v", iteration, got, err)
		}
	}
}

// TestEntitlementRejectConflicts 使用 t 验证同键不可变事实或状态不同即报错，不静默覆盖，无返回值。
func TestEntitlementRejectConflicts(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	for _, change := range []func(*d.EntitlementSource){
		func(source *d.EntitlementSource) { source.PaidAt = source.PaidAt.Add(time.Second) },
		func(source *d.EntitlementSource) { source.Months = 2 },
		func(source *d.EntitlementSource) { source.Kind = d.TermPermanent; source.Months = 0 },
		func(source *d.EntitlementSource) { source.State = d.SourceRevoked },
	} {
		duplicate := first
		change(&duplicate)
		for _, sources := range [][]d.EntitlementSource{{first, duplicate}, {duplicate, first}} {
			if _, err := d.RecalculateEntitlement(scope, sources, []d.SourceKey{first.Key}, paidAt); !errors.Is(err, d.ErrSourceConflict) {
				t.Fatalf("conflict accepted: %v", err)
			}
		}
	}
}

// TestEntitlementRejectInvalidSources 使用 t 验证身份、状态、日期、期限和时长溢出，无返回值。
func TestEntitlementRejectInvalidSources(t *testing.T) {
	scope, paidAt, first := entitlementFixture()
	for _, test := range []struct {
		name   string
		change func(*d.EntitlementSource)
		want   error
	}{
		{"zero order item", func(source *d.EntitlementSource) { source.Key.OrderItemID = 0 }, d.ErrInvalidEntitlement},
		{"zero user", func(source *d.EntitlementSource) { source.Key.BeneficiaryTelegramID = 0 }, d.ErrInvalidEntitlement},
		{"negative user", func(source *d.EntitlementSource) { source.Key.BeneficiaryTelegramID = -1 }, d.ErrInvalidEntitlement},
		{"zero target", func(source *d.EntitlementSource) { source.Key.TargetID = 0 }, d.ErrInvalidEntitlement},
		{"other user", func(source *d.EntitlementSource) { source.Key.BeneficiaryTelegramID++ }, d.ErrEntitlementScope},
		{"other target", func(source *d.EntitlementSource) { source.Key.TargetID++ }, d.ErrEntitlementScope},
		{"missing state", func(source *d.EntitlementSource) { source.State = "" }, d.ErrInvalidEntitlement},
		{"unknown state", func(source *d.EntitlementSource) { source.State = "refunded" }, d.ErrInvalidEntitlement},
		{"zero paid time", func(source *d.EntitlementSource) { source.PaidAt = time.Time{} }, d.ErrInvalidEntitlement},
		{"utc year overflow", func(source *d.EntitlementSource) {
			source.PaidAt = time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -3600))
		}, d.ErrInvalidEntitlement},
		{"utc year underflow", func(source *d.EntitlementSource) {
			source.PaidAt = time.Date(1, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600))
		}, d.ErrInvalidEntitlement},
		{"negative months", func(source *d.EntitlementSource) { source.Months = -1 }, d.ErrInvalidTerm},
		{"duration overflow", func(source *d.EntitlementSource) { source.Months = math.MaxInt64 }, d.ErrInvalidTerm},
		{"permanent sentinel", func(source *d.EntitlementSource) { source.Kind = d.TermPermanent; source.Months = 999 }, d.ErrInvalidTerm},
		{"unknown kind", func(source *d.EntitlementSource) { source.Kind = "years" }, d.ErrInvalidTerm},
		{"expiry overflow", func(source *d.EntitlementSource) { source.PaidAt = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC) }, d.ErrInvalidTerm},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := first
			test.change(&invalid)
			if _, err := d.RecalculateEntitlement(scope, []d.EntitlementSource{invalid}, nil, paidAt); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

// TestEntitlementScopeAndObservation 使用 t 验证观察时刻、空投影及墓碑作用域失败关闭，无返回值。
func TestEntitlementScopeAndObservation(t *testing.T) {
	scope, paidAt, source := entitlementFixture()
	for _, at := range []time.Time{{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := d.RecalculateEntitlement(scope, nil, nil, at); !errors.Is(err, d.ErrInvalidEntitlement) {
			t.Fatalf("invalid at accepted: %v", err)
		}
	}
	if _, err := d.RecalculateEntitlement(d.EntitlementScope{}, nil, nil, paidAt); !errors.Is(err, d.ErrInvalidEntitlement) {
		t.Fatalf("invalid scope accepted: %v", err)
	}
	for _, alterUser := range []bool{true, false} {
		foreign := source.Key
		if alterUser {
			foreign.BeneficiaryTelegramID++
		} else {
			foreign.TargetID++
		}
		if _, err := d.RecalculateEntitlement(scope, nil, []d.SourceKey{foreign}, paidAt); !errors.Is(err, d.ErrEntitlementScope) {
			t.Fatalf("foreign tombstone accepted: %v", err)
		}
	}
	invalidKey := source.Key
	invalidKey.OrderItemID = 0
	if _, err := d.RecalculateEntitlement(scope, nil, []d.SourceKey{invalidKey}, paidAt); !errors.Is(err, d.ErrInvalidEntitlement) {
		t.Fatalf("invalid tombstone accepted: %v", err)
	}
	empty, err := d.RecalculateEntitlement(scope, nil, nil, paidAt)
	if err != nil {
		t.Fatal(err)
	}
	requireEntitlement(t, empty, "", nil)
	if empty.Effective.Scope != scope || empty.LimitedExpiresAt != nil {
		t.Fatalf("invalid empty projection: %+v", empty)
	}
}

// TestEntitlementAccumulationOverflow 使用 t 验证单源合法但累计跨过日期上限时失败，永久不掩盖错误，无返回值。
func TestEntitlementAccumulationOverflow(t *testing.T) {
	scope, _, first := entitlementFixture()
	first.PaidAt = time.Date(9999, 11, 15, 0, 0, 0, 0, time.UTC)
	second := nextEntitlementSource(first, 20, 0, d.TermMonths, 1)
	permanent := nextEntitlementSource(first, 30, 0, d.TermPermanent, 0)
	for _, sources := range [][]d.EntitlementSource{{first, second}, {first, second, permanent}} {
		result, err := d.RecalculateEntitlement(scope, sources, nil, first.PaidAt)
		if !errors.Is(err, d.ErrInvalidTerm) || !reflect.DeepEqual(result, d.EntitlementCalculation{}) {
			t.Fatalf("overflow returned partial qualification: %+v, %v", result, err)
		}
	}
}

// TestEntitlementConcurrentPureCalculation 使用 t 验证并行重算无共享可变状态且输出不别名输入，无返回值。
func TestEntitlementConcurrentPureCalculation(t *testing.T) {
	scope, paidAt, source := entitlementFixture()
	sources := []d.EntitlementSource{source}
	keys := []d.SourceKey{nextEntitlementSource(source, 20, 0, d.TermMonths, 1).Key}
	var workers sync.WaitGroup
	for index := 0; index < 24; index++ {
		workers.Go(func() {
			result, err := d.RecalculateEntitlement(scope, sources, keys, paidAt)
			if err != nil {
				t.Error(err)
				return
			}
			result.Sources[0].Months = 10
			result.Revocations[0].OrderItemID = 100
			*result.Effective.ExpiresAt = paidAt
			if result.LimitedExpiresAt.Equal(paidAt) {
				t.Error("effective expiry aliases audit expiry")
			}
		})
	}
	workers.Wait()
	if sources[0] != source || keys[0].OrderItemID != 20 {
		t.Fatal("output aliases input")
	}
}
