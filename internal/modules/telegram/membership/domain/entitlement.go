package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// SourceState 表示已确认的来源状态，不从退款金额、订单取消或处理顺序推断。
type SourceState string

const (
	// SourceActive 表示来源未被明确撤销；是否已生效/到期另按业务时间计算。
	SourceActive SourceState = "active"
	// SourceRevoked 表示整个来源贡献已被授权流程明确撤销，不代表部分退款扣时。
	SourceRevoked SourceState = "revoked"
)

var (
	// ErrInvalidEntitlement 表示身份、来源状态或业务时间不合法。
	ErrInvalidEntitlement = errors.New("invalid membership entitlement")
	// ErrEntitlementScope 表示输入混入其他受益人或逻辑目标，计算必须失败关闭。
	ErrEntitlementScope = errors.New("membership entitlement scope mismatch")
	// ErrSourceConflict 表示同一业务来源出现不同的不可变事实或状态。
	ErrSourceConflict = errors.New("conflicting membership entitlement source")
)

// EntitlementScope 隔离稳定 Telegram 受益身份和逻辑目标，不使用账号当前绑定或物理群路由。
type EntitlementScope struct {
	BeneficiaryTelegramID int64
	TargetID              uint
}

// SourceKey 是来源业务唯一键；同订单项可以为不同目标分别贡献权益。
type SourceKey struct {
	EntitlementScope
	OrderItemID uint
}

// EntitlementSource 是纯领域值，不是数据库模型或付款证明。
// PaidAt 必须由调用方提供持久化的可信付款成功时刻，不得用消费/重试时间替代。
// Kind/Months 来自不可变购买快照；只有明确整笔撤权才可设置 SourceRevoked。
type EntitlementSource struct {
	Key    SourceKey
	PaidAt time.Time
	Kind   string
	Months int64
	State  SourceState
}

// EntitlementProjection 是 at 时刻的有效资格；无资格时 Kind 为空且 ExpiresAt 为 nil。
// 永久资格 Active=true、Kind=TermPermanent、ExpiresAt=nil，禁止日期哨兵。
type EntitlementProjection struct {
	Scope     EntitlementScope
	Active    bool
	Kind      string
	ExpiresAt *time.Time
}

// EntitlementCalculation 保留去重后的全部来源及撤权墓碑，供调用方审计和后续重算。
// Sources 按 PaidAt、OrderItemID 排序，包含未来、过期和撤销来源，不改写原来源状态。
// Revocations 是已确认的整来源撤权键，按 OrderItemID 排序；即使尚无来源也必须保留。
// LimitedExpiresAt 仅为未撤销且已付款来源的限时累计截止时间，可已过期，不可用于单独授权。
// 永久存在时仍计算 LimitedExpiresAt，有限时间自然流逝，不冻结、不补时。
type EntitlementCalculation struct {
	Effective        EntitlementProjection
	LimitedExpiresAt *time.Time
	Sources          []EntitlementSource
	Revocations      []SourceKey
}

// RecalculateEntitlement 按 scope、完整来源 sources、明确撤权墓碑 revocations 和观察时刻 at 重算。
// 未来 PaidAt 的来源保留但不参与当前累计；PaidAt==at 生效，at==截止时间失效（无宽限期）。
// 墓碑优先于迟到授予且不随重算丢弃；调用方须持久保留并重新传入，本函数不实现存储或事件消费。
// 相同来源完全一致则去重，冲突报错；混合 scope、非法值或累计溢出也报错且不返回部分结果。
// 返回独立结果，不修改输入、不读取时钟、不执行外部副作用或日志输出。
// at 只控制付款生效和自然到期，不用于回查撤销前历史；sources/revocations 必须是调用方确认的当前事实。
// 错误不是“无资格”：上层必须停止授权/移除等动作并记录失败，不能把零值结果作为可执行结论。
func RecalculateEntitlement(scope EntitlementScope, sources []EntitlementSource, revocations []SourceKey, at time.Time) (EntitlementCalculation, error) {
	if !validEntitlementScope(scope) || !validEntitlementTime(at) {
		return EntitlementCalculation{}, fmt.Errorf("%w: scope or observation time", ErrInvalidEntitlement)
	}
	ordered, err := normalizeEntitlementSources(scope, sources)
	if err != nil {
		return EntitlementCalculation{}, err
	}
	tombstones, revoked, err := normalizeRevocations(scope, revocations)
	if err != nil {
		return EntitlementCalculation{}, err
	}
	result := EntitlementCalculation{
		Effective:   EntitlementProjection{Scope: scope},
		Sources:     ordered,
		Revocations: tombstones,
	}
	permanent := false
	for _, source := range ordered {
		if source.State == SourceRevoked || revoked[source.Key] || source.PaidAt.After(at) {
			continue
		}
		if source.Kind == TermPermanent {
			permanent = true
			continue
		}
		// 重放固定业务时刻而非 now；撤回旧来源后，后续续费仅保留其自身应有贡献。
		start := source.PaidAt
		if result.LimitedExpiresAt != nil && result.LimitedExpiresAt.After(start) {
			start = *result.LimitedExpiresAt
		}
		result.LimitedExpiresAt, err = Expiry(start, source.Kind, source.Months)
		if err != nil {
			return EntitlementCalculation{}, fmt.Errorf("accumulated membership expiry: %w", err)
		}
	}
	if permanent {
		result.Effective.Active = true
		result.Effective.Kind = TermPermanent
	} else if result.LimitedExpiresAt != nil && at.Before(*result.LimitedExpiresAt) {
		result.Effective.Active = true
		result.Effective.Kind = TermMonths
		expiry := *result.LimitedExpiresAt
		result.Effective.ExpiresAt = &expiry
	}
	return result, nil
}

