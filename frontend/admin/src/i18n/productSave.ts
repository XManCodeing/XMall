/** Shared request and product-save recovery messages; registered with existing locale modules. */
export const productSaveMessages = {
  'zh-CN': {
    timeout: '请求超时', invalidResponse: '响应内容无效',
    unknown: '保存结果未知，商品可能已保存。草稿已保留，已暂停重复提交，请只读核对。',
    memory: '草稿和提交保护仅保留在当前页面内；刷新或离开页面会丢失，请勿据此重复提交。',
    check: '只读核对保存结果', checking: '正在核对', notFound: '未找到匹配商品。不能确认保存失败，请继续核对，不要重复创建。',
    found: '找到可能已保存的商品，不代表本次提交成功。', open: '在新标签页编辑现有商品',
    confirm: '将在新标签页打开现有商品，当前草稿保持不变。此商品不一定由本次提交保存，确认继续？',
    close: '关闭后草稿仍保留，再次打开将恢复此草稿；不要刷新页面。确认关闭？', permission: '无读取权限，无法核对。请联系有权限的管理员核对。',
    emptySlug: '缺少精确商品标识，无法核对，请联系管理员。', popup: '未能打开新标签页，请允许弹出窗口后重试。',
  },
  'zh-TW': {
    timeout: '請求逾時', invalidResponse: '回應內容無效',
    unknown: '儲存結果未知，商品可能已儲存。草稿已保留，已暫停重複提交，請唯讀核對。',
    memory: '草稿與提交保護僅保留於目前頁面；重新整理或離開頁面會遺失，請勿據此重複提交。',
    check: '唯讀核對儲存結果', checking: '正在核對', notFound: '未找到相符商品。無法確認儲存失敗，請繼續核對，勿重複建立。',
    found: '找到可能已儲存的商品，不代表本次提交成功。', open: '在新分頁編輯現有商品',
    confirm: '將在新分頁開啟現有商品，目前草稿保持不變。此商品不一定由本次提交儲存，確定繼續？',
    close: '關閉後草稿仍保留，再次開啟將恢復此草稿；請勿重新整理頁面。確定關閉？', permission: '無讀取權限，無法核對。請聯絡有權限的管理員核對。',
    emptySlug: '缺少精確商品識別，無法核對，請聯絡管理員。', popup: '無法開啟新分頁，請允許彈出視窗後重試。',
  },
  'en-US': {
    timeout: 'Request timed out', invalidResponse: 'Invalid response content',
    unknown: 'Save outcome unknown; the product may have been saved. Your draft is preserved and repeat submission is paused. Verify using read-only checks.',
    memory: 'Draft and submission protection exist only in this page. Reloading or leaving loses them; do not resubmit on that basis.',
    check: 'Check saved result (read-only)', checking: 'Checking', notFound: 'No matching product found. This does not prove failure. Keep checking; do not create again.',
    found: 'Found a possibly saved product. This does not confirm this submission succeeded.', open: 'Edit existing product in a new tab',
    confirm: 'Open the existing product in a new tab, keeping this draft unchanged? This product may not originate from this submission.',
    close: 'The draft will remain and reopen next time. Do not reload this page. Close now?', permission: 'Read access is required. Ask an authorized administrator to verify.',
    emptySlug: 'No exact product identifier available. Contact an administrator to verify.', popup: 'Could not open a new tab. Allow pop-ups and try again.',
  },
}
