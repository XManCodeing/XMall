package routerecovery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	recovery "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	"gorm.io/gorm"
)

// Request is the existing cross-module recovery contract. Same-Bot replacement
// retains stable target/order identities and the original entitlement expiry.
type Request recovery.GroupRecoveryRequest

// Result separates entry readiness from full historical cleanup verification.
type Result recovery.GroupRecoveryResult

// scanRequest adds explicit dual identities without weakening the group contract.
type scanRequest struct {
	Request
	OldBotID, NewBotID uint
}

// Recoverer preserves the legacy admission/cleanup bridge for compatible callers.
type Recoverer interface {
	RecoverRoute(context.Context, Request) (Result, error)
}

// EntrySwitcher is an optional same-Bot entry-only recovery port. Request supplies
// the trusted actor and immutable route; Result never certifies historical cleanup.
type EntrySwitcher interface {
	SwitchEntry(context.Context, Request) (Result, error)
}

// SwitchEntry inventories the exact fenced request without any platform calls,
// invitations, admissions or cleanup. It returns durable entry proof separately
// from full cleanup proof; old members and active links remain real liabilities.
func (worker *GroupRecovery) SwitchEntry(ctx context.Context, input Request) (Result, error) {
	out := Result{TaskID: input.TaskID, TargetID: input.TargetID, RouteVersion: input.NewRouteVersion}
	if input.ActorID == 0 {
		return out, c.ErrInvalid
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return out, err
	}
	if err := session.check(ctx); err != nil {
		return out, err
	}
	err = worker.Store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := session.fence(tx); err != nil {
			return err
		}
		run, err := lockRun(tx, input)
		if err != nil {
			return err
		}
		scope := scanRequest{Request: input, OldBotID: input.BotID, NewBotID: input.BotID}
		if err := scanHistorical(tx, scope); err != nil {
			return err
		}
		if err := scanEntrySafety(tx, scope); err != nil {
			return err
		}
		var evidence []Evidence
		if err := tx.Where("task_id = ? AND target_id = ?", input.TaskID, input.TargetID).Find(&evidence).Error; err != nil {
			return err
		}
		blocked := false
		for _, row := range evidence {
			switch row.State {
			case "unknown":
				out.Unknown++
			case "remaining":
				out.Remaining++
				// Only known old membership/link liability is intentionally deferred.
				// Queued panels and all pending writes remain blocking, not cancelled.
				blocked = blocked || row.Kind != "member" && row.Kind != "invite"
			}
		}
		out.EntryReady = out.Unknown == 0 && !blocked
		out.Checkpointed = out.Unknown == 0
		receipt := ""
		if out.EntryReady {
			receipt = fmt.Sprintf("membership-entry:%d:%d:%d", input.TaskID, input.TargetID, input.NewRouteVersion)
		}
		updated := tx.Model(&Run{}).Where("task_id = ? AND target_id = ? AND version = ?", input.TaskID, input.TargetID, run.Version).Updates(map[string]any{
			"version": run.Version + 1, "verified": false, "remaining": out.Remaining,
			"unknown": out.Unknown, "completion_receipt": receipt, "scanned_at": time.Now().UTC(),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		out.EntryReady, out.Checkpointed = false, false
	}
	slog.InfoContext(ctx, "membership_entry_switch_checkpoint", "task_id", input.TaskID, "target_id", input.TargetID, "entry_ready", out.EntryReady, "remaining", out.Remaining, "unknown", out.Unknown, "persisted", err == nil)
	return out, err
}

// scanEntrySafety supplements the shared historical scans with entry-specific
// destination/lease checks. tx is the fenced transaction and scope is immutable;
// errors abort proof persistence, while uncertain facts remain explicit evidence.
func scanEntrySafety(tx *gorm.DB, scope scanRequest) error {
	var operations []d.AccessOperation
	if err := accessScope(tx, scope).Find(&operations).Error; err != nil {
		return err
	}
	for _, operation := range operations {
		state := "resolved"
		if operation.LeaseToken != "" || operation.State == "creating" || operation.BanOutcome == c.AccessUnknown || operation.UnbanOutcome == c.AccessUnknown {
			state = "unknown"
		}
		if err := record(tx, scope, time.Now().UTC(), Evidence{Kind: "entry_operation", Reference: fmt.Sprint(operation.ID), State: state}); err != nil {
			return err
		}
	}
	if err := scanEntryInvites(tx, scope); err != nil {
		return err
	}
	var members []d.AccessMember
	if err := accessScope(tx, scope).Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		if routeState(scope, member.TargetID, member.BotID, member.ChatID, member.RouteVersion) != "destination" {
			continue
		}
		state := "resolved"
		switch member.Status {
		case "member", "administrator", "creator", "left":
		case "restricted":
			if !member.IsMember {
				state = "unknown"
			}
		default:
			state = "unknown"
		}
		if err := record(tx, scope, time.Now().UTC(), Evidence{Kind: "entry_destination", Reference: fmt.Sprintf("%d:%d", member.ChatID, member.TelegramID), State: state}); err != nil {
			return err
		}
	}
	return nil
}

// scanEntryInvites permits only a known active old link to remain usable elsewhere.
// Pending or malformed links are not blanket historical exemptions; tx and scope
// belong to the entry transaction, and any database error aborts its proof.
func scanEntryInvites(tx *gorm.DB, scope scanRequest) error {
	var invites []d.AccessInvite
	if err := accessScope(tx, scope).Find(&invites).Error; err != nil {
		return err
	}
	for _, invite := range invites {
		state := "resolved"
		if !d.IsInviteExpiredAt(invite, time.Now().UTC()) && invite.State != "active" && invite.State != "revoked" && invite.State != "failed" {
			state = "unknown"
		}
		if err := record(tx, scope, time.Now().UTC(), Evidence{Kind: "entry_invite", Reference: fmt.Sprint(invite.ID), State: state}); err != nil {
			return err
		}
	}
	return nil
}

var _ EntrySwitcher = (*GroupRecovery)(nil)
