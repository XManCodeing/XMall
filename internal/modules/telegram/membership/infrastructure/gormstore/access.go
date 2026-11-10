package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AccessModels 返回 B6a 待装配模型；不执行迁移或开启销售。
func AccessModels() []any {
	return []any{&d.AccessInvite{}, &d.AccessSlot{}, &d.AccessJoin{}, &d.AccessOperation{}, &d.AccessMember{}, &d.AccessResult{}, &d.InviteCleanupCycle{}, &d.InviteCleanupItem{}}
}

// Eligibility 在 ctx 下按 at 从账本重算身份 scope；不信任可能过期的 Active 投影位。
func (store *Store) Eligibility(ctx context.Context, identity c.AccessIdentity, target uint, at time.Time) (out c.AccessEligibility, err error) {
	err = store.transaction(ctx, func(tx *gorm.DB) error { out, err = accessEligibility(tx, identity, target, at); return err })
	return out, err
}

// accessEligibility 仅使用 tx，account=0 供内部可信成员观测查来源；对外服务总是要求账号。
func accessEligibility(tx *gorm.DB, identity c.AccessIdentity, target uint, at time.Time) (c.AccessEligibility, error) {
	out := c.AccessEligibility{}
	if identity.TelegramID <= 0 || target == 0 || at.IsZero() {
		return out, c.ErrInvalid
	}
	if identity.AccountID != 0 {
		telegramID, binding, err := userAccessBindingSnapshot(tx, identity.AccountID)
		if err != nil {
			return out, err
		}
		if telegramID != identity.TelegramID {
			return out, c.ErrUntrustedIdentity
		}
		out.BindingVersion = binding
	}
	rows, revoked, mapping, err := im.Sources(tx, identity.TelegramID, target)
	if err != nil {
		return out, err
	}
	if mapping != nil && identity.AccountID != 0 && (mapping.AccountID != identity.AccountID || mapping.NewTelegramID != identity.TelegramID) {
		return out, c.ErrUntrustedIdentity
	}
	if mapping != nil && identity.Route != nil && (mapping.BotID != identity.Route.BotID || mapping.ChatID != identity.Route.ChatID || mapping.RouteVersion != identity.Route.RouteVersion) {
		return out, c.ErrConflict
	}
	rootIdentity := identity.TelegramID
	if mapping != nil {
		rootIdentity = mapping.OldTelegramID
	}
	scope := d.EntitlementScope{BeneficiaryTelegramID: identity.TelegramID, TargetID: target}
	sources := make([]d.EntitlementSource, 0, len(rows))
	for _, row := range rows {
		if identity.AccountID == 0 || identity.AccountID == row.AccountID {
			out.SourceID = row.ID
			out.AccountID = row.AccountID
		}
		sources = append(sources, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
	}
	if out.SourceID == 0 {
		return out, c.ErrUntrustedIdentity
	}
	tombstones := make([]d.SourceKey, 0, len(revoked))
	for _, row := range revoked {
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID})
	}
	calculation, err := d.RecalculateEntitlement(scope, sources, tombstones, at)
	if err != nil {
		return out, err
	}
	var projection d.LedgerProjection
	if err := tx.First(&projection, "beneficiary_telegram_id = ? AND target_id = ?", rootIdentity, target).Error; err != nil {
		return out, err
	}
	out.Active, out.Kind, out.ExpiresAt, out.Version = calculation.Effective.Active, calculation.Effective.Kind, calculation.Effective.ExpiresAt, projection.Version
	if mapping != nil && mapping.OldTelegramID == identity.TelegramID && mapping.State != "awaiting_member" {
		out.Active = false
	}
	if identity.Route != nil && identity.AccountID != 0 {
		if identity.Route.TargetID != target {
			return out, c.ErrInvalid
		}
		if err := accessNotKicked(tx, identity.TelegramID, *identity.Route, at); err != nil {
			return out, err
		}
	}
	if identity.Authorization != nil && (out.BindingVersion != identity.Authorization.BindingVersion || out.Version != identity.Authorization.Version) {
		return out, c.ErrConflict
	}
	return out, nil
}

// ListAccess 使用 target 游标稳定分页，只展示当前归属 identity.BotID 的完整路由状态。
// identity 必须是可信账号/Bot 身份；after/limit 控制分页，at 用于权益重算，返回无秘密列表及错误。
func (store *Store) ListAccess(ctx context.Context, identity c.AccessIdentity, after uint, limit int, at time.Time) (out []c.AccessGroup, err error) {
	if identity.AccountID == 0 || identity.TelegramID <= 0 || identity.BotID == 0 || at.IsZero() || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	out = []c.AccessGroup{}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var targets []uint
		mapped := tx.Model(&d.IdentityMigration{}).Select("old_telegram_id").Where("account_id = ? AND new_telegram_id = ? AND target_id = telegram_membership_ledger_sources.target_id", identity.AccountID, identity.TelegramID)
		// 在分页前过滤当前 Bot，避免其他实例的前页记录挤掉后续合法目标。
		currentTargets := tx.Model(&g.Target{}).Select("id").Where("bot_instance_id = ? AND purpose = ?", identity.BotID, "membership")
		if err := tx.Model(&d.LedgerSource{}).Distinct("target_id").Where("account_id = ? AND (beneficiary_telegram_id = ? OR beneficiary_telegram_id IN (?)) AND target_id > ? AND target_id IN (?)", identity.AccountID, identity.TelegramID, mapped, after, currentTargets).Order("target_id").Limit(limit).Pluck("target_id", &targets).Error; err != nil {
			return err
		}
		for _, target := range targets {
			var route g.Target
			if err := tx.First(&route, "id = ? AND bot_instance_id = ? AND purpose = ?", target, identity.BotID, "membership").Error; err != nil {
				return err
			}
			eligibility, err := accessEligibility(tx, identity, target, at)
			if err != nil {
				return err
			}
			item := c.AccessGroup{Title: route.Name, TargetID: target, BotID: route.BotInstanceID, TelegramID: identity.TelegramID, Active: eligibility.Active, Kind: eligibility.Kind, ExpiresAt: eligibility.ExpiresAt, InviteState: "pending"}
			var invite d.AccessInvite
			query := tx.Where("target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", target, identity.TelegramID, route.BotInstanceID, route.TelegramChatID, route.RouteVersion).Order("id DESC").First(&invite)
			if query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound) {
				return query.Error
			}
			if query.Error == nil {
				if err := projectInviteRetirement(tx, &invite); err != nil {
					return err
				}
				item.InviteID, item.BotID, item.InviteState = invite.ID, invite.BotID, invite.State
				if invite.State == "active" && !d.IsInviteUsableAt(invite, at) {
					item.InviteState = "expired"
				}
				var member d.AccessMember
				err := tx.First(&member, "target_id = ? AND chat_id = ? AND telegram_id = ? AND bot_id = ? AND route_version = ?", target, route.TelegramChatID, identity.TelegramID, route.BotInstanceID, route.RouteVersion).Error
				if err == nil {
					item.MemberStatus = member.Status
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			out = append(out, item)
		}
		return nil
	})
	return out, err
}

