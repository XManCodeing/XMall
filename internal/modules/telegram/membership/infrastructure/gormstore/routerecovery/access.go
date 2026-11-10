package routerecovery

import (
	"fmt"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// scanInvites enumerates all target/physical-route invitations, including unknown histories.
// Only explicit revocation or trusted expiry closes a live link; no cleanup is fabricated.
func scanInvites(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []d.AccessInvite
	if err := accessScope(tx, request).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "resolved"
		route := routeState(request, row.TargetID, row.BotID, row.ChatID, row.RouteVersion)
		switch {
		case route == "unknown", row.State == "unknown", row.State == "creating":
			state = "unknown"
		case row.State == "revoked":
		case d.IsInviteExpiredAt(row, at):
		case route == "old":
			state = "remaining"
		case row.State != "pending" && row.State != "active" && row.State != "failed":
			state = "unknown"
		}
		if err := record(tx, request, at, Evidence{Kind: "invite", Reference: fmt.Sprint(row.ID), State: state, TelegramID: row.TelegramID, SourceID: row.SourceID}); err != nil {
			return err
		}
	}
	return nil
}

// scanOperations inventories the existing access/lifecycle queue without rewriting attempts,
// leases or outcomes. In-flight and unknown work require explicit reconciliation.
func scanOperations(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []d.AccessOperation
	if err := accessScope(tx, request).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "resolved"
		route := routeState(request, row.TargetID, row.BotID, row.ChatID, row.RouteVersion)
		switch {
		case route == "unknown", row.State == "creating", row.State == "unknown":
			state = "unknown"
		case row.State == "pending", row.State == "retryable":
			state = "remaining"
		case row.State != "succeeded" && row.State != "failed":
			state = "unknown"
		}
		// An approval is not actual membership, even when the HTTP operation succeeded.
		if route == "old" && row.Kind == "approve" && row.State == "succeeded" {
			observations := tx.Model(&d.AccessMember{}).Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND observed_at >= ?", request.TargetID, request.OldChatID, row.TelegramID, row.UpdatedAt)
			if request.OldChatID == request.NewChatID {
				// An acknowledged approval has no retired physical membership when
				// only the Bot changes. Require the destination's trusted observation;
				// this branch never claims an unknown approval succeeded.
				observations = observations.Where("bot_id = ? AND route_version = ? AND source_id > 0 AND observation_source IN ? AND status IN ?", request.NewBotID, request.NewRouteVersion, []string{"query", "chat_member"}, []string{"member", "administrator", "creator", "restricted", "left", "kicked"})
			} else {
				observations = observations.Where("bot_id = ? AND status IN ? AND is_member = ?", request.OldBotID, []string{"left", "kicked"}, false)
			}
			var count int64
			if err := observations.Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				state = "unknown"
			}
		}
		if err := record(tx, request, at, Evidence{Kind: "operation", Reference: fmt.Sprint(row.ID), State: state, TelegramID: row.TelegramID}); err != nil {
			return err
		}
	}
	return nil
}

// scanMembers preserves known membership responsibilities; no old identity is removed here.
// A terminal trusted absence is the only local closure, never an inferred successful kick.
func scanMembers(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []d.AccessMember
	if err := accessScope(tx, request).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "resolved"
		route := routeState(request, row.TargetID, row.BotID, row.ChatID, row.RouteVersion)
		if route == "unknown" || row.SourceID == 0 || row.ObservedAt.IsZero() || (row.ObservationSource != "chat_member" && row.ObservationSource != "query") {
			state = "unknown"
		} else if route == "old" {
			if row.IsMember || row.Status == "member" || row.Status == "administrator" || row.Status == "creator" {
				state = "remaining"
			} else if row.Status != "left" && row.Status != "kicked" {
				state = "unknown"
			}
		}
		key := fmt.Sprintf("%d:%d:%d", row.TargetID, row.ChatID, row.TelegramID)
		if err := record(tx, request, at, Evidence{Kind: "member", Reference: key, State: state, TelegramID: row.TelegramID, SourceID: row.SourceID}); err != nil {
			return err
		}
	}
	return nil
}

// scanJoins includes unlinked requests by physical chat so an empty ledger is not false proof.
func scanJoins(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []d.AccessJoin
	if err := tx.Where("chat_id IN ? OR invite_id IN (?)", []int64{request.OldChatID, request.NewChatID}, tx.Model(&d.AccessInvite{}).Select("id").Where("target_id = ?", request.TargetID)).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "resolved"
		if (row.BotID != request.OldBotID && row.BotID != request.NewBotID) || row.State == "unknown" || row.State == "creating" || row.State == "needs_review" {
			state = "unknown"
		} else if row.State == "pending" || row.State == "retryable" {
			state = "remaining"
		} else if row.State == "approved" {
			observations := tx.Model(&d.AccessMember{}).Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND observed_at >= ? AND source_id > 0", request.TargetID, row.ChatID, row.TelegramID, row.UpdatedAt)
			if request.OldChatID == request.NewChatID && row.BotID == request.OldBotID {
				observations = observations.Where("bot_id = ? AND route_version = ? AND observation_source IN ?", request.NewBotID, request.NewRouteVersion, []string{"query", "chat_member"})
			} else {
				observations = observations.Where("bot_id = ?", row.BotID)
			}
			var count int64
			if err := observations.Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				state = "remaining"
			}
		} else if row.State != "succeeded" && row.State != "failed" && row.State != "declined" {
			state = "unknown"
		}
		if err := record(tx, request, at, Evidence{Kind: "join", Reference: fmt.Sprint(row.ID), State: state, TelegramID: row.TelegramID}); err != nil {
			return err
		}
	}
	return nil
}

// scanMigrations refuses to bypass new-identity admission before old-identity removal.
// Existing physical mappings need their own reviewed transition and are never rewritten.
func scanMigrations(tx *gorm.DB, request scanRequest, at time.Time) error {
	var rows []d.IdentityMigration
	if err := accessScope(tx, request).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		state := "unknown"
		if routeState(request, row.TargetID, row.BotID, row.ChatID, row.RouteVersion) == "destination" && row.State == "completed" {
			state = "resolved"
		}
		if err := record(tx, request, at, Evidence{Kind: "identity_migration", Reference: fmt.Sprint(row.ID), State: state}); err != nil {
			return err
		}
	}
	return nil
}
