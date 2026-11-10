export const notificationPrerequisiteMessages = {
  'zh-CN': {
    email: '请先在站点设置的邮件配置中启用 SMTP，并完整配置服务器、端口和发件邮箱。',
    telegram: '请先在站点设置 → 系统设置 → Telegram能力相关配置中保存 Bot Token，无需开启 Telegram 登录或配置 Mini App。',
    feishu: '请先填写飞书 App ID 和 App Secret。已保存的密钥无需重复填写。',
    verify: '配置齐全不代表可送达，请使用下方测试发送确认。Telegram 私聊接收人需先向该机器人发送 /start。',
    loadFailed: '无法检查通知前置配置，请稍后重试。',
  },
  'zh-TW': {
    email: '請先在站點設定的郵件設定中啟用 SMTP，並完整設定伺服器、連接埠和寄件信箱。',
    telegram: '請先在站點設定 → 系統設定 → Telegram能力相關設定中儲存 Bot Token，無需啟用 Telegram 登入或設定 Mini App。',
    feishu: '請先填寫飛書 App ID 和 App Secret。已儲存的密鑰無需重複填寫。',
    verify: '設定完整不代表可送達，請使用下方測試發送確認。Telegram 私聊接收人需先向該機器人發送 /start。',
    loadFailed: '無法檢查通知前置設定，請稍後重試。',
  },
  'en-US': {
    email: 'Enable SMTP and configure the server, port and sender address in Site Settings → Email settings first.',
    telegram: 'Save a Bot Token in Site Settings → System Settings → Telegram settings first. Telegram login and a Mini App URL are not required.',
    feishu: 'Enter the Feishu App ID and App Secret first. A saved secret does not need to be entered again.',
    verify: 'Complete settings do not guarantee delivery. Use the test send below to verify. Telegram private recipients must first send /start to the bot.',
    loadFailed: 'Unable to check notification prerequisites. Please try again later.',
  },
}