// EnsureInvite 在串行化权益根下复核资格、保留 unknown 槽位，并原子创建邀请和操作。
func (store *Store) EnsureInvite(ctx context.Context, identity c.AccessIdentity, route c.AccessRoute, at, expires time.Time) (out *d.AccessInvite, err error) {
	if identity.AccountID == 0 || identity.BotID != route.BotID || route.TargetID == 0 || route.ChatID >= 0 || route.Generation == 0 || route.RouteVersion == 0 || (!expires.IsZero() && !expires.After(at)) {
		return nil, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		rootIdentity, err := im.Scope(tx, identity.TelegramID, route.TargetID)
		if err != nil {
			return err
		}
		if _, err := lockProjection(tx, d.EntitlementScope{BeneficiaryTelegramID: rootIdentity, TargetID: route.TargetID}); err != nil {
			return err
		}
		identity.Route = &route
		eligible, err := accessEligibility(tx, identity, route.TargetID, at)
		if err != nil {
			return err
		}
		if !eligible.Active {
			return c.ErrUntrustedIdentity
		}
		if err := accessNotKicked(tx, identity.TelegramID, route, at); err != nil {
			return err
		}
		slot := d.AccessSlot{TelegramID: identity.TelegramID, TargetID: route.TargetID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&slot).Error; err != nil {
			return err
		}
		if err := tx.First(&slot, "telegram_id = ? AND target_id = ?", identity.TelegramID, route.TargetID).Error; err != nil {
			return err
		}
		if slot.InviteID != 0 {
			var old d.AccessInvite
			if err := tx.First(&old, slot.InviteID).Error; err != nil {
				return err
			}
			if err := projectInviteRetirement(tx, &old); err != nil {
				return err
			}
			if old.BindingVersion == 0 || old.BindingVersion != eligible.BindingVersion || old.AccountID != identity.AccountID {
				return c.ErrUntrustedIdentity
			}
			switched, err := releasedEntrySwitch(tx, old, route)
			if err != nil {
				return err
			}
			if old.Retired && old.State != "revoked" && old.State != "expired" && !switched {
				return c.ErrConflict
			}
			if old.State == "unknown" || old.State == "creating" {
				out = &old
				return nil
			}
			same := old.BotID == route.BotID && old.ChatID == route.ChatID && old.RouteVersion == route.RouteVersion
			if same && d.IsInviteUsableAt(old, at) && (old.State == "pending" || old.State == "failed" || old.State == "active") {
				out = &old
				return nil
			}
			if old.State != "revoked" && old.State != "expired" && !switched {
				// Without an exact released entry-switch receipt, a live old link still blocks a new slot.
				if !d.IsInviteExpiredAt(old, at) {
					return c.ErrConflict
				}
				if err := tx.Model(&old).Update("state", "expired").Error; err != nil {
					return err
				}
			}
		}
		if eligible.ExpiresAt != nil && expires.After(*eligible.ExpiresAt) {
			expires = *eligible.ExpiresAt
		}
		term := d.InviteTermLimited
		if expires.IsZero() {
			term = d.InviteTermLongTerm
		}
		row := d.AccessInvite{Term: term, AccountID: identity.AccountID, TelegramID: identity.TelegramID, TargetID: route.TargetID, BotID: route.BotID, ChatID: route.ChatID, RouteVersion: route.RouteVersion, Generation: route.Generation, BindingVersion: eligible.BindingVersion, EntitlementVersion: eligible.Version, SourceID: eligible.SourceID, ExpiryTrusted: true, State: "pending", ExpiresAt: expires, CreatedAt: at, UpdatedAt: at}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		operation := accessCreateOperation(row, at)
		if err := tx.Create(&operation).Error; err != nil {
			return err
		}
		updated := tx.Model(&d.AccessSlot{}).Where("telegram_id = ? AND target_id = ? AND version = ?", identity.TelegramID, route.TargetID, slot.Version).Updates(map[string]any{"invite_id": row.ID, "version": slot.Version + 1})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		out = &row
		return nil
	})
	return out, err
}

// accessCreateOperation 根据已保存 invite 构建唯一创建操作，不携带明文秘密。
func accessCreateOperation(invite d.AccessInvite, at time.Time) d.AccessOperation {
	return d.AccessOperation{BusinessKey: fmt.Sprintf("invite:%d:create", invite.ID), Kind: "create", InviteID: invite.ID, AccountID: invite.AccountID, TelegramID: invite.TelegramID, TargetID: invite.TargetID, BotID: invite.BotID, ChatID: invite.ChatID, RouteVersion: invite.RouteVersion, Generation: invite.Generation, BindingVersion: invite.BindingVersion, EntitlementVersion: invite.EntitlementVersion, State: "pending", Version: 1, NextAttemptAt: at, CreatedAt: at, UpdatedAt: at}
}

