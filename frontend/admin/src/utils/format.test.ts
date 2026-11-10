import { describe, expect, it } from 'vitest'
import { formatDate, toISO, toLocalInput, toRFC3339, businessDateKey } from './format'
import { formatResellerOperationsPeriod } from './resellerOperations'

describe('UTC+08 business dates independent of host timezone', () => {
  it('renders equivalent instants identically to seconds across midnight/year boundaries', () => {
    for (const value of ['2026-12-31T16:01:02Z', '2027-01-01T00:01:02+08:00', '2026-12-31T08:01:02-08:00']) {
      expect(formatDate(value)).toBe('2027-01-01 00:01:02')
    }
    expect(formatDate('2026-01-01T16:00:00Z')).toBe('2026-01-02 00:00:00')
    expect(formatDate('1988-07-01T00:00:00Z')).toBe('1988-07-01 08:00:00')
  })
  it('treats datetime-local inputs as UTC+08 even during browser DST transitions', () => {
    expect(toRFC3339('2026-03-08T02:30')).toBe('2026-03-07T18:30:00.000Z')
    expect(toRFC3339('2026-11-01T01:30:45')).toBe('2026-10-31T17:30:45.000Z')
    expect(toRFC3339('2026-01-01T00:00:00Z')).toBe('2026-01-01T00:00:00.000Z')
    expect(toLocalInput('2026-12-31T16:01:02Z')).toBe('2027-01-01T00:01')
    expect(toISO(toLocalInput('2026-12-31T16:01:00Z'))).toBe('2026-12-31T16:01:00.000Z')
  })
  it('keeps date-only semantics and uses UTC+08 daily boundaries', () => {
    expect(businessDateKey('1990-01-01')).toBe('1990-01-01')
    expect(businessDateKey('2026-01-01T15:59:59Z')).toBe('2026-01-01')
    expect(businessDateKey('2026-01-01T16:00:00Z')).toBe('2026-01-02')
    expect(formatResellerOperationsPeriod('2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z'))
      .toBe('2026-01-01 08:00:00 - 2026-01-02 08:00:00')
  })
  it('rejects empty, malformed, overflowing calendar and non-finite values', () => {
    for (const value of [undefined, null, '', 'bad', '2026-02-30T00:00', '2026-01-01T24:00', NaN]) {
      expect(formatDate(value)).toBe('—')
      expect(toRFC3339(value)).toBeUndefined()
      expect(toLocalInput(value)).toBe('')
      expect(toISO(value)).toBe('')
    }
  })
})
