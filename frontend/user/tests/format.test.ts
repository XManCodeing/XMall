import assert from 'node:assert/strict'
import test from 'node:test'
import { formatDate, businessDateKey } from '../src/utils/format.ts'
import { formatResellerConsoleDate } from '../src/utils/resellerConsole.ts'

test('all storefront timestamps render in fixed UTC+08 with seconds', () => {
  for (const input of ['2026-12-31T16:01:02Z', '2027-01-01T00:01:02+08:00', '2026-12-31T08:01:02-08:00']) {
    assert.equal(formatDate(input), '2027-01-01 00:01:02')
    assert.equal(formatResellerConsoleDate(input), '2027-01-01 00:01:02')
  }
  assert.equal(formatDate('1988-07-01T00:00:00Z'), '1988-07-01 08:00:00')
  assert.equal(formatDate('2026-03-08T02:30:00'), '2026-03-08 02:30:00')
})

test('announcement daily grouping follows UTC+08 and calendar-only values stay unchanged', () => {
  assert.equal(businessDateKey('2026-01-01T15:59:59Z'), '2026-01-01')
  assert.equal(businessDateKey('2026-01-01T16:00:00Z'), '2026-01-02')
  assert.equal(businessDateKey('1990-01-01'), '1990-01-01')
  assert.equal(formatDate('2026-01-01T16:00:00Z'), '2026-01-02 00:00:00')
})

test('invalid and absent timestamps never leak raw values or Invalid Date', () => {
  for (const input of ['', null, undefined, 'invalid', '2026-02-30T08:00:00Z', NaN]) {
    assert.equal(formatDate(input), '—')
  }
})
