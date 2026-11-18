import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"
import { SCAN_LABEL, STATUS_LABEL, STATUS_TRANSITIONS, STATUSES } from "./labels"

// Guard: the client tables must match the server's sources of truth.
const root = new URL("../../../../", import.meta.url)
const read = (p: string) => readFileSync(new URL(p, root), "utf8")

const list = (s: string) => s.split(",").map((v) => v.trim()).filter(Boolean).sort()

function enumAfter(src: string, re: RegExp): string[] {
  const m = src.match(re)
  const out = m ? list(m[1]) : []
  expect(out.length, `no match for ${re}`).toBeGreaterThan(0)
  return out
}

function goTransitions(src: string): Record<string, string[]> {
  const m = src.match(/var statusTransitions = map\[string\]\[\]string\{\n([\s\S]*?)\n\}/)
  expect(m, "statusTransitions not found").not.toBeNull()
  const out: Record<string, string[]> = {}
  for (const line of m![1].split("\n")) {
    const e = line.match(/"(\w+)":\s*\{([^}]*)\}/)
    if (e) out[e[1]] = list(e[2].replaceAll('"', ""))
  }
  expect(Object.keys(out).length).toBeGreaterThan(0)
  return out
}

describe("client tables match the server", () => {
  const openapi = read("api/openapi.yaml")

  it("feedback statuses", () => {
    const want = enumAfter(openapi, /FeedbackStatus:\n\s+type: string\n\s+enum: \[([^\]]+)\]/)
    expect(Object.keys(STATUS_LABEL).sort()).toEqual(want)
    expect([...STATUSES].sort()).toEqual(want)
  })

  it("attachment scan statuses", () => {
    const want = enumAfter(openapi, /scan_status: \{ type: string, enum: \[([^\]]+)\] \}/)
    expect(Object.keys(SCAN_LABEL).sort()).toEqual(want)
  })

  it("status transitions", () => {
    const want = goTransitions(read("internal/feedback/feedback.go"))
    const got = Object.fromEntries(
      Object.entries(STATUS_TRANSITIONS).map(([k, v]) => [k, [...v].sort()]),
    )
    expect(got).toEqual(want)
  })
})
