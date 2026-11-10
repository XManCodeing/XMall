import i18n from '@/i18n'
import type { ApiResponse } from './types'
import { notifyError } from '@/utils/notify'
import { adminUrl } from '@/utils/adminBase'
import { productSaveMessages } from '@/i18n/productSave'
export type { ApiResponse }
export type RequestErrorKind = 'timeout' | 'network' | 'business' | 'http' | 'response'
export interface RequestError extends Error {
  __notified: boolean
  kind: RequestErrorKind
  resultUnknown: boolean
  response?: { status: number; data: any }
}
interface RequestOptions {
  params?: Record<string, any>
  headers?: Record<string, string>
  blob?: boolean
  timeoutMs?: number
  /** Suppress error toasts for background reads; authentication handling is unchanged. */
  silent?: boolean
  data?: any
  [key: string]: any
}
const t = (key: string, params?: Record<string, unknown>) =>
  (params ? i18n.global.t(key, params) : i18n.global.t(key)) as string
const baseURL = `${import.meta.env.VITE_API_BASE_URL || ''}/api/v1`
/** @returns Current application locale string. */
function getLocale(): string { return (i18n.global.locale as any).value || i18n.global.locale || '' }
/** @param key Shared recovery message key. @returns Localized network outcome text. */
function requestText(key: 'timeout' | 'unknown' | 'invalidResponse'): string {
  return (productSaveMessages[getLocale() as keyof typeof productSaveMessages] || productSaveMessages['en-US'])[key]
}
/** @returns Nothing. Clear expired authentication and navigate to the existing login route. */
function redirectToLogin(): void {
  localStorage.removeItem('admin_token')
  window.location.href = adminUrl('/login')
}
/** @param status HTTP status. @returns Existing localized fallback. */
function getHttpErrorMessage(status: number): string {
  const keys: Record<number, string> = { 401: 'unauthorized', 403: 'forbidden', 404: 'notFound', 500: 'serverError', 502: 'badGateway', 503: 'serviceUnavailable' }
  return keys[status] ? t(`common.api.${keys[status]}`) : t('common.api.requestFailedStatus', { status })
}
/** @param path API path. @param params Query values. @returns Encoded URL preserving prior query behavior. */
function buildUrl(path: string, params?: Record<string, any>): string {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params || {})) {
    if (value !== undefined && value !== null) query.append(key, String(value))
  }
  return `${baseURL}${path}${query.size ? `?${query}` : ''}`
}
/** @param input Error metadata. @returns Typed, already-notified error without erasing server messages. */
function failure(input: { message: string; kind: RequestErrorKind; unknown: boolean; status?: number; data?: any; silent?: boolean }): RequestError {
  const error = Object.assign(new Error(input.message), {
    __notified: true as const, kind: input.kind, resultUnknown: input.unknown,
    ...(input.status === undefined ? {} : { response: { status: input.status, data: input.data } }),
  })
  if (!input.silent) notifyError(input.unknown ? `${input.message} ${requestText('unknown')}` : input.message)
  return error
}
/** @param value Any caught value. @returns Whether a write may have committed without reliable acknowledgement. */
export function isResultUnknown(value: unknown): boolean { return (value as Partial<RequestError> | null)?.resultUnknown === true }
/** @param data Parsed error payload. @param fallback Localized fallback. @returns Real business message when present. */
function messageOf(data: any, fallback: string): string {
  return typeof data?.msg === 'string' && data.msg ? data.msg : typeof data?.message === 'string' && data.message ? data.message : fallback
}
/** @param headers Response headers. @returns Existing flat header map for downloads. */
function headerMap(headers: Headers): Record<string, string> {
  const result: Record<string, string> = {}
  headers.forEach((value, key) => { result[key] = value })
  return result
}
/**
 * Decode response bodies while the caller's timeout remains active; plain JSON and envelopes remain supported.
 * @param response Fetch response. @param options Existing blob options. @param write Whether request can mutate state.
 * @param path API path for login redirect policy. @param signal Shared deadline signal. @returns Existing response wrapper.
 */
