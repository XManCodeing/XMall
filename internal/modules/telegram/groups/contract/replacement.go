package contract

import (
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// ReplacementInput identifies a proposed physical route, never an implicit name match.
// Versions are the administrator's last observed source configuration and route.
type ReplacementInput struct {
	TargetID             uint   `json:"target_id"`
	ExpectedVersion      uint64 `json:"expected_version"`
	ExpectedRouteVersion uint64 `json:"expected_route_version"`
	NewBotInstanceID     uint   `json:"new_bot_instance_id"`
	NewTelegramChatID    string `json:"new_telegram_chat_id"`
}

// ReplacementPreview is a read-only, short-lived capability snapshot.
// It is not authority to switch a route: confirmation must revalidate versions and occupancy.
type ReplacementPreview struct {
	Input       ReplacementInput `json:"input"`
	Source      d.Target         `json:"source"`
	Destination d.Observation    `json:"destination"`
	CheckedAt   time.Time        `json:"checked_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
}
