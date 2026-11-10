package routerecovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CheckBotInventory verifies the entire immutable inventory before any recovery HTTP.
// Missing histories, unresolved target purpose or unlisted module records fail closed.
func (store *Store) CheckBotInventory(ctx context.Context, input c.BotRecoveryInventory) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := checkInventory(tx, input)
		return err
	})
}

// CompleteBotInventory rescans historical liabilities even when no current member
// target exists, then persists a nonempty module receipt. No support proof is produced.
func (store *Store) CompleteBotInventory(ctx context.Context, input c.BotRecoveryInventory) (out c.BotRouteRecoveryResult, err error) {
	out.TaskID = input.Identity.TaskID
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		membership, err := checkInventory(tx, input)
		if err != nil {
			return err
		}
		digest, err := inventoryHash(input)
		if err != nil {
			return err
		}
		row := ModuleReceipt{TaskID: input.Identity.TaskID, InventoryHash: digest}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.First(&row, "task_id = ?", row.TaskID).Error; err != nil {
			return err
		}
		if row.InventoryHash != digest {
			return c.ErrConflict
		}
		current := map[uint]bool{}
		for _, route := range input.Current {
			if route.Purpose != "membership" {
				continue
			}
			current[route.TargetID] = true
			var run Run
			if err := tx.Where("task_id = ? AND target_id = ?", input.Identity.TaskID, route.TargetID).Take(&run).Error; err != nil {
				return err
			}
			if !run.Verified || run.CompletionReceipt == "" || run.OldBotID != input.Identity.OldBotID || run.NewBotID != input.Identity.NewBotID || run.OldVersion != input.Identity.OldVersion || run.NewVersion != input.Identity.NewVersion || run.OldGeneration != input.Identity.OldGeneration || run.NewGeneration != input.Identity.NewGeneration || run.OldChatID != route.ChatID || run.NewChatID != destinationChat(route) || run.OldRouteVersion != route.RouteVersion || run.NewRouteVersion != route.RouteVersion+1 {
				out.Unknown++
			}
			request := input.Identity
			request.TargetID, request.OldChatID, request.NewChatID = route.TargetID, route.ChatID, destinationChat(route)
			request.OldRouteVersion, request.NewRouteVersion = route.RouteVersion, route.RouteVersion+1
			if err := botFence(tx, request); err != nil {
				return err
			}
			if err := scanHistorical(tx, botScope(request)); err != nil {
				return err
			}
			var remaining, unknown int64
			if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", input.Identity.TaskID, route.TargetID, "remaining").Count(&remaining).Error; err != nil {
				return err
			}
			if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", input.Identity.TaskID, route.TargetID, "unknown").Count(&unknown).Error; err != nil {
				return err
			}
			out.Remaining += uint64(remaining)
			out.Unknown += uint64(unknown)
		}
		for target, route := range membership {
			if current[target] {
				continue
			}
			// Historical targets require either a real matching completed handoff or
			// proof of no immutable entitlement/order exposure; no permanent sentinel.
			if err := historicalHandoff(tx, input, target, route, &out); err != nil {
				return err
			}
			scope := scanRequest{Request: Request{TaskID: input.Identity.TaskID, TargetID: target, OldChatID: route.ChatID, NewChatID: route.ChatID, OldRouteVersion: route.RouteVersion, NewRouteVersion: route.RouteVersion + 1}, OldBotID: input.Identity.OldBotID, NewBotID: input.Identity.NewBotID}
			if err := scanHistorical(tx, scope); err != nil {
				return err
			}
			var remaining, unknown int64
			if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", scope.TaskID, target, "remaining").Count(&remaining).Error; err != nil {
				return err
			}
			if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ? AND state = ?", scope.TaskID, target, "unknown").Count(&unknown).Error; err != nil {
				return err
			}
			out.Remaining += uint64(remaining)
			out.Unknown += uint64(unknown)
		}
		if err := modulePending(tx, input, len(membership) == 0, &out); err != nil {
			return err
		}
		out.Complete = out.Remaining == 0 && out.Unknown == 0
		if out.Complete {
			out.CompletionReceipt = fmt.Sprintf("membership-bot:%d:%s", input.Identity.TaskID, digest)
		}
		return tx.Model(&ModuleReceipt{}).Where("task_id = ? AND inventory_hash = ?", row.TaskID, digest).Updates(map[string]any{"receipt": out.CompletionReceipt, "complete": out.Complete, "remaining": out.Remaining, "unknown": out.Unknown, "checked_at": time.Now().UTC()}).Error
	})
	if err != nil {
		out.Complete = false
		out.CompletionReceipt = ""
	}
	return out, err
}