// Invite 按 id 返回内部密文记录；调用方必须独立验证授权后才解密。
func (store *Store) Invite(ctx context.Context, id uint) (*d.AccessInvite, error) {
	var row d.AccessInvite
	err := store.db.WithContext(ctx).First(&row, id).Error
	if err == nil {
		err = projectInviteRetirement(store.db.WithContext(ctx), &row)
	}
	return &row, result(err)
}

// InviteByHash 用真实 bot/chat/hash 限定可信入口，不跨物理群匹配链接。
func (store *Store) InviteByHash(ctx context.Context, bot uint, chat int64, hash string) (*d.AccessInvite, error) {
	var row d.AccessInvite
	err := store.db.WithContext(ctx).Where("bot_id = ? AND chat_id = ? AND link_hash = ? AND link_hash <> ''", bot, chat, hash).First(&row).Error
	if err == nil {
		err = projectInviteRetirement(store.db.WithContext(ctx), &row)
	}
	return &row, result(err)
}

// EnqueueJoin 以事件时间/update 序号维护同 bot/chat/user 水位；未知入口也挡住旧任务。
// 先调用 needs_review 落水位，再补决策；事务失败不影响此前独立提交的水位。
func (store *Store) EnqueueJoin(ctx context.Context, input c.AccessJoinRequest, invite *d.AccessInvite, decision string, at time.Time) (out *d.AccessOperation, err error) {
	if input.BotID == 0 || input.UpdateID <= 0 || input.ChatID >= 0 || input.TelegramID <= 0 || input.ReceivedAt.IsZero() || (decision != "approve" && decision != "decline" && decision != "needs_review") {
		return nil, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		digest := sha256.Sum256([]byte(input.InviteLink))
		join := d.AccessJoin{BotID: input.BotID, UpdateID: input.UpdateID, ChatID: input.ChatID, TelegramID: input.TelegramID, LinkHash: hex.EncodeToString(digest[:]), Decision: "needs_review", State: "needs_review", ReceivedAt: input.ReceivedAt.UTC(), UpdatedAt: at}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&join).Error; err != nil {
			return err
		}
		var saved d.AccessJoin
		if err := tx.First(&saved, "bot_id = ? AND update_id = ?", input.BotID, input.UpdateID).Error; err != nil {
			return err
		}
		if saved.ChatID != join.ChatID || saved.TelegramID != join.TelegramID || saved.LinkHash != join.LinkHash || !saved.ReceivedAt.Equal(join.ReceivedAt) {
			return c.ErrConflict
		}
		latest, err := latestAccessJoin(tx, saved.BotID, saved.ChatID, saved.TelegramID)
		if err != nil {
			return err
		}
		// 待执行旧任务原子失效；已抢占任务由调用前水位检查拦截，调用中结果保留 unknown。
		older := tx.Model(&d.AccessJoin{}).Select("id").Where("bot_id = ? AND chat_id = ? AND telegram_id = ? AND (received_at < ? OR (received_at = ? AND update_id < ?))", latest.BotID, latest.ChatID, latest.TelegramID, latest.ReceivedAt, latest.ReceivedAt, latest.UpdateID)
		if err := tx.Model(&d.AccessOperation{}).Where("join_id IN (?) AND state IN ? AND kind <> ?", older, []string{"pending", "retryable"}, "reconcile").Updates(map[string]any{"state": "failed", "error_class": "identity", "version": gorm.Expr("version + 1"), "updated_at": at}).Error; err != nil {
			return err
		}
		if latest.ID != saved.ID {
			return nil
		}
		var existing d.AccessOperation
		err = tx.Where("join_id = ? AND kind <> ?", saved.ID, "reconcile").First(&existing).Error
		if err == nil {
			out = &existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if decision == "needs_review" {
			return nil
		}
		out, err = decideAccessJoin(tx, saved, invite, decision, input.Authorization, at)
		return err
	})
	return out, err
}

// latestAccessJoin 用可信事件时间优先、同秒 update 序号排序；包含所有状态和未知链接。
// 参数为物理申请 scope；返回持久最新申请或数据库错误，不调用外部依赖。
func latestAccessJoin(tx *gorm.DB, bot uint, chat, user int64) (*d.AccessJoin, error) {
	var join d.AccessJoin
	err := tx.Where("bot_id = ? AND chat_id = ? AND telegram_id = ?", bot, chat, user).Order("received_at DESC, update_id DESC").First(&join).Error
	return &join, err
}