async function decode(response: Response, options: RequestOptions, write: boolean, path: string, signal: AbortSignal): Promise<{ data: any; headers?: Record<string, string> }> {
  let data: any
  const jsonBlob = options.blob && (response.headers.get('content-type') || '').includes('application/json')
  if (options.blob && response.ok && !jsonBlob) return { data: await response.blob(), headers: headerMap(response.headers) }
  try { data = await response.json(); signal.throwIfAborted() }
  catch (error) {
    signal.throwIfAborted()
    if (!(error instanceof SyntaxError)) throw error
    if (response.status === 401 && !/\/admin\/login\b/.test(path)) redirectToLogin()
    throw failure({ silent: !write && options.silent, message: response.ok ? requestText('invalidResponse') : getHttpErrorMessage(response.status),
      kind: response.ok ? 'response' : 'http', unknown: write && (response.ok || response.status >= 500), status: response.status })
  }
  const businessStatus = data && typeof data === 'object' ? data.status_code : undefined
  if (!response.ok || (businessStatus !== undefined && businessStatus !== 0) || jsonBlob) {
    const status = !response.ok ? response.status : businessStatus || response.status
    if (status === 401 && !/\/admin\/login\b/.test(path)) redirectToLogin()
    throw failure({ silent: !write && options.silent, message: messageOf(data, response.ok ? t('common.api.requestFailed') : getHttpErrorMessage(status)),
      kind: response.ok ? 'business' : 'http', unknown: write && (response.status >= 500 || (businessStatus >= 500 && businessStatus <= 599)), status, data })
  }
  if (data === null || typeof data !== 'object') {
    throw failure({ silent: !write && options.silent, message: requestText('invalidResponse'), kind: 'response', unknown: write, status: response.status, data })
  }
  return { data }
}
/**
 * Execute one request; timeout covers headers and body. Never retries writes with unknown outcomes.
 * @param method HTTP verb. @param path API path. @param bodyOrOptions Body or GET/DELETE options.
 * @param options Write options. @returns Existing data/header response wrapper.
 */
async function request(method: string, path: string, bodyOrOptions?: any, options?: RequestOptions): Promise<{ data: any; headers?: Record<string, string> }> {
  const readOptions = method === 'GET' || method === 'DELETE'
  const opts: RequestOptions = (readOptions ? bodyOrOptions : options) || {}
  const body = readOptions ? opts.data : bodyOrOptions
  const write = method !== 'GET'
  const headers: Record<string, string> = { ...opts.headers }
  const locale = getLocale(), token = localStorage.getItem('admin_token')
  if (locale) headers['X-Lang'] = locale
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined && !(body instanceof FormData)) headers['Content-Type'] = 'application/json'
  // Serialization happens before dispatch: a local validation/encoding error cannot have committed.
  const serialized = body instanceof FormData ? body : body !== undefined ? JSON.stringify(body) : undefined
  const controller = new AbortController()
  const timeoutMs = typeof opts.timeoutMs === 'number' && Number.isFinite(opts.timeoutMs) ? Math.min(30000, Math.max(1000, opts.timeoutMs)) : 10000
  let response: Response | undefined
  let timedOut = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => { timedOut = true; controller.abort(); reject(new DOMException('Request timed out', 'AbortError')) }, timeoutMs)
  })
  try {
    const operation = async () => {
      response = await fetch(buildUrl(path, opts.params), { method, headers, body: serialized, signal: controller.signal })
      return decode(response, opts, write, path, controller.signal)
    }
    return await Promise.race([operation(), deadline])
  } catch (error) {
    if ((error as Partial<RequestError>)?.__notified) throw error
    const timeout = timedOut || (error as Error)?.name === 'AbortError'
    const definiteRejection = response && response.status >= 400 && response.status < 500
    throw failure({ silent: !write && opts.silent, message: definiteRejection ? getHttpErrorMessage(response!.status) : timeout ? requestText('timeout') : t('common.api.networkError'),
      kind: definiteRejection ? 'http' : timeout ? 'timeout' : 'network', unknown: write && !definiteRejection, status: response?.status })
  } finally { clearTimeout(timer) }
}
export const api = {
  get: (path: string, options?: RequestOptions) => request('GET', path, options),
  post: (path: string, body?: any, options?: RequestOptions) => request('POST', path, body, options),
  put: (path: string, body?: any, options?: RequestOptions) => request('PUT', path, body, options),
  patch: (path: string, body?: any, options?: RequestOptions) => request('PATCH', path, body, options),
  delete: (path: string, options?: RequestOptions) => request('DELETE', path, options),
}