// historicalHandoff resolves a previously unconditional historical blocker only
// through an exact persisted destination receipt, or genuinely zero exposure.
func historicalHandoff(tx *gorm.DB, input c.BotRecoveryInventory, target uint, route c.BotRecoveryRoute, out *c.BotRouteRecoveryResult) error {
	var current struct {
		BotInstanceID  uint
		TelegramChatID int64
		RouteVersion   uint64
	}
	if err := tx.Table("telegram_group_targets").Where("id = ?", target).Take(&current).Error; err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&Run{}).Where("target_id = ? AND old_chat_id = ? AND old_route_version = ? AND new_chat_id = ? AND new_route_version = ? AND verified = ? AND completion_receipt <> ? AND remaining = 0 AND unknown = 0 AND ((old_bot_id = ? AND new_bot_id = ?) OR (bot_id = ? AND bot_id = ?))", target, route.ChatID, route.RouteVersion, current.TelegramChatID, current.RouteVersion, true, "", input.Identity.OldBotID, current.BotInstanceID, input.Identity.OldBotID, current.BotInstanceID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if err := tx.Model(&d.LedgerSource{}).Where("target_id = ?", target).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		out.Unknown++
		return nil
	}
	var after uint
	for {
		var rows []d.OrderItemSnapshot
		if err := tx.Where("order_item_id > ?", after).Order("order_item_id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			for _, snapshot := range row.Targets {
				if snapshot.TargetID == target {
					out.Unknown++
					return nil
				}
			}
			after = row.OrderItemID
		}
		if len(rows) < 100 {
			return nil
		}
	}
}

// modulePending inventories reminders and targetless encrypted panel liabilities.
func modulePending(tx *gorm.DB, input c.BotRecoveryInventory, empty bool, out *c.BotRouteRecoveryResult) error {
	var pending int64
	if err := tx.Model(&d.Reminder{}).Where("bot_id IN ? AND state NOT IN ?", []uint{input.Identity.OldBotID, input.Identity.NewBotID}, []string{"sent", "cancelled", "failed", "superseded"}).Count(&pending).Error; err != nil {
		return err
	}
	out.Unknown += uint64(pending)
	if err := tx.Model(&d.AccessJoin{}).Where("bot_id IN ? AND state NOT IN ?", []uint{input.Identity.OldBotID, input.Identity.NewBotID}, []string{"succeeded", "failed", "approved", "declined"}).Count(&pending).Error; err != nil {
		return err
	}
	out.Unknown += uint64(pending)
	if empty {
		if err := tx.Table("telegram_bot_deliveries").Where("bot_id IN ? AND purpose = ? AND status NOT IN ?", []uint{input.Identity.OldBotID, input.Identity.NewBotID}, "panel", []string{"sent", "failed", "superseded"}).Count(&pending).Error; err != nil {
			return err
		}
		out.Unknown += uint64(pending)
	}
	return nil
}