// decideAccessJoin 在 tx 内校验 invite 并 CAS 补齐 join 的决策，返回唯一持久任务。
// 不处理外部授权；调用方在事务外复核后调用，基础设施错误保留 needs_review。
func decideAccessJoin(tx *gorm.DB, join d.AccessJoin, invite *d.AccessInvite, decision string, snapshot *c.AccessEligibility, at time.Time) (*d.AccessOperation, error) {
	if invite == nil {
		return nil, c.ErrUntrustedIdentity
	}
	var actual d.AccessInvite
	if err := tx.First(&actual, invite.ID).Error; err != nil {
		return nil, err
	}
	if err := projectInviteRetirement(tx, &actual); err != nil {
		return nil, err
	}
	if actual.BotID != join.BotID || actual.ChatID != join.ChatID || actual.LinkHash != join.LinkHash {
		return nil, c.ErrUntrustedIdentity
	}
	join.EntitlementVersion = actual.EntitlementVersion
	if decision == "approve" {
		eligible, err := accessEligibility(tx, c.AccessIdentity{AccountID: actual.AccountID, TelegramID: join.TelegramID, Authorization: snapshot}, actual.TargetID, at)
		if err != nil {
			return nil, err
		}
		if actual.BindingVersion == 0 || actual.BindingVersion != eligible.BindingVersion || actual.TelegramID != join.TelegramID || actual.State != "active" || !d.IsInviteUsableAt(actual, at) || !eligible.Active {
			return nil, c.ErrUntrustedIdentity
		}
		if err := accessNotKicked(tx, join.TelegramID, c.AccessRoute{TargetID: actual.TargetID, BotID: actual.BotID, ChatID: actual.ChatID, RouteVersion: actual.RouteVersion}, at); err != nil {
			return nil, err
		}
		join.EntitlementVersion = eligible.Version
	}
	updated := tx.Model(&d.AccessJoin{}).Where("id = ? AND decision = ? AND state = ?", join.ID, "needs_review", "needs_review").Updates(map[string]any{"decision": decision, "state": "pending", "invite_id": actual.ID, "entitlement_version": join.EntitlementVersion, "updated_at": at})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, c.ErrConflict
	}
	operation := accessCreateOperation(actual, at)
	operation.Kind, operation.JoinID, operation.TelegramID = decision, join.ID, join.TelegramID
	operation.BusinessKey = fmt.Sprintf("join:%d:%s", join.ID, decision)
	operation.EntitlementVersion = join.EntitlementVersion
	if err := tx.Create(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

// CurrentJoin 检查 operation 的最新申请水位、关联身份及租约 CAS；旧任务返回冲突。
// 参数 at 为可信时钟；返回供决策复核的最新记录，不持有事务跨越平台调用。
func (store *Store) CurrentJoin(ctx context.Context, operation *d.AccessOperation, at time.Time) (out *d.AccessJoin, err error) {
	if operation == nil || operation.JoinID == 0 {
		return nil, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		join, err := latestAccessJoin(tx, operation.BotID, operation.ChatID, operation.TelegramID)
		if err != nil {
			return err
		}
		if join.ID != operation.JoinID || join.InviteID != operation.InviteID || join.Decision != operation.Kind {
			return c.ErrConflict
		}
		var count int64
		if err := accessLeaseQuery(tx, operation, at).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
		out = join
		return nil
	})
	return out, err
}

// ClaimAccess 将失租创建置 unknown，再按到期时间 CAS 抢占；无工作返回 ErrNotFound。
func (store *Store) ClaimAccess(ctx context.Context, bot uint, token string, at time.Time, ttl time.Duration) (*d.AccessOperation, error) {
	return store.claimAccess(ctx, bot, token, at, ttl, false)
}

// ClaimAccessReconcile claims only read-only reconciliation work; bot/token/time bound each lease.
// Returns ErrNotFound when the bounded queue is empty, without replaying unknown effects.
func (store *Store) ClaimAccessReconcile(ctx context.Context, bot uint, token string, at time.Time, ttl time.Duration) (*d.AccessOperation, error) {
	return store.claimAccess(ctx, bot, token, at, ttl, true)
}

// claimAccess separates side effects and reconciliation while sharing lease CAS and recovery.
// reconcile selects the read-only queue; expired reads may retry, expired effects stay unknown.
func (store *Store) claimAccess(ctx context.Context, bot uint, token string, at time.Time, ttl time.Duration, reconcile bool) (out *d.AccessOperation, err error) {
	if bot == 0 || len(token) < 16 || len(token) > 64 || ttl < time.Second || ttl > 5*time.Minute {
		return nil, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var stale []d.AccessOperation
		if err := tx.Where("bot_id = ? AND state = ? AND lease_until <= ? AND kind <> ? AND target_id NOT IN (?)", bot, "creating", at, "lifecycle", tx.Table("telegram_group_targets").Select("id").Where("recovery_task_id <> 0")).Order("id").Limit(100).Find(&stale).Error; err != nil {
			return err
		}
		for _, operation := range stale {
			state := c.AccessUnknown
			if operation.Kind == "reconcile" {
				state = c.AccessRetryable
			}
			updated := tx.Model(&d.AccessOperation{}).Where("id = ? AND state = ? AND version = ? AND lease_until <= ?", operation.ID, "creating", operation.Version, at).Updates(map[string]any{"state": state, "error_class": "lease_lost", "version": operation.Version + 1, "updated_at": at})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				if err := accessRelatedState(tx, operation, "unknown", at); err != nil {
					return err
				}
			}
		}
		if err := enqueueAccessReconciliations(tx, bot, at); err != nil {
			return err
		}
		var operation d.AccessOperation
		queue := tx.Where("bot_id = ? AND state IN ? AND next_attempt_at <= ? AND target_id NOT IN (?)", bot, []string{"pending", "retryable"}, at, tx.Table("telegram_group_targets").Select("id").Where("recovery_task_id <> 0"))
		if reconcile {
			queue = queue.Where("kind = ?", "reconcile")
		} else {
			queue = queue.Where("kind NOT IN ?", []string{"reconcile", "lifecycle"})
		}
		err := queue.Order("next_attempt_at, id").First(&operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		updated := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ? AND state = ?", operation.ID, operation.Version, operation.State).Updates(map[string]any{"state": "creating", "version": operation.Version + 1, "attempt": operation.Attempt + 1, "last_attempt_at": at, "lease_token": token, "lease_until": at.Add(ttl), "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		operation.State, operation.Version, operation.Attempt, operation.LeaseToken, operation.LeaseUntil = "creating", operation.Version+1, operation.Attempt+1, token, at.Add(ttl)
		if err := accessRelatedState(tx, operation, "creating", at); err != nil {
			return err
		}
		out = &operation
		return nil
	})
	if err == nil && out == nil {
		err = c.ErrNotFound
	}
	return out, err
}