// normalizeEntitlementSources 校验 scope 下的 sources，返回独立、UTC 归一且稳定排序的唯一来源或错误。
// 同键的状态差异也属于冲突；乱序撤权应使用独立墓碑，不依靠输入先后覆盖来源。
func normalizeEntitlementSources(scope EntitlementScope, sources []EntitlementSource) ([]EntitlementSource, error) {
	unique := make(map[SourceKey]EntitlementSource, len(sources))
	for _, source := range sources {
		if err := validateSourceKey(scope, source.Key); err != nil {
			return nil, err
		}
		if !validEntitlementTime(source.PaidAt) || (source.State != SourceActive && source.State != SourceRevoked) {
			return nil, fmt.Errorf("%w: payment time or source state", ErrInvalidEntitlement)
		}
		// 复用期限规则，永久也必须携带合法付款时间；非法未来/撤销来源不能逃过校验。
		if _, err := Expiry(source.PaidAt.UTC(), source.Kind, source.Months); err != nil {
			return nil, fmt.Errorf("membership source term: %w", err)
		}
		source.PaidAt = source.PaidAt.UTC()
		if previous, exists := unique[source.Key]; exists {
			if !previous.PaidAt.Equal(source.PaidAt) || previous.Kind != source.Kind || previous.Months != source.Months || previous.State != source.State {
				return nil, ErrSourceConflict
			}
			continue
		}
		unique[source.Key] = source
	}
	ordered := make([]EntitlementSource, 0, len(unique))
	for _, source := range unique {
		ordered = append(ordered, source)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].PaidAt.Equal(ordered[right].PaidAt) {
			return ordered[left].Key.OrderItemID < ordered[right].Key.OrderItemID
		}
		return ordered[left].PaidAt.Before(ordered[right].PaidAt)
	})
	return ordered, nil
}

// normalizeRevocations 校验 scope 下的撤权 keys，返回排序去重的墓碑、查询集合及错误。
// 墓碑无需虚构 PaidAt 或套餐快照，允许明确撤权先于授予到达；不含退款金额推断逻辑。
func normalizeRevocations(scope EntitlementScope, keys []SourceKey) ([]SourceKey, map[SourceKey]bool, error) {
	revoked := make(map[SourceKey]bool, len(keys))
	for _, key := range keys {
		if err := validateSourceKey(scope, key); err != nil {
			return nil, nil, err
		}
		revoked[key] = true
	}
	ordered := make([]SourceKey, 0, len(revoked))
	for key := range revoked {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].OrderItemID < ordered[right].OrderItemID
	})
	return ordered, revoked, nil
}

// validateSourceKey 验证 key 的业务身份及其与 scope 的一致性，合法返回 nil。
func validateSourceKey(scope EntitlementScope, key SourceKey) error {
	if !validEntitlementScope(key.EntitlementScope) || key.OrderItemID == 0 {
		return fmt.Errorf("%w: source key", ErrInvalidEntitlement)
	}
	if key.EntitlementScope != scope {
		return ErrEntitlementScope
	}
	return nil
}

// validEntitlementScope 返回 scope 是否具有正数 Telegram 用户 ID 和非零逻辑目标 ID。
func validEntitlementScope(scope EntitlementScope) bool {
	return scope.BeneficiaryTelegramID > 0 && scope.TargetID > 0
}

// validEntitlementTime 返回 instant 的 UTC 时间是否非零且可表示为 1..9999 年，避免时区跨界溢出。
func validEntitlementTime(instant time.Time) bool {
	utc := instant.UTC()
	return !utc.IsZero() && utc.Year() >= 1 && utc.Year() <= 9999
}
