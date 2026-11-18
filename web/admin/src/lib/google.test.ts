// @vitest-environment jsdom
import { afterEach, beforeEach, expect, test, vi } from "vitest"

// The loader memoizes in module scope, so every case needs a fresh module.
beforeEach(() => vi.resetModules())
afterEach(() => {
  document.head.querySelectorAll("script").forEach((s) => s.remove())
  delete (window as { google?: unknown }).google
})

function tag() {
  const el = document.head.querySelector("script")
  if (!el) throw new Error("no script tag was injected")
  return el
}

const identity = { accounts: { id: { initialize() {}, renderButton() {} } } }

// React strict mode runs the effect twice; two <script> tags would make Google
// initialize twice and the button render into a detached node.
test("reuses one script tag across concurrent callers", async () => {
  const { loadGoogleIdentity } = await import("./google")
  const both = Promise.all([loadGoogleIdentity(), loadGoogleIdentity()])

  expect(document.head.querySelectorAll("script")).toHaveLength(1)
  Object.assign(window, { google: identity })
  tag().onload?.(new Event("load"))

  expect(await both).toEqual([identity, identity])
})

// A cached rejected promise would disable sign-in until a full page reload.
test("a failed load can be retried", async () => {
  const { loadGoogleIdentity } = await import("./google")
  const first = loadGoogleIdentity()
  tag().onerror?.(new Event("error"))
  await expect(first).rejects.toThrow()

  document.head.querySelectorAll("script").forEach((s) => s.remove())
  const second = loadGoogleIdentity()
  Object.assign(window, { google: identity })
  tag().onload?.(new Event("load"))
  expect(await second).toEqual(identity)
})

test("skips the network entirely once the script is present", async () => {
  Object.assign(window, { google: identity })
  const { loadGoogleIdentity } = await import("./google")

  expect(await loadGoogleIdentity()).toEqual(identity)
  expect(document.head.querySelectorAll("script")).toHaveLength(0)
})

// Google only accepts 200-400. The login column is capped at 360px but shrinks
// with the viewport, and an unlaid-out slot measures 0 — both have to land
// inside the range or the button renders wrong or not at all.
test("clamps the button width to the range Google accepts", async () => {
  const { googleButtonWidth } = await import("./google")

  expect(googleButtonWidth(0)).toBe(200)
  expect(googleButtonWidth(327)).toBe(327)
  expect(googleButtonWidth(900)).toBe(400)
})
