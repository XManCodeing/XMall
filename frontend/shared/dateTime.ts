/** Business timestamps always use UTC+08:00, independent of browser/OS settings. */
export const BUSINESS_TIME_ZONE = 'Etc/GMT-8'
type DateInput = string | number | Date | null | undefined
const dateTimeFormatter = new Intl.DateTimeFormat('en-GB', {
  timeZone: BUSINESS_TIME_ZONE, year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})

/** Parse an instant; timezone-less calendar input belongs to UTC+08:00.
 * @param raw ISO timestamp, date-only calendar value, Date, or epoch milliseconds.
 * @returns Valid date without changing its instant, or undefined for invalid input.
 */
export function parseBusinessDate(raw: DateInput): Date | undefined {
  if (raw === null || raw === undefined || raw === '') return undefined
  let value: string | number | Date = raw
  if (typeof raw === 'string') {
    const text = raw.trim()
    const calendar = text.match(/^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2})(\.\d{1,9})?)?)?(Z|[+-]\d{2}:?\d{2})?$/i)
    if (!calendar) return undefined
    const [, year, month, day, hour = '00', minute = '00', second = '00', fraction = '', zone] = calendar
    const calendarCheck = new Date(`${year}-${month}-${day}T00:00:00Z`)
    if (!Number.isFinite(calendarCheck.getTime()) || calendarCheck.toISOString().slice(0, 10) !== `${year}-${month}-${day}`
      || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) return undefined
    value = `${year}-${month}-${day}T${hour}:${minute}:${second}${fraction}${zone || '+08:00'}`
  }
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date : undefined
}

/** Format a business instant exactly to seconds.
 * @param raw Timestamp or calendar value. @returns YYYY-MM-DD HH:mm:ss, or an em dash.
 */
export function formatDate(raw: DateInput): string {
  const date = parseBusinessDate(raw)
  if (!date) return '—'
  const parts = Object.fromEntries(dateTimeFormatter.formatToParts(date).map(part => [part.type, part.value]))
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`
}

/** Get a UTC+08 calendar key without shifting date-only values.
 * @param raw Timestamp (defaults to now). @returns YYYY-MM-DD or an empty string.
 */
export function businessDateKey(raw: DateInput = new Date()): string {
  const formatted = formatDate(raw)
  return formatted === '—' ? '' : formatted.slice(0, 10)
}

/** Populate a datetime-local control using business wall-clock time.
 * @param raw Timestamp. @returns YYYY-MM-DDTHH:mm or an empty string.
 */
export function toLocalInput(raw: DateInput): string {
  const formatted = formatDate(raw)
  return formatted === '—' ? '' : formatted.slice(0, 16).replace(' ', 'T')
}

/** Serialize UTC+08 wall-clock input while preserving explicitly zoned instants.
 * @param raw Timestamp or datetime-local value. @returns UTC RFC3339, or undefined.
 */
export function toRFC3339(raw: DateInput): string | undefined {
  return parseBusinessDate(raw)?.toISOString()
}

/** Serialize optional scheduling fields which use empty strings to clear a value.
 * @param raw Timestamp or datetime-local value. @returns UTC RFC3339, or empty string.
 */
export function toISO(raw: DateInput): string {
  return toRFC3339(raw) || ''
}
