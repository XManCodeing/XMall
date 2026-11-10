package gormstore

import (
	"context"
	"database/sql"
	"fmt"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// enqueueCleanupOnlyLifecycle 保存已离群/只有邀请的独立成员结论，不伪造平台移出成功。
// cycle 为零表示没有邀请责任；同周期每物理路由仅创建一条可见任务。
func enqueueCleanupOnlyLifecycle(tx *gorm.DB, member d.AccessMember, eligible c.AccessEligibility, cycle uint, at time.Time, operationIDs ...*uint) error {
	if cycle == 0 {
		return nil
	}
	operation := d.AccessOperation{BusinessKey: fmt.Sprintf("cleanup:%d:%d:%d", cycle, member.BotID, member.ChatID), Kind: "lifecycle", CleanupCycleID: cycle, TelegramID: member.TelegramID, TargetID: member.TargetID, BotID: member.BotID, ChatID: member.ChatID, RouteVersion: member.RouteVersion, EntitlementVersion: eligible.Version, State: "succeeded", Version: 1, LifecyclePhase: "not_required", MemberStatus: member.Status, NextAttemptAt: at, CreatedAt: at, UpdatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&operation).Error; err != nil {
		return err
	}
	return captureLifecycleID(tx, operation, operationIDs...)
}

// LifecycleUsersPage pages distinct users in bot using optional status only for selection.
// Count, page identities and complete history share a read snapshot; errors return no partial page.
func (store *Store) LifecycleUsersPage(ctx context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleUserTasks, int64, error) {
	if _, err := c.LifecycleFilterStates(status...); err != nil {
		return nil, 0, err
	}
	filter := ""
	if len(status) == 1 {
		filter = status[0]
	}
	return store.SearchLifecycleUsersPage(ctx, bot, page, size, filter, "")
}

// SearchLifecycleUsersPage selects bot-scoped users by status and keyword, returning complete history.
// All projections share a read snapshot; invalid inputs and storage failures return no partial results.
func (store *Store) SearchLifecycleUsersPage(ctx context.Context, bot uint, page, size int, status, keyword string) ([]c.LifecycleUserTasks, int64, error) {
	states, err := c.LifecycleFilterStates(status)
	keyword, identity, keywordErr := c.LifecycleKeyword(keyword)
	if bot == 0 || !c.ValidAdminPage(page, size) || err != nil || keywordErr != nil {
		return nil, 0, c.ErrInvalid
	}
	groups := []c.LifecycleUserTasks{}
	var total int64
	// Match OrderStates: SQLite uses its native snapshot, PostgreSQL needs repeatable-read.
	options := &sql.TxOptions{ReadOnly: true}
	if store.db.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&d.AccessOperation{}).Where("bot_id = ? AND kind = ?", bot, "lifecycle")
		if identity > 0 {
			query = query.Where("telegram_id = ?", identity)
		} else if keyword != "" {
			// Explicit ESCAPE works in SQLite/PostgreSQL; underscores are literal username characters.
			pattern := "%" + strings.ReplaceAll(strings.TrimPrefix(keyword, "@"), "_", "!_") + "%"
			query = query.Where("LOWER(COALESCE((?), '')) LIKE ? ESCAPE '!'", lifecycleUsername(tx), pattern)
		}
		if len(states) > 0 {
			query = query.Where("state IN ?", states)
		}
		if err := query.Distinct("telegram_id").Count(&total).Error; err != nil {
			return err
		}
		var identities []int64
		if err := query.Distinct("telegram_id").Order("telegram_id").Offset((page-1)*size).Limit(size).Pluck("telegram_id", &identities).Error; err != nil {
			return err
		}
		if len(identities) == 0 {
			return nil
		}
		var tasks []c.LifecycleTask
		// No state predicate or row limit here: histories must never be split or hidden.
		if err := tx.Model(&d.AccessOperation{}).Where("bot_id = ? AND kind = ? AND telegram_id IN ?", bot, "lifecycle", identities).Order("id").Scan(&tasks).Error; err != nil {
			return err
		}
		// Scalar identity projection cannot multiply users or task rows, even with legacy duplicate identities.
		var profiles []struct {
			TelegramID       int64
			TelegramUsername string
		}
		if err := tx.Model(&d.AccessOperation{}).
			Select("DISTINCT telegram_id, COALESCE((?), '') AS telegram_username", lifecycleUsername(tx)).
			Where("bot_id = ? AND kind = ? AND telegram_id IN ?", bot, "lifecycle", identities).
			Order("telegram_id").Scan(&profiles).Error; err != nil {
			return err
		}
		positions := make(map[int64]int, len(identities))
		for _, profile := range profiles {
			positions[profile.TelegramID] = len(groups)
			groups = append(groups, c.LifecycleUserTasks{TelegramID: profile.TelegramID, TelegramUsername: profile.TelegramUsername, Tasks: []c.LifecycleTask{}})
		}
		reader := New(tx)
		for index := range tasks {
			if err := reader.projectInviteCleanup(ctx, &tasks[index]); err != nil {
				return err
			}
			position := positions[tasks[index].TelegramID]
			groups[position].Tasks = append(groups[position].Tasks, tasks[index])
		}
		return nil
	}, options)
	if err != nil {
		return nil, 0, err
	}
	return groups, total, nil
}

