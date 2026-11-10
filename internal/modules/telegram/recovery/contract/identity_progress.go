package contract

// IdentityMapping exposes only persisted membership progress, never inferred parent state.
// All identifiers are decimal strings to preserve Telegram and database ID precision.
type IdentityMapping struct {
	ID            string `json:"id"`
	TargetID      string `json:"target_id"`
	State         string `json:"state"`
	OldTelegramID string `json:"old_telegram_id"`
	NewTelegramID string `json:"new_telegram_id"`
}