// RenewAccess 使用未过期 token/version CAS 续租并记录本次运行代次。
func (store *Store) RenewAccess(ctx context.Context, operation *d.AccessOperation, at time.Time, ttl time.Duration) error {
	if operation == nil || ttl < time.Second || ttl > 5*time.Minute {
		return c.ErrInvalid
	}
	updated := accessLeaseQuery(store.db.WithContext(ctx), operation, at).Updates(map[string]any{"lease_until": at.Add(ttl), "generation": operation.Generation, "entitlement_version": operation.EntitlementVersion, "updated_at": at})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return c.ErrConflict
	}
	operation.LeaseUntil = at.Add(ttl)
	return nil
}

// accessLeaseQuery 为 operation 生成有效租约 CAS 条件，不接受旧 owner。
func accessLeaseQuery(tx *gorm.DB, operation *d.AccessOperation, at time.Time) *gorm.DB {
	return tx.Model(&d.AccessOperation{}).Where("id = ? AND state = ? AND version = ? AND lease_token = ? AND lease_until > ?", operation.ID, "creating", operation.Version, operation.LeaseToken, at)
}

// FinishAccess 保存结果证据，再以租约 CAS 原子完成关联状态；迟到结果不覆盖新 owner。
func (store *Store) FinishAccess(ctx context.Context, operation *d.AccessOperation, outcome c.AccessAPIResult, cipher, hash string, at time.Time) error {
	if operation == nil {
		return c.ErrInvalid
	}
	state := outcome.Outcome
	if state != c.AccessSucceeded && state != c.AccessFailed && state != c.AccessRetryable {
		state = c.AccessUnknown
	}
	if operation.Kind == "create" && state == c.AccessSucceeded && (cipher == "" || len(hash) != 64) {
		return c.ErrInvalid
	}
	receipt := d.AccessResult{OperationID: operation.ID, LeaseVersion: operation.Version, Generation: operation.Generation, Outcome: state, ErrorClass: accessErrorClass(outcome.ErrorClass), LinkCipher: cipher, LinkHash: hash, ReceivedAt: at}
	if err := store.saveAccessEvidence(ctx, operation, &receipt); err != nil {
		return err
	}
	return store.transaction(ctx, func(tx *gorm.DB) error {
		delay := outcome.RetryAfter
		if delay < time.Second {
			delay = time.Minute
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		updated := accessLeaseQuery(tx, operation, at).Updates(map[string]any{"state": state, "error_class": accessErrorClass(outcome.ErrorClass), "next_attempt_at": at.Add(delay), "version": operation.Version + 1, "lease_token": "", "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		if err := accessRelatedState(tx, *operation, state, at); err != nil {
			return err
		}
		if operation.Kind == "revoke" && operation.CleanupCycleID != 0 {
			if err := refreshCleanupCycles(tx, 0, operation.InviteID, at); err != nil {
				return err
			}
		}
		if operation.Kind == "create" && cipher != "" && len(hash) == 64 && (state == c.AccessSucceeded || state == c.AccessUnknown) {
			return tx.Model(&d.AccessInvite{}).Where("id = ?", operation.InviteID).Updates(map[string]any{"link_cipher": cipher, "link_hash": hash}).Error
		}
		return nil
	})
}

// saveAccessEvidence commits immutable response evidence independently of completion CAS.
// operation must match persisted provenance; stale leases may only revoke expiry trust, never restore it.
func (store *Store) saveAccessEvidence(ctx context.Context, operation *d.AccessOperation, receipt *d.AccessResult) error {
	if operation.ID == 0 || operation.Version == 0 || operation.Attempt == 0 || len(operation.LeaseToken) < 16 {
		return c.ErrInvalid
	}
	return store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var current d.AccessOperation
		if err := tx.First(&current, operation.ID).Error; err != nil {
			return err
		}
		if current.Kind != operation.Kind || current.InviteID != operation.InviteID || current.JoinID != operation.JoinID || current.AccountID != operation.AccountID || current.TelegramID != operation.TelegramID || current.TargetID != operation.TargetID || current.BotID != operation.BotID || current.ChatID != operation.ChatID || current.RouteVersion != operation.RouteVersion || current.BindingVersion != operation.BindingVersion || current.BusinessKey != operation.BusinessKey || current.Version < operation.Version || current.Attempt < operation.Attempt {
			return c.ErrConflict
		}
		if current.Version == operation.Version && current.LeaseToken != operation.LeaseToken {
			return c.ErrConflict
		}
		if operation.Kind == "create" && receipt.ErrorClass == "invalid_response" {
			var invite d.AccessInvite
			if err := tx.First(&invite, current.InviteID).Error; err != nil {
				return err
			}
			if invite.AccountID != current.AccountID || invite.TelegramID != current.TelegramID || invite.TargetID != current.TargetID || invite.BotID != current.BotID || invite.ChatID != current.ChatID || invite.RouteVersion != current.RouteVersion || invite.BindingVersion != current.BindingVersion {
				return c.ErrConflict
			}
			// 否定证据与回执一起独立提交，不随后续租约 CAS 冲突回滚。
			if err := tx.Model(&d.AccessInvite{}).Where("id = ?", invite.ID).Update("expiry_trusted", false).Error; err != nil {
				return err
			}
		}
		conflict := clause.OnConflict{DoNothing: true}
		if operation.Kind == "create" && receipt.ErrorClass == "invalid_response" {
			// 同租约已有回执时仅补充单向否定分类，不覆写已有结果或秘密证据。
			conflict = clause.OnConflict{Columns: []clause.Column{{Name: "operation_id"}, {Name: "lease_version"}}, DoUpdates: clause.Assignments(map[string]any{"error_class": "invalid_response"})}
		}
		if err := tx.Clauses(conflict).Create(receipt).Error; err != nil {
			return err
		}
		if operation.Kind == "create" {
			// 已退休邀请的迟到 URL 即使在续费后到达，也必须补入原清理周期。
			var item d.InviteCleanupItem
			err := tx.Where("invite_id = ?", operation.InviteID).Order("cycle_id").First(&item).Error
			if err == nil {
				var cycle d.InviteCleanupCycle
				var invite d.AccessInvite
				if err := tx.First(&cycle, item.CycleID).Error; err != nil {
					return err
				}
				if err := tx.First(&invite, operation.InviteID).Error; err != nil {
					return err
				}
				return enqueueInviteCleanupItem(tx, cycle, invite, receipt.ReceivedAt)
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			_, err = enqueueInviteCleanup(tx, operation.TargetID, operation.TelegramID, receipt.ReceivedAt)
			return err
		}
		return nil
	})
}

// accessErrorClass 限制 err 分类，防止原始平台响应泄漏秘密。
func accessErrorClass(value string) string {
	switch value {
	case "lease_lost", "unavailable", "capability", "identity", "expired", "route_changed", "entitlement_changed", "rate_limit", "permission", "authentication", "network", "invalid_response", "crypto", "not_found":
		return value
	default:
		return "unavailable"
	}
}

// accessRelatedState 映射链路状态；批准不等同观测到成员加入。
func accessRelatedState(tx *gorm.DB, operation d.AccessOperation, state string, at time.Time) error {
	if operation.Kind == "reconcile" {
		return nil // Read results never rewrite the original approval or join state.
	}
	if operation.Kind == "approve" && state == c.AccessUnknown {
		if err := enqueueAccessReconciliation(tx, operation, at); err != nil {
			return err
		}
	}
	if operation.Kind == "create" {
		if state == c.AccessSucceeded {
			state = "active"
		}
		if state == c.AccessRetryable {
			state = "failed"
		}
		return tx.Model(&d.AccessInvite{}).Where("id = ? AND state <> ?", operation.InviteID, "revoked").Updates(map[string]any{"state": state, "updated_at": at}).Error
	}
	if operation.Kind == "revoke" {
		if state == c.AccessSucceeded {
			state = "revoked"
		} else if state != "unknown" {
			return nil
		}
		return tx.Model(&d.AccessInvite{}).Where("id = ?", operation.InviteID).Updates(map[string]any{"state": state, "updated_at": at}).Error
	}
	if operation.JoinID != 0 {
		if state == c.AccessSucceeded {
			if operation.Kind == "approve" {
				state = "approved"
			} else {
				state = "declined"
			}
		}
		return tx.Model(&d.AccessJoin{}).Where("id = ?", operation.JoinID).Updates(map[string]any{"state": state, "updated_at": at, "entitlement_version": operation.EntitlementVersion}).Error
	}
	return nil
}

// EnqueueRevoke 为已授权 identity 的已知邀请 id 创建去重撤销操作，不重置 unknown。
func (store *Store) EnqueueRevoke(ctx context.Context, identity c.AccessIdentity, id uint, at time.Time) (out *d.AccessOperation, err error) {
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var invite d.AccessInvite
		if err := tx.First(&invite, id).Error; err != nil {
			return err
		}
		if identity.AccountID == 0 || invite.AccountID != identity.AccountID || invite.TelegramID != identity.TelegramID || invite.BotID != identity.BotID || invite.LinkCipher == "" || invite.LinkHash == "" || invite.State != "active" {
			return c.ErrUntrustedIdentity
		}
		operation := accessCreateOperation(invite, at)
		operation.Kind, operation.BusinessKey = "revoke", fmt.Sprintf("invite:%d:revoke", invite.ID)
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&operation).Error; err != nil {
			return err
		}
		var saved d.AccessOperation
		if err := tx.First(&saved, "business_key = ?", operation.BusinessKey).Error; err != nil {
			return err
		}
		out = &saved
		return nil
	})
	return out, err
}

