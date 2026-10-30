import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { notifyErrorOnce } from '@/utils/notify'

export interface UseCrudModalOptions {
  /** 关闭弹窗后刷新数据 */
  onSuccess?: () => void
}

export function useCrudModal(options: UseCrudModalOptions = {}) {
  const { t } = useI18n()
  const showModal = ref(false)
  const isEditing = ref(false)
  const editingId = ref<number | null>(null)
  const submitting = ref(false)
  const error = ref('')

  const openCreate = () => {
    isEditing.value = false
    editingId.value = null
    error.value = ''
    showModal.value = true
  }

  const openEdit = (id: number) => {
    isEditing.value = true
    editingId.value = id
    error.value = ''
    showModal.value = true
  }

  const closeModal = () => {
    showModal.value = false
    error.value = ''
    editingId.value = null
  }

  /**
   * 包装提交函数，自动管理 submitting 和 error 状态
   */
  const handleSubmit = async (submitFn: () => Promise<void>) => {
    error.value = ''
    submitting.value = true
    try {
      await submitFn()
      closeModal()
      options.onSuccess?.()
    } catch (err: any) {
      notifyErrorOnce(err, err?.response?.data?.msg || err?.message || t('common.api.requestFailed'))
    } finally {
      submitting.value = false
    }
  }

  return {
    showModal,
    isEditing,
    editingId,
    submitting,
    error,
    openCreate,
    openEdit,
    closeModal,
    handleSubmit,
  }
}
