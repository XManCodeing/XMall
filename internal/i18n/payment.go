package i18n

// init registers payment balance feedback in the existing three-language catalog.
func init() {
	messages[LocaleZH]["error.wallet_insufficient_balance"] = "钱包余额不足，请先充值或选择其他支付方式"
	messages[LocaleTW]["error.wallet_insufficient_balance"] = "錢包餘額不足，請先儲值或選擇其他付款方式"
	messages[LocaleEN]["error.wallet_insufficient_balance"] = "Insufficient wallet balance. Top up or choose another payment method"
}