// ObserveAccess 仅纳管账本来源身份，时间/序号 CAS 防乱序，restricted 保留 is_member。
func (store *Store) ObserveAccess(ctx context.Context, input c.AccessObservation) (changed bool, err error) {
	if input.TargetID == 0 || input.BotID == 0 || input.ChatID >= 0 || input.TelegramID <= 0 || input.RouteVersion == 0 || input.ObservedAt.IsZero() || input.Sequence < 0 || (input.Source != "chat_member" && input.Source != "query") {
		return false, c.ErrInvalid
	}
	switch input.Status {
	case "member", "administrator", "creator":
		input.IsMember = true
	case "left", "kicked":
		input.IsMember = false
	case "restricted":
	default:
		return false, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		changed, err = observeAccess(tx, input)
		return err
	})
	return changed, err
}

// observeAccess writes validated observations on the supplied transaction; returns whether the watermark advanced.
func observeAccess(tx *gorm.DB, input c.AccessObservation) (bool, error) {
	sources, _, _, err := im.Sources(tx, input.TelegramID, input.TargetID)
	if err != nil {
		return false, err
	}
	if len(sources) == 0 {
		return false, nil
	}
	source := sources[0]
	row := d.AccessMember{TargetID: input.TargetID, ChatID: input.ChatID, TelegramID: input.TelegramID, BotID: input.BotID, RouteVersion: input.RouteVersion, SourceID: source.ID, Status: input.Status, IsMember: input.IsMember, ObservedAt: input.ObservedAt.UTC(), Sequence: input.Sequence, ObservationSource: input.Source, ManagedAt: input.ObservedAt.UTC()}
	created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if created.Error != nil {
		return false, created.Error
	}
	if created.RowsAffected == 1 {
		if err := im.Advance(tx, input); err != nil {
			return false, err
		}
		return true, blockExternalLifecycleBan(tx, input)
	}
	updated := tx.Model(&d.AccessMember{}).Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND (observed_at < ? OR (observed_at = ? AND sequence < ?)) AND route_version <= ?", input.TargetID, input.ChatID, input.TelegramID, input.ObservedAt, input.ObservedAt, input.Sequence, input.RouteVersion).Updates(map[string]any{"bot_id": input.BotID, "route_version": input.RouteVersion, "status": input.Status, "is_member": input.IsMember, "observed_at": input.ObservedAt.UTC(), "sequence": input.Sequence, "observation_source": input.Source})
	if updated.Error != nil {
		return false, updated.Error
	}
	// Historical external-ban events may arrive after a newer query. Preserve their
	// attribution when their second-wide interval overlaps/follows our acknowledgement,
	// even if the member watermark did not move.
	if err := blockExternalLifecycleBan(tx, input); err != nil {
		return false, err
	}
	if updated.RowsAffected == 1 {
		if err := im.Advance(tx, input); err != nil {
			return false, err
		}
	}
	return updated.RowsAffected == 1, nil
}