// lifecycleUsername correlates the immutable task Telegram ID with the actual provider profile.
// tx is the same read transaction. Latest updated profile wins (ID breaks ties), including cleared names.
// Identity rows have no soft-delete column; deleted owning users are excluded like Telegram user listings.
func lifecycleUsername(tx *gorm.DB) *gorm.DB {
	return telegramProfileUsername(tx, "telegram_membership_access_operations")
}

// telegramProfileUsername correlates a fixed internal table's Telegram ID with the latest profile.
// table must be an internal constant, never request input; tx keeps the caller's snapshot.
func telegramProfileUsername(tx *gorm.DB, table string) *gorm.DB {
	return tx.Session(&gorm.Session{NewDB: true}).Table("user_oauth_identities AS identity_profile").
		Select("identity_profile.username").
		Joins("JOIN users AS identity_owner ON identity_owner.id = identity_profile.user_id AND identity_owner.deleted_at IS NULL").
		Where("identity_profile.provider = ? AND identity_profile.provider_user_id = "+dblock.TextCast(tx, table+".telegram_id"), "telegram").
		Order("identity_profile.updated_at DESC, identity_profile.id DESC").Limit(1)
}

// SearchAccessReviewsPage returns a snapshot-consistent bot-scoped application page.
// Keyword is normalized by the shared identity validator; failures return no partial rows or count.
func (store *Store) SearchAccessReviewsPage(ctx context.Context, bot uint, page, size int, keyword string, status ...string) ([]c.AccessReview, int64, error) {
	filter, filterErr := c.AccessReviewFilter(status...)
	keyword, identity, err := c.LifecycleKeyword(keyword)
	if filterErr != nil || err != nil || bot == 0 || !c.ValidAdminPage(page, size) {
		return nil, 0, c.ErrInvalid
	}
	rows := []c.AccessReview{}
	var total int64
	options := &sql.TxOptions{ReadOnly: true}
	if store.db.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := accessReviewQuery(tx, bot)
		if filter == "pending" {
			query = query.Where("reviewed_at IS NULL")
		}
		if filter == "reviewed" {
			query = query.Where("reviewed_at IS NOT NULL")
		}
		if identity > 0 {
			query = query.Where("telegram_id = ?", identity)
		} else if keyword != "" {
			pattern := "%" + strings.ReplaceAll(strings.TrimPrefix(keyword, "@"), "_", "!_") + "%"
			query = query.Where("LOWER(COALESCE((?), '')) LIKE ? ESCAPE '!'", telegramProfileUsername(tx, "telegram_membership_access_joins"), pattern)
		}
		if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return err
		}
		return query.Order("id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error
	}, options)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

var _ c.AccessReviewSearchReader = (*Store)(nil)
var _ c.LifecycleUserPageReader = (*Store)(nil)
var _ c.LifecycleUserSearchReader = (*Store)(nil)

// projectInviteCleanup 在只读 API 中独立汇总清理结果；移出成功不能覆盖失败/未知邀请责任。
// task 是授权范围内单任务，返回数据库错误，不输出密文、URL 或租约。
func (store *Store) projectInviteCleanup(ctx context.Context, task *c.LifecycleTask) error {
	task.MemberOutcome = task.State
	if task.LifecyclePhase == "not_required" {
		task.MemberOutcome = "not_required"
	}
	task.InviteCleanup = "unconfirmed"
	if task.CleanupCycleID == 0 {
		return nil
	}
	task.InviteCleanup = "not_required"
	var items []d.InviteCleanupItem
	if err := store.db.WithContext(ctx).Where("cycle_id = ?", task.CleanupCycleID).Find(&items).Error; err != nil {
		return err
	}
	for _, item := range items {
		state := item.State
		if item.OperationID != 0 && state != "succeeded" {
			var operation d.AccessOperation
			if err := store.db.WithContext(ctx).First(&operation, item.OperationID).Error; err != nil {
				return err
			}
			state = operation.State
		}
		task.InviteCleanupTotal++
		switch state {
		case "succeeded":
			task.InviteCleanupCompleted++
		case "failed", "needs_review":
			task.InviteCleanupFailed++
		case "unknown":
			task.InviteCleanupUnknown++
		}
	}
	switch {
	case task.InviteCleanupUnknown > 0:
		task.InviteCleanup = "unknown"
	case task.InviteCleanupFailed > 0:
		task.InviteCleanup = "failed"
	case task.InviteCleanupTotal > task.InviteCleanupCompleted:
		task.InviteCleanup = "pending"
	case task.InviteCleanupTotal > 0:
		task.InviteCleanup = "succeeded"
	}
	if task.State == "succeeded" && task.InviteCleanup != "succeeded" && task.InviteCleanup != "not_required" {
		task.State = task.InviteCleanup
	}
	return nil
}