// inventoryHash canonicalizes ordering so retries cannot rebind or lose historical scope.
func inventoryHash(input c.BotRecoveryInventory) (string, error) {
	input.Identity.ActorID = 0 // Execution actor is authorization, not immutable inventory identity.
	input.Current = append([]c.BotRecoveryRoute(nil), input.Current...)
	input.History = append([]c.BotRecoveryRoute(nil), input.History...)
	for _, rows := range [][]c.BotRecoveryRoute{input.Current, input.History} {
		sort.Slice(rows, func(left, right int) bool {
			if rows[left].TargetID != rows[right].TargetID {
				return rows[left].TargetID < rows[right].TargetID
			}
			return rows[left].RouteVersion < rows[right].RouteVersion
		})
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

// scanHistorical reuses all existing inventory rules; no scan alone grants admission.
func scanHistorical(tx *gorm.DB, scope scanRequest) error {
	at := time.Now().UTC()
	if err := tx.Model(&Evidence{}).Where("task_id = ? AND target_id = ?", scope.TaskID, scope.TargetID).Update("state", "unknown").Error; err != nil {
		return err
	}
	for _, scan := range []func(*gorm.DB, scanRequest, time.Time) error{scanLedger, scanInvites, scanOperations, scanMembers, scanJoins, scanMigrations, scanReceipts, scanActions} {
		if err := scan(tx, scope, at); err != nil {
			return err
		}
	}
	if scope.OldBotID != scope.NewBotID && scope.OldChatID != scope.NewChatID {
		shared := &groupSession{}
		if err := shared.scanAdmission(tx, scope); err != nil {
			return err
		}
		if err := shared.bindOperations(tx, scope, at); err != nil {
			return err
		}
	}
	return nil
}

// checkInventory compares every old physical route and every newly owned current
// target. Historical purposes come from real target rows, not omitted JSON fields.
func checkInventory(tx *gorm.DB, input c.BotRecoveryInventory) (map[uint]c.BotRecoveryRoute, error) {
	if err := runtimeFences(tx, input.Identity); err != nil {
		return nil, err
	}
	current := map[uint]c.BotRecoveryRoute{}
	history := map[string]c.BotRecoveryRoute{}
	membership := map[uint]c.BotRecoveryRoute{}
	for _, route := range input.Current {
		if route.TargetID == 0 || route.ChatID >= 0 || route.NewChatID > 0 || route.RouteVersion == 0 || current[route.TargetID].TargetID != 0 {
			return nil, c.ErrInvalid
		}
		current[route.TargetID] = route
	}
	for _, route := range input.History {
		key := fmt.Sprintf("%d:%d", route.TargetID, route.RouteVersion)
		if route.TargetID == 0 || route.ChatID >= 0 || route.RouteVersion == 0 || history[key].TargetID != 0 {
			return nil, c.ErrInvalid
		}
		history[key] = route
	}
	var routes []struct {
		TargetID       uint
		TelegramChatID int64
		RouteVersion   uint64
	}
	if err := tx.Table("telegram_group_routes").Where("bot_instance_id = ?", input.Identity.OldBotID).Order("target_id, route_version").Find(&routes).Error; err != nil {
		return nil, err
	}
	if len(routes) != len(history) {
		return nil, c.ErrConflict
	}
	for _, route := range routes {
		expected, ok := history[fmt.Sprintf("%d:%d", route.TargetID, route.RouteVersion)]
		if !ok || expected.ChatID != route.TelegramChatID {
			return nil, c.ErrConflict
		}
		if err := matchedLock(tx.Table("telegram_group_targets").Where("id = ?", route.TargetID), "version"); err != nil {
			return nil, err
		}
		var target struct{ Purpose string }
		if err := tx.Table("telegram_group_targets").Where("id = ?", route.TargetID).Take(&target).Error; err != nil {
			return nil, err
		}
		if target.Purpose != "membership" && target.Purpose != "support" {
			return nil, c.ErrConflict
		}
		if expected.Purpose != "" && expected.Purpose != target.Purpose {
			return nil, c.ErrConflict
		}
		if target.Purpose == "membership" {
			expected.Purpose = target.Purpose
			membership[route.TargetID] = expected
		}
	}
	var targets []struct {
		ID             uint
		Purpose        string
		TelegramChatID int64
		RouteVersion   uint64
		RecoveryTaskID uint
		BotInstanceID  uint
	}
	if err := tx.Table("telegram_group_targets").Where("bot_instance_id IN ?", []uint{input.Identity.OldBotID, input.Identity.NewBotID}).Find(&targets).Error; err != nil {
		return nil, err
	}
	if len(targets) != len(current) {
		return nil, c.ErrConflict
	}
	for _, target := range targets {
		route, ok := current[target.ID]
		if !ok || target.BotInstanceID != input.Identity.NewBotID || route.Purpose != target.Purpose || destinationChat(route) != target.TelegramChatID || route.RouteVersion+1 != target.RouteVersion || target.RecoveryTaskID != input.Identity.TaskID {
			return nil, c.ErrConflict
		}
		if _, ok := history[fmt.Sprintf("%d:%d", route.TargetID, route.RouteVersion)]; !ok {
			return nil, c.ErrConflict
		}
		if route.Purpose == "membership" {
			membership[target.ID] = route
		}
	}
	if err := checkOrphans(tx, input, membership); err != nil {
		return nil, err
	}
	return membership, nil
}

// destinationChat preserves historical single-chat snapshots without inventing mappings.
func destinationChat(route c.BotRecoveryRoute) int64 {
	if route.NewChatID != 0 {
		return route.NewChatID
	}
	return route.ChatID
}

// checkOrphans refuses uncovered bot-owned access/identity/reminder records and
// historical order targets. Snapshot scans are paged; no SQL JSON dialect is assumed.
func checkOrphans(tx *gorm.DB, input c.BotRecoveryInventory, membership map[uint]c.BotRecoveryRoute) error {
	ids := []uint{0}
	chats := []int64{0}
	for target := range membership {
		ids = append(ids, target)
	}
	for _, route := range input.History {
		if _, ok := membership[route.TargetID]; ok {
			chats = append(chats, route.ChatID)
		}
	}
	for _, route := range input.Current {
		chats = append(chats, destinationChat(route))
	}
	for _, model := range []any{&d.AccessInvite{}, &d.AccessOperation{}, &d.AccessMember{}, &d.IdentityMigration{}, &d.Reminder{}} {
		var count int64
		if err := tx.Model(model).Where("bot_id IN ? AND (target_id = 0 OR target_id NOT IN ?)", []uint{input.Identity.OldBotID, input.Identity.NewBotID}, ids).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return c.ErrConflict
		}
	}
	var count int64
	if err := tx.Model(&d.AccessJoin{}).Where("bot_id IN ? AND (chat_id = 0 OR chat_id NOT IN ?)", []uint{input.Identity.OldBotID, input.Identity.NewBotID}, chats).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return c.ErrConflict
	}
	var after uint
	for {
		var rows []d.OrderItemSnapshot
		if err := tx.Where("order_item_id > ?", after).Order("order_item_id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			for _, target := range row.Targets {
				if target.BotInstanceID == input.Identity.OldBotID || target.BotInstanceID == input.Identity.NewBotID {
					if _, ok := membership[target.TargetID]; !ok {
						return c.ErrConflict
					}
					covered := false
					for _, route := range input.History {
						if target.BotInstanceID == input.Identity.OldBotID && route.TargetID == target.TargetID && route.ChatID == target.TelegramChatID && route.RouteVersion == target.RouteVersion {
							covered = true
						}
					}
					for _, route := range input.Current {
						if target.BotInstanceID == input.Identity.NewBotID && route.TargetID == target.TargetID && route.ChatID == target.TelegramChatID && route.RouteVersion+1 == target.RouteVersion {
							covered = true
						}
					}
					if !covered {
						return c.ErrConflict
					}
				}
			}
			after = row.OrderItemID
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
