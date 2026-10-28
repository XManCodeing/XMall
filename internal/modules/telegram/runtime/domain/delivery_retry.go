package domain

// CanRetryBroadcastDelivery permits only definite Telegram rejections. Unknown
// outcomes, identity/binding failures and unreadable payloads fail closed.
// Keep the SQL compare-and-set predicate in broadcaststore in sync.
func CanRetryBroadcastDelivery(d Delivery) bool {
	return d.PayloadCipher != "" && d.BroadcastID != 0 && d.Status == "failed" && d.LastError == "telegram rejected message" && d.MessageID == 0 && d.Claim == "" && d.ClaimedUntil == nil
}