// blockExternalLifecycleBan preserves newer or same-second ambiguous external bans.
// Telegram date denotes [second, second+1s), not a nanosecond-precise instant.
// It invalidates leased unban work without changing the event/member observation time.
func blockExternalLifecycleBan(tx *gorm.DB, input c.AccessObservation) error {
	if input.Source != "chat_member" || input.Status != "kicked" || input.ActorIsManagedBot {
		return nil
	}
	// Only an interval ending at/before our acknowledgement is provably older.
	// The exclusive upper bound is comparison-only; it is never persisted as an event fact.
	eventEndExclusive := input.ObservedAt.UTC().Truncate(time.Second).Add(time.Second)
	return tx.Model(&d.AccessOperation{}).Where("kind = ? AND bot_id = ? AND target_id = ? AND chat_id = ? AND telegram_id = ? AND state <> ? AND (ban_confirmed_at IS NULL OR ban_confirmed_at < ?)", "lifecycle", input.BotID, input.TargetID, input.ChatID, input.TelegramID, "succeeded", eventEndExclusive).
		Updates(map[string]any{"ban_blocked": true, "state": "needs_review", "review_reason": "disciplinary_ban", "version": gorm.Expr("version + 1"), "updated_at": input.ObservedAt}).Error
}

// accessNotKicked 仅用 tx 核验当前物理路由可信观测；left 不等于封禁。
// 返回数据库错误或明确拒绝，不创建封禁政策、不解除平台封禁。
func accessNotKicked(tx *gorm.DB, telegramID int64, route c.AccessRoute, at time.Time) error {
	var member d.AccessMember
	err := tx.Where("target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", route.TargetID, telegramID, route.BotID, route.ChatID, route.RouteVersion).
		Where("observation_source IN ? AND observed_at <= ?", []string{"query", "chat_member"}, at).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if member.Status == "kicked" {
		return c.ErrUntrustedIdentity
	}
	return nil
}

// enqueueAccessReconciliations backfills at most 100 unknown approvals missing their unique read task.
// tx is the current transaction; historical rows are never rewritten as successful approvals.
func enqueueAccessReconciliations(tx *gorm.DB, bot uint, at time.Time) error {
	var rows []d.AccessOperation
	err := tx.Where("bot_id = ? AND kind = ? AND state = ?", bot, "approve", c.AccessUnknown).
		Where("id NOT IN (?)", tx.Model(&d.AccessOperation{}).Select("parent_operation_id").Where("kind = ?", "reconcile")).
		Order("id").Limit(100).Find(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := enqueueAccessReconciliation(tx, row, at); err != nil {
			return err
		}
	}
	return nil
}

// enqueueAccessReconciliation copies the historical physical identity, using a unique parent business key.
// The read task has its own lease; it must never inherit the parent's lease or change its outcome.
func enqueueAccessReconciliation(tx *gorm.DB, parent d.AccessOperation, at time.Time) error {
	row := parent
	row.ID, row.ParentOperationID = 0, parent.ID
	row.Kind, row.BusinessKey = "reconcile", fmt.Sprintf("%s:%d:reconcile", parent.Kind, parent.ID)
	row.State, row.Version, row.Attempt = "pending", 1, 0
	row.LeaseToken, row.LeaseUntil = "", time.Time{}
	row.ErrorClass, row.ReviewReason, row.MemberStatus = "", "", ""
	row.MemberIsMember, row.ObservedAt = false, nil
	row.NextAttemptAt, row.CreatedAt, row.UpdatedAt = at, at, at
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// ReconcileAccessEligibility validates the leased read's original operation, invite and trusted ledger source.
// Returns current historical-ID entitlement, ignoring current account binding (not granting new access).
func (store *Store) ReconcileAccessEligibility(ctx context.Context, operation *d.AccessOperation, at time.Time) (out c.AccessEligibility, err error) {
	if operation == nil || operation.Kind != "reconcile" || operation.ParentOperationID == 0 {
		return out, c.ErrInvalid
	}
	err = store.transaction(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := accessLeaseQuery(tx, operation, at).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
		out, err = reconcileAccessEligibility(tx, operation, at)
		return err
	})
	return out, err
}

