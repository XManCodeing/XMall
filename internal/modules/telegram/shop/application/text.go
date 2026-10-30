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
	"status_fulfilling":          {"正在发货", "正在發貨", "Preparing delivery"},
	"status_partially_delivered": {"部分已发货", "部分已發貨", "Partially delivered"},
	"status_partially_refunded":  {"部分已退款", "部分已退款", "Partially refunded"},
	"restored":                   {"已为你打开最新菜单", "已為你開啟最新選單", "Latest panel restored"},
	"use_buttons":                {"点下方按钮就能继续哦", "點下方按鈕就能繼續", "Use the buttons below to continue"},
	"pending_summary":            {"⏳ 你有 %d 笔订单待支付 · %s", "⏳ 你有 %d 筆訂單待付款 · %s", "⏳ %d pending orders · %s"},
	"continue_pay":               {"继续支付", "繼續付款", "Continue payment"},
	"all":                        {"全部", "全部", "All"},
	"catalog_all":                {"全部商品", "全部商品", "All products"},
	"catalog_page":               {"🛍 %s · 第 %d 页", "🛍 %s · 第 %d 頁", "🛍 %s · page %d"},
	"sold_out":                   {"暂不可购买", "暫不可購買", "Unavailable"},
	"choose_sku":                 {"选一个规格就能下单啦", "選一個規格就能下單", "Choose an option to place your order"},
	"selected":                   {"已选 %s", "已選 %s", "Selected %s"},
	"selected_line":              {"已选：%s × %d", "已選：%s × %d", "Selected: %s × %d"},
	"quantity_selected":          {"数量 %d 件", "數量 %d 件", "Quantity: %d"},
	"quantity_display":           {"%d 件", "%d 件", "%d"},
	"quantity_limit":             {"已达到可购买数量边界", "已達到可購買數量邊界", "Quantity limit reached"},
	"more_quantity":              {"更多数量", "更多數量", "More quantities"},
	"other_quantity":             {"输入其他数量", "輸入其他數量", "Enter another quantity"},
	"back_purchase":              {"返回选购", "返回選購", "Back to purchase"},
	"back_list":                  {"‹ 返回列表", "‹ 返回列表", "‹ Back"},
	"product_details":            {"商品详情", "商品詳情", "Product details"},
	"open_product":               {"前往商城购买", "前往商城購買", "Buy on the website"},
	"payable":                    {"应付：", "應付：", "Total:"},
	"discounted":                 {"已含会员优惠", "已含會員優惠", "Member discount included"},
	"preview_failed":             {"暂时无法报价，请调整规格或数量后重试", "暫時無法報價，請調整規格或數量後重試", "Cannot quote this selection. Adjust the option or quantity and retry"},
	"fulfillment_auto":           {"⚡ 自动发货", "⚡ 自動發貨", "⚡ Auto delivery"},
	"fulfillment_manual":         {"人工发货", "人工發貨", "Manual delivery"},
	"processing":                 {"正在处理，请稍候…", "正在處理，請稍候…", "Processing, please wait…"},
	"order_created_pay":          {"订单已创建，扫码即可完成支付", "訂單已建立，掃碼即可完成付款", "Order created — scan to pay"},
	"order_created":              {"订单已创建", "訂單已建立", "Order created"},
	"pay_before":                 {"请在 %s 前完成支付", "請在 %s 前完成付款", "Pay before %s"},
	"payment_wait":               {"支付完成后我会第一时间通知你，无需重复付款。", "付款完成後我會第一時間通知你，無需重複付款。", "I'll notify you as soon as payment clears. Do not pay twice."},
	"not_paid":                   {"还没收到付款，完成后会自动通知你", "還沒收到付款，完成後會自動通知你", "No payment received yet. We will notify you automatically"},
	"wallet_pay":                 {"余额支付 %s（余额 %s）", "餘額付款 %s（餘額 %s）", "Pay %s from wallet (balance %s)"},
	"change_payment":             {"更换支付方式", "更換付款方式", "Change payment method"},
	"cancel_order":               {"取消订单", "取消訂單", "Cancel order"},
	"cancel_confirm":             {"确认取消", "確認取消", "Confirm cancellation"},
	"keep_order":                 {"再想想", "再想想", "Keep order"},
	"cancel_warning":             {"确定取消这笔订单吗？如果已经扫码付款，请不要取消，稍等片刻即可收到通知。取消后继续使用旧二维码付款不会自动发货，需要联系客服处理。", "確定取消這筆訂單嗎？\n如果已經掃碼付款，請不要取消，稍等片刻即可收到通知。取消後繼續使用舊二維碼付款不會自動發貨，需要聯絡客服處理。", "Cancel this order?\nIf you have already paid, do not cancel; wait for the notification. Paying an old QR code after cancellation will not deliver your order and requires support."},
	"rebuy":                      {"重新购买", "重新購買", "Buy again"},
	"resend_delivery":            {"重新发送交付内容", "重新傳送交付內容", "Resend delivery"},
	"delivery_recent":            {"刚刚已发送，请查看聊天记录", "剛剛已傳送，請查看聊天記錄", "Just sent. Please check your chat"},
	"code_error":                 {"验证码不对，再试一次", "驗證碼不對，再試一次", "Incorrect code. Please try again"},
	"resend_code":                {"重新发送验证码", "重新傳送驗證碼", "Resend code"},
	"status_pending_payment":     {"⏳ 待支付", "⏳ 待付款", "⏳ Awaiting payment"},
	"status_paid":                {"已付款，等待发货", "已付款，等待發貨", "Paid, awaiting delivery"},
	"status_processing":          {"处理中", "處理中", "Processing"},
	"status_delivered":           {"已发货", "已發貨", "Delivered"},
	"status_completed":           {"✅ 已完成", "✅ 已完成", "✅ Completed"},
	"status_cancelled":           {"已取消", "已取消", "Cancelled"},
	"status_canceled":            {"已取消", "已取消", "Cancelled"},
	"status_expired":             {"已过期", "已過期", "Expired"},
	"status_refunded":            {"已退款", "已退款", "Refunded"},
	"status_unknown":             {"订单状态待更新", "訂單狀態待更新", "Status updating"},
	"history_account":            {"原账号余额（只读，不合并）", "原帳號餘額（唯讀，不合併）", "Previous balance (read only, not merged)"},
	"history_refunds":            {"原账号余额退款记录（只读）", "原帳號餘額退款紀錄（唯讀）", "Previous wallet refunds (read only)"},
	"payment_uncertain":          {"支付请求正在处理或结果暂不确定，请先查询订单，不要重复付款。", "付款請求正在處理或結果暫不確定，請先查詢訂單，不要重複付款。", "Payment creation is processing or uncertain. Check the order first; do not pay twice."},
	"welcome":                    {"欢迎光临，请选择服务。", "歡迎光臨，請選擇服務。", "Welcome. Choose a service."},
	"catalog":                    {"🛍 逛商店", "🛍 逛商店", "🛍 Shop"},
	"orders":                     {"📦 我的订单", "📦 我的訂單", "📦 My orders"},
	"account":                    {"👤 账户余额", "👤 帳戶餘額", "👤 Balance"},
	"bind":                       {"绑定已有账号", "綁定已有帳號", "Bind existing account"},
	"history":                    {"绑定前历史订单（只读）", "綁定前歷史訂單（唯讀）", "Previous account orders (read only)"},
	"language":                   {"🌐 语言", "🌐 語言", "🌐 Language"},
	"help":                       {"❓ 帮助", "❓ 說明", "❓ Help"},
	"help_closed":                {"帮助中心已关闭。", "說明中心已關閉。", "Help center is closed."},
	"support":                    {"联系客服", "聯絡客服", "Contact support"},
	"help_text":                  {"选择商品、规格和数量，确认报价后下单。支付后通过订单查询交付内容。/cancel 取消当前操作，/back 返回菜单。绑定不会合并余额；历史订单仅可查询。", "選擇商品、規格和數量，確認報價後下單。付款後透過訂單查詢交付內容。/cancel 取消目前操作，/back 返回選單。綁定不會合併餘額；歷史訂單僅可查詢。", "Select a product, SKU and quantity, then confirm the quote. Check your order after payment for delivery. /cancel cancels this flow; /back returns to the menu. Binding does not merge balances. Previous orders are read-only."},
	"back":                       {"🏠 首页", "🏠 首頁", "🏠 Home"},
	"stale":                      {"按钮已过期，请使用最新菜单或 /start。", "按鈕已過期，請使用最新選單或 /start。", "This button expired. Use the latest menu or /start."},
	"quantity":                   {"请输入购买数量（正整数）。", "請輸入購買數量（正整數）。", "Enter the quantity (positive integer)."},
	"email":                      {"请输入已有账号的邮箱。绑定后原账号订单保留只读访问，余额不会合并。", "請輸入已有帳號的電子郵件。綁定後原帳號訂單保留唯讀存取，餘額不會合併。", "Enter your existing account email. Previous orders remain read-only; balances are not merged."},
	"code":                       {"验证码已发送，请输入邮箱验证码。", "驗證碼已傳送，請輸入電子郵件驗證碼。", "Code sent. Enter the email verification code."},
	"quote":                      {"请确认报价", "請確認報價", "Review quote"},
	"confirm":                    {"✅ 确认下单", "✅ 確認下單", "✅ Place order"},
	"channels":                   {"选择支付方式", "選擇付款方式", "Choose payment method"},
	"pay":                        {"打开收银台", "開啟收銀台", "Open checkout"},
	"refresh":                    {"我已支付，查询结果", "我已付款，查詢結果", "I've paid — check status"},
	"refresh_order":              {"刷新订单", "重新整理訂單", "Refresh order"},
	"expires":                    {"支付截止：", "付款截止：", "Pay by: "},
	"paid":                       {"订单已付款，请查询交付状态。", "訂單已付款，請查詢交付狀態。", "Order paid. Check delivery status."},
	"failed":                     {"出了点小问题，请返回首页再试一次。", "出了點小問題，請返回首頁再試一次。", "Something went wrong. Return home and try again."},
	"price_changed":              {"价格刚刚更新了，请再确认一下。", "價格剛剛更新了，請再確認一下。", "Price just updated — please confirm again."},
	"unsupported":                {"此商品需要填写表单，请前往商城网站购买。", "此商品需要填寫表單，請前往商城網站購買。", "This product requires a form. Please purchase on the store website."},
	"category_current":           {"当前分类：%s", "目前分類：%s", "Current category: %s"},
	"catalog_empty":              {"这个分类暂时没有商品，换个分类看看吧。", "這個分類暫時沒有商品，換個分類看看吧。", "Nothing here yet. Try another category."},
	"section_products":           {"商品", "商品", "Products"},
	"section_products_hint":      {"点下方商品查看详情并下单", "點下方商品查看詳情並下單", "Tap a product below to view and buy"},
	"section_orders":             {"订单", "訂單", "Orders"},
	"section_orders_hint":        {"点下方订单查看详情", "點下方訂單查看詳情", "Tap an order below for details"},
	"orders_empty":               {"这里还没有订单。", "這裡還沒有訂單。", "No orders here yet."},
	"order_no":                   {"订单号 ", "訂單號 ", "Order "},
	"status_label":               {"状态：", "狀態：", "Status: "},
	"amount_label":               {"金额：", "金額：", "Amount: "},
	"created_label":              {"下单时间：", "下單時間：", "Placed: "},
	"go_pay":                     {"去支付", "去付款", "Pay now"},
	"cancel_title":               {"取消订单", "取消訂單", "Cancel order"},
	"account_id":                 {"账户 #%d", "帳戶 #%d", "Account #%d"},
	"email_label":                {"邮箱：", "電子郵件：", "Email: "},
	"balance_label":              {"余额：", "餘額：", "Balance: "},
	"page_prev":                  {"‹ 上一页", "‹ 上一頁", "‹ Prev"},
	"page_next":                  {"下一页 ›", "下一頁 ›", "Next ›"},
}
