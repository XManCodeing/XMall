/** Keep custom brands intact; retire only the original default brand. */
export function resolveSiteName(value: unknown, fallback: string): string {
  const name = typeof value === 'string' ? value.trim() : ''
  return name && name.toLowerCase() !== 'dujiao-next' ? name : fallback
}
