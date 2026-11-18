// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import type { Project } from "@/lib/api"
import { EmbedPage } from "@/components/settings/embed-page"

/**
 * The page's one dangerous control is rotation: it locks out every SDK holding
 * the old key. So it must sit behind a confirmation, be hidden from roles the
 * API refuses, and — since the listing can lack a key on old projects — the
 * no-key state must offer a way out rather than an empty box.
 */
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

vi.mock("@/lib/api", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api")>()
  return { ...actual, api: { overview: vi.fn(), rotateWriteKey: vi.fn() } }
})

const { api } = await import("@/lib/api")
const overview = vi.mocked(api.overview)
const rotateWriteKey = vi.mocked(api.rotateWriteKey)

const WITH_KEY: Project = { ID: "p1", Name: "Web", Environment: "production", WriteKey: "wk_live" }
const NO_KEY: Project = { ID: "p2", Name: "Legacy", Environment: "development" }

function renderPage(projects: Project[], canManage = true) {
  overview.mockResolvedValue({ total: 0, by_status: {}, by_type: {}, trend: [] } as never)
  return render(
    <EmbedPage
      projects={projects}
      canManage={canManage}
      onProjectsChanged={() => {}}
      onOpenAutomation={() => {}}
      webhookCount={0}
    />,
  )
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("EmbedPage", () => {
  it("shows the write key and fills it into the snippet", () => {
    renderPage([WITH_KEY])
    expect(screen.getByTestId("write-key").textContent).toBe("wk_live")
    // The snippet carries the key and this console's origin, not localhost.
    const code = Array.from(document.querySelectorAll("code"), (c) => c.textContent).join("\n")
    expect(code).toContain('writeKey="wk_live"')
    expect(code).toContain(`apiUrl="${window.location.origin}"`)
    expect(code).not.toContain("localhost:8787")
  })

  it("rotates only after confirmation and swaps in the new key", async () => {
    rotateWriteKey.mockResolvedValue({ ...WITH_KEY, WriteKey: "wk_new" })
    renderPage([WITH_KEY])

    fireEvent.click(screen.getByRole("button", { name: "更换密钥" }))
    // Opening the dialog must not rotate by itself.
    expect(rotateWriteKey).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole("button", { name: "更换并让旧密钥失效" }))

    await waitFor(() => expect(rotateWriteKey).toHaveBeenCalledWith("p1"))
    await waitFor(() => expect(screen.getByTestId("write-key").textContent).toBe("wk_new"))
  })

  it("offers to generate a key when the listing has none", async () => {
    rotateWriteKey.mockResolvedValue({ ...NO_KEY, WriteKey: "wk_fresh" })
    renderPage([NO_KEY])
    expect(screen.queryByTestId("write-key")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "生成密钥" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "生成密钥" }))
    await waitFor(() => expect(rotateWriteKey).toHaveBeenCalledWith("p2"))
    await waitFor(() => expect(screen.getByTestId("write-key").textContent).toBe("wk_fresh"))
  })

  it("hides rotation from roles the API will refuse", () => {
    renderPage([WITH_KEY], false)
    expect(screen.queryByRole("button", { name: "更换密钥" })).toBeNull()
    expect(screen.getByTestId("write-key").textContent).toBe("wk_live")
  })

  it("reports the first submission without a manual refresh", async () => {
    overview
      .mockResolvedValueOnce({ total: 0, by_status: {}, by_type: {}, trend: [] } as never)
      .mockResolvedValue({ total: 1, by_status: {}, by_type: {}, trend: [] } as never)
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      render(
        <EmbedPage projects={[WITH_KEY]} canManage onProjectsChanged={() => {}} onOpenAutomation={() => {}} webhookCount={0} />,
      )
      await waitFor(() => expect(screen.getByText(/等待第一条反馈/)).toBeTruthy())
      await vi.advanceTimersByTimeAsync(5000)
      await waitFor(() => expect(screen.getByText(/收到 1 条反馈/)).toBeTruthy())
    } finally {
      vi.useRealTimers()
    }
  })
})
