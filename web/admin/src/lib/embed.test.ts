import { describe, expect, it } from "vitest"
import { PLATFORMS, installCommand, snippet } from "@/lib/embed"

const cfg = { writeKey: 'wk_a"b', apiUrl: "https://pinpost.example.com", environment: "production" }

describe("embed snippets", () => {
  it.each(PLATFORMS.map((p) => p.key))("%s carries key, origin and environment, escaped", (platform) => {
    const code = snippet(platform, cfg)
    // JSON-quoted so a quote in a value cannot break out of the string literal.
    expect(code).toContain('"wk_a\\"b"')
    expect(code).toContain('"https://pinpost.example.com"')
    expect(code).toContain('"production"')
    expect(code).not.toContain("localhost")
  })

  it("installs the package the snippet imports from", () => {
    for (const p of PLATFORMS) {
      expect(installCommand(p.key)).toBe(`npm install ${p.pkg}`)
      expect(snippet(p.key, cfg)).toContain(`from "${p.pkg}"`)
    }
  })
})
