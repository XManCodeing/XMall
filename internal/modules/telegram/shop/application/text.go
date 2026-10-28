package application

import "strings"

func locale(l string) string {
	if strings.HasPrefix(l, "en") {
		return "en-US"
	}
	if l == "zh-TW" || l == "zh-Hant" || l == "zh-HK" {
		return "zh-TW"
	}
	return "zh-CN"
}
func text(l, k string) string {
	n := 0
	if locale(l) == "zh-TW" {
		n = 1
	}
	if locale(l) == "en-US" {
		n = 2
	}
	if v, ok := texts[k]; ok {
		return v[n]
	}
	return k
}

var texts = map[string][3]string{
	"history_account":   {"原账号余额（只读，不合并）", "原帳號餘額（唯讀，不合併）", "Previous balance (read only, not merged)"},
	"history_refunds":   {"原账号余额退款记录（只读）", "原帳號餘額退款紀錄（唯讀）", "Previous wallet refunds (read only)"},
	"payment_uncertain": {"支付请求正在处理或结果暂不确定，请先查询订单，不要重复付款。", "付款請求正在處理或結果暫不確定，請先查詢訂單，不要重複付款。", "Payment creation is processing or uncertain. Check the order first; do not pay twice."},
	"welcome":           {"欢迎光临，请选择服务。", "歡迎光臨，請選擇服務。", "Welcome. Choose a service."},
	"catalog":           {"商品目录", "商品目錄", "Catalog"}, "orders": {"我的订单", "我的訂單", "My orders"}, "account": {"账号与余额", "帳號與餘額", "Account and balance"}, "bind": {"绑定已有账号", "綁定已有帳號", "Bind existing account"}, "history": {"绑定前历史订单（只读）", "綁定前歷史訂單（唯讀）", "Previous account orders (read only)"}, "language": {"语言", "語言", "Language"}, "help": {"帮助", "說明", "Help"},
	"help_text": {"选择商品、规格和数量，确认报价后下单。支付后通过订单查询交付内容。/cancel 取消当前操作，/back 返回菜单。绑定不会合并余额；历史订单仅可查询。", "選擇商品、規格和數量，確認報價後下單。付款後透過訂單查詢交付內容。/cancel 取消目前操作，/back 返回選單。綁定不會合併餘額；歷史訂單僅可查詢。", "Select a product, SKU and quantity, then confirm the quote. Check your order after payment for delivery. /cancel cancels this flow; /back returns to the menu. Binding does not merge balances. Previous orders are read-only."},
	"back":      {"返回主菜单 / 取消", "返回主選單 / 取消", "Menu / cancel"}, "stale": {"按钮已过期，请使用最新菜单或 /start。", "按鈕已過期，請使用最新選單或 /start。", "This button expired. Use the latest menu or /start."}, "quantity": {"请输入购买数量（正整数）。", "請輸入購買數量（正整數）。", "Enter the quantity (positive integer)."}, "email": {"请输入已有账号的邮箱。绑定后原账号订单保留只读访问，余额不会合并。", "請輸入已有帳號的電子郵件。綁定後原帳號訂單保留唯讀存取，餘額不會合併。", "Enter your existing account email. Previous orders remain read-only; balances are not merged."}, "code": {"验证码已发送，请输入邮箱验证码。", "驗證碼已傳送，請輸入電子郵件驗證碼。", "Code sent. Enter the email verification code."},
	"quote": {"请确认报价", "請確認報價", "Review quote"}, "confirm": {"确认下单", "確認下單", "Place order"}, "channels": {"选择支付渠道", "選擇付款管道", "Choose payment method"}, "pay": {"打开收银台", "開啟收銀台", "Open checkout"}, "refresh": {"查询订单 / 支付结果", "查詢訂單 / 付款結果", "Check order / payment"}, "expires": {"有效期：", "有效期：", "Expires: "}, "paid": {"订单已付款，请查询交付状态。", "訂單已付款，請查詢交付狀態。", "Order paid. Check delivery status."}, "failed": {"当前操作不可用，请返回菜单重试。", "目前操作無法使用，請返回選單重試。", "This operation is unavailable. Return to the menu and retry."}, "price_changed": {"报价已变化，未创建订单。请核对新报价后再次确认。", "報價已變更，未建立訂單。請核對新報價後再次確認。", "The quote changed. No order was created. Review the new quote and confirm again."}, "unsupported": {"此商品需要填写表单，请前往商城网站购买。", "此商品需要填寫表單，請前往商城網站購買。", "This product requires a form. Please purchase on the store website."},
}
