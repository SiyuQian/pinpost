/**
 * Deterministic avatar tint. Six hues spread around the wheel, picked by a hash
 * of the member's name, so the same person keeps the same colour across the
 * sidebar, the topbar stack and every row they own — the point is recognition,
 * not decoration. Only the hue is returned; the lightness ramps live in the
 * `.avatar-tint` rules, which have to flip with the theme.
 */
export const HUES = [274, 162, 62, 20, 210, 320]

export function avatarTint(seed: string): React.CSSProperties {
  let hash = 0
  for (const char of seed) hash = (hash * 31 + char.codePointAt(0)!) >>> 0
  return { "--tint": HUES[hash % HUES.length] } as React.CSSProperties
}