// reconcileAccessEligibility uses only tx to verify immutable provenance and recalculate all historical sources.
// operation must be a claimed reconciliation; absent/mismatched provenance is never managed.
func reconcileAccessEligibility(tx *gorm.DB, operation *d.AccessOperation, at time.Time) (c.AccessEligibility, error) {
	var parent d.AccessOperation
	if err := tx.First(&parent, operation.ParentOperationID).Error; err != nil {
		return c.AccessEligibility{}, err
	}
	// Historical reads may bypass current binding/ban checks only here; this never authorizes a write.
	readableParent := parent.Kind == "approve" && parent.State == c.AccessUnknown ||
		(parent.Kind == "create" || parent.Kind == "approve") && (parent.State == c.AccessFailed || parent.State == c.AccessRetryable)
	if !readableParent || parent.InviteID != operation.InviteID || parent.JoinID != operation.JoinID || parent.TelegramID != operation.TelegramID || parent.AccountID != operation.AccountID || parent.TargetID != operation.TargetID || parent.BotID != operation.BotID || parent.ChatID != operation.ChatID || parent.RouteVersion != operation.RouteVersion {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	var invite d.AccessInvite
	if err := tx.First(&invite, parent.InviteID).Error; err != nil {
		return c.AccessEligibility{}, err
	}
	if invite.TelegramID != parent.TelegramID || invite.TargetID != parent.TargetID || invite.AccountID != parent.AccountID || invite.BotID != parent.BotID || invite.ChatID != parent.ChatID || invite.RouteVersion != parent.RouteVersion {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	var source d.LedgerSource
	if err := tx.First(&source, "id = ? AND account_id = ?", invite.SourceID, parent.AccountID).Error; err != nil {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	matched, err := im.SourceMatches(tx, invite.SourceID, parent.TargetID, parent.TelegramID)
	if err != nil {
		return c.AccessEligibility{}, err
	}
	if !matched {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	return accessEligibility(tx, c.AccessIdentity{TelegramID: parent.TelegramID}, parent.TargetID, at)
}

// FinishAccessReconcile atomically commits the leased read result, member watermark and review reason.
// Failed reads back off; successful left/restricted observations never prove the parent approval failed/succeeded.
func (store *Store) FinishAccessReconcile(ctx context.Context, operation *d.AccessOperation, outcome c.AccessMemberResult, observedAt, at time.Time) error {
	if operation == nil || operation.Kind != "reconcile" || observedAt.IsZero() || observedAt.After(at) {
		return c.ErrInvalid
	}
	return store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		values := map[string]any{"state": c.AccessRetryable, "error_class": accessErrorClass(outcome.ErrorClass), "next_attempt_at": at.Add(accessReconcileDelay(operation.Attempt, outcome.RetryAfter)), "version": operation.Version + 1, "lease_token": "", "updated_at": at}
		if outcome.Outcome == c.AccessSucceeded {
			eligible, err := reconcileAccessEligibility(tx, operation, at)
			if err != nil {
				return err
			}
			switch outcome.Status {
			case "member", "administrator", "creator":
				outcome.IsMember = true
			case "left", "kicked":
				outcome.IsMember = false
			case "restricted":
			default:
				return c.ErrInvalid
			}
			values["state"], values["error_class"] = c.AccessSucceeded, ""
			values["member_status"], values["member_is_member"], values["observed_at"] = outcome.Status, outcome.IsMember, observedAt
			values["entitlement_version"] = eligible.Version
			if outcome.IsMember && !eligible.Active {
				values["state"], values["review_reason"] = "needs_review", "member_without_entitlement"
			}
		} else if outcome.ErrorClass == "route_changed" || outcome.ErrorClass == "identity" {
			values["state"], values["review_reason"] = "needs_review", outcome.ErrorClass
		}
		updated := accessLeaseQuery(tx, operation, at).Updates(values)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		if outcome.Outcome == c.AccessSucceeded {
			_, err := observeAccess(tx, c.AccessObservation{TargetID: operation.TargetID, BotID: operation.BotID, ChatID: operation.ChatID, RouteVersion: operation.RouteVersion, TelegramID: operation.TelegramID, Status: outcome.Status, IsMember: outcome.IsMember, ObservedAt: observedAt, Source: "query"})
			return err
		}
		return nil
	})
}

// accessReconcileDelay bounds exponential read retries to one day, honoring a larger server retry hint.
// attempt is the claimed attempt number; no unknown side effect is retried by this calculation.
func accessReconcileDelay(attempt uint, hint time.Duration) time.Duration {
	if attempt > 10 {
		attempt = 10
	}
	delay := time.Minute * time.Duration(1<<attempt)
	if hint > delay {
		delay = hint
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}

// ListAccessReconciliations returns a cursor-bounded, secret-free recovery view for an authorized admin adapter.
// bot scopes access; after and limit page durable conclusions without exposing lease tokens or invitation secrets.
func (store *Store) ListAccessReconciliations(ctx context.Context, bot, after uint, limit int) ([]c.AccessReconciliation, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	var rows []c.AccessReconciliation
	err := store.db.WithContext(ctx).Model(&d.AccessOperation{}).
		Select("id, parent_operation_id, target_id, bot_id, chat_id, telegram_id, route_version, state, attempt, next_attempt_at, error_class, review_reason, member_status, member_is_member, observed_at, entitlement_version").
		Where("bot_id = ? AND kind = ? AND id > ?", bot, "reconcile", after).Order("id").Limit(limit).Scan(&rows).Error
	return rows, err
}

// PendingIdentityMigrations reads an ID-cursor page of explicitly reviewed scopes, without effects.
// The runtime bridge must verify route/fence and query actual Telegram membership outside transactions.
func (store *Store) PendingIdentityMigrations(ctx context.Context, bot, after uint, limit int) ([]c.IdentityMigrationWork, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	var rows []c.IdentityMigrationWork
	err := store.db.WithContext(ctx).Model(&d.IdentityMigration{}).Select("id, target_id, bot_id, chat_id, route_version, old_telegram_id, new_telegram_id").Where("bot_id = ? AND id > ? AND state IN ?", bot, after, []string{"awaiting_member", "cleanup_pending"}).Order("id").Limit(limit).Scan(&rows).Error
	return rows, err
}

var _ c.AccessRepository = (*Store)(nil)
