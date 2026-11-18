import { describe, expect, it } from "vitest"
import { describeSource, displayWidth } from "@/lib/format"

/**
 * The table's tooltip gate is `displayWidth(message, 46) > 46`, so what matters
 * is the estimate's direction either side of that boundary: over-reporting
 * shows a redundant tooltip, under-reporting hides a needed one.
 */
describe("displayWidth", () => {
  it("counts latin characters as one column each", () => {
    expect(displayWidth("abc")).toBe(3)
    expect(displayWidth("")).toBe(0)
  })

  it("counts full-width CJK as two", () => {
    expect(displayWidth("中文")).toBe(4)
    expect(displayWidth("ａｂ")).toBe(4) // fullwidth latin
    expect(displayWidth("a中")).toBe(3)
  })

  it("counts astral wide code points as two rather than as surrogate pairs", () => {
    expect(displayWidth("𠀀")).toBe(2) // CJK Ext-B, U+20000
    expect(displayWidth("😀")).toBe(2)
  })

  it("stops once the limit is passed, and agrees with the unbounded form there", () => {
    const clamp = 46
    const under = "中".repeat(23) // exactly 46 — fits
    const over = "中".repeat(24) // 48 — clips

    expect(displayWidth(under, clamp) > clamp).toBe(false)
    expect(displayWidth(over, clamp) > clamp).toBe(true)
    expect(displayWidth(under, clamp)).toBe(displayWidth(under))
  })

  it("does not scan past the limit", () => {
    // 10000 characters is the ingest cap; the bounded call must settle early.
    const huge = "中".repeat(10_000)
    expect(displayWidth(huge, 46)).toBeLessThanOrEqual(48)
    expect(displayWidth(huge)).toBe(20_000)
  })
})

describe("describeSource", () => {
  const CHROME_MAC =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
  const SAFARI_IOS =
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
  const EDGE_WIN =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 Edg/128.0.0.0"

  it("names the page, the browser and the platform", () => {
    expect(describeSource({ url: "https://example.com/pricing", user_agent: CHROME_MAC })).toEqual([
      "example.com/pricing",
      "Chrome 128",
      "macOS",
    ])
  })

  it("does not mistake Safari and Edge for Chrome", () => {
    // Both user agents carry a Safari token, and Edge also claims Chrome.
    expect(describeSource({ user_agent: SAFARI_IOS })).toEqual(["Safari 17", "iOS"])
    expect(describeSource({ user_agent: EDGE_WIN })).toEqual(["Edge 128", "Windows"])
  })

  it("drops the path for a bare origin", () => {
    expect(describeSource({ url: "https://example.com/" })).toEqual(["example.com"])
  })

  it("says nothing when there is nothing to say", () => {
    expect(describeSource({})).toEqual([])
    expect(describeSource({ url: null, user_agent: null })).toEqual([])
    // An unparseable url is still worth showing; an unknown UA is not.
    expect(describeSource({ url: "app://checkout", user_agent: "curl/8.4" })).toEqual(["app://checkout"])
  })
})
