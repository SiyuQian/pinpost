import { describe, expect, it } from "vitest"
import { avatarTint, HUES } from "@/lib/avatar"

const hue = (seed: string) => Number((avatarTint(seed) as Record<string, unknown>)["--tint"])

/**
 * Three call sites (row assignee, topbar stack, sidebar footer) rely on one
 * seed producing one hue everywhere — a colour that differs between them
 * degrades silently, with no type error to catch it.
 */
describe("avatarTint", () => {
  it("gives the same seed the same hue every time", () => {
    expect(hue("张三")).toBe(hue("张三"))
    expect(hue("ada@example.com")).toBe(hue("ada@example.com"))
  })

  it("separates different people", () => {
    expect(hue("张三")).not.toBe(hue("李四"))
  })

  it("always lands on a defined hue", () => {
    // An "improved" hash that drops the >>> 0 returns a negative index, and
    // HUES[-2] is undefined — which reaches the DOM as an invalid --tint.
    for (const seed of ["", "李四", "Z", "😀", "a".repeat(200), "user+tag@example.com"]) {
      expect(HUES).toContain(hue(seed))
    }
  })
})
