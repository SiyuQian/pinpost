const dateTime = new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" })

/** Absolute timestamp, for tooltips and detail fields. */
export function formatDateTime(iso: string) {
  return dateTime.format(new Date(iso))
}

/** Compact relative time for list rows; falls back to a date past a week. */
export function formatRelative(iso: string) {
  const then = new Date(iso).getTime()
  const seconds = Math.round((Date.now() - then) / 1000)
  if (seconds < 60) return "刚刚"
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.round(hours / 24)
  if (days === 1) return "昨天"
  if (days < 7) return `${days} 天前`
  return new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric" }).format(then)
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/** Hours since a timestamp — used for the "超 24h 未响应" SLA tile. */
export function hoursSince(iso: string) {
  return (Date.now() - new Date(iso).getTime()) / 3_600_000
}

/**
 * Human-readable "where this came from", from the two fields a list row
 * carries. Returned as parts so a row can join them with its own separator and
 * drop what it has no room for; an unrecognised user agent contributes nothing
 * rather than a guess.
 */
export function describeSource(source: { url?: string | null; user_agent?: string | null }) {
  const parts: string[] = []

  if (source.url) {
    try {
      const parsed = new URL(source.url)
      // Only web pages get shortened to host + path. A custom scheme (a native
      // app's deep link) means the whole string, or "checkout" is all that
      // survives of "app://checkout".
      if (parsed.protocol === "http:" || parsed.protocol === "https:") {
        parts.push(parsed.host + (parsed.pathname === "/" ? "" : parsed.pathname))
      } else {
        parts.push(source.url.slice(0, 60))
      }
    } catch {
      parts.push(source.url.slice(0, 60))
    }
  }

  const ua = source.user_agent ?? ""
  if (ua) {
    // Order matters: Edge and most in-app browsers also claim "Chrome", and
    // Safari's own token trails every WebKit user agent.
    const browsers: [RegExp, string][] = [
      [/Edg\/(\d+)/, "Edge"],
      [/Firefox\/(\d+)/, "Firefox"],
      [/Chrome\/(\d+)/, "Chrome"],
      [/Version\/(\d+).*Safari/, "Safari"],
    ]
    for (const [pattern, name] of browsers) {
      const match = pattern.exec(ua)
      if (match) {
        parts.push(`${name} ${match[1]}`)
        break
      }
    }

    const platforms: [RegExp, string][] = [
      [/Windows NT/, "Windows"],
      [/iPhone|iPad/, "iOS"],
      [/Mac OS X/, "macOS"],
      [/Android/, "Android"],
      [/Linux/, "Linux"],
    ]
    for (const [pattern, name] of platforms) {
      if (pattern.test(ua)) {
        parts.push(name)
        break
      }
    }
  }

  return parts
}

export function initials(nameOrEmail: string) {
  const trimmed = nameOrEmail.trim()
  if (!trimmed) return "?"
  // CJK names read best as the last character; latin ones as the first letter.
  if (/[一-龥]/.test(trimmed)) return trimmed.slice(-1)
  return trimmed[0]!.toUpperCase()
}

/**
 * East Asian Wide and Fullwidth code points, which render at roughly twice the
 * advance width of a Latin character. The `u` flag is load-bearing: without it
 * the astral ranges below cannot be expressed, and a surrogate pair would fall
 * through as two narrow units.
 */
const WIDE =
  /[\u1100-\u115F\u2E80-\u303E\u3041-\u33FF\u3400-\u4DBF\u4E00-\u9FFF\uA000-\uA4CF\uAC00-\uD7A3\uF900-\uFAFF\uFE10-\uFE19\uFE30-\uFE6F\uFF00-\uFF60\uFFE0-\uFFE6\u{1F000}-\u{1FAFF}\u{20000}-\u{3FFFD}]/u

/**
 * Approximate rendered width in `ch` units — a full-width glyph counts as two.
 * Used to decide whether text will overflow a `ch`-based clamp, which a plain
 * `.length` gets wrong in either direction: 30 Chinese characters overflow a
 * 46ch box that 40 Latin ones fit inside. An estimate rather than a measurement
 * (`ch` is the advance of "0", and Latin glyphs average narrower than that), so
 * it errs toward reporting overflow — showing a redundant tooltip is a cheaper
 * mistake than withholding a needed one.
 *
 * Pass `limit` when the caller only needs to know whether the text is wider
 * than some bound: messages run to 10000 characters and this is called per row
 * per render, so scanning past the answer is wasted work. The return value is
 * then only accurate up to `limit`.
 */
export function displayWidth(text: string, limit = Infinity) {
  let width = 0
  for (const char of text) {
    width += WIDE.test(char) ? 2 : 1
    if (width > limit) break
  }
  return width
}
