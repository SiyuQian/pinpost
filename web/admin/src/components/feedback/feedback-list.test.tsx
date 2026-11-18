// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { ApiError, type FeedbackFilters, type FeedbackListItem, type Member } from "@/lib/api"
import { FeedbackList } from "@/components/feedback/feedback-list"
import { TooltipProvider } from "@/components/ui/tooltip"

/**
 * Every defect the reviews found in this component lived in the wiring between
 * the tested helpers and the component — a stale page appended under new
 * filters, a 409 swallowed without reloading. The helpers have their own specs;
 * these two cases pin the wiring.
 */
vi.mock("@/lib/api", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: { listFeedback: vi.fn(), updateFeedback: vi.fn() },
  }
})

const { api } = await import("@/lib/api")
const listFeedback = vi.mocked(api.listFeedback)
const updateFeedback = vi.mocked(api.updateFeedback)

const MEMBER: Member = {
  ID: "m1",
  Name: "Ada",
  Email: "ada@example.com",
  Role: "owner",
  OrganizationID: "o1",
}

function row(id: string, message: string): FeedbackListItem {
  return {
    id,
    type: "bug",
    message,
    source: "widget",
    status: "open",
    priority: "none",
    assignee_id: null,
    tags: [],
    project_id: "p1",
    created_at: new Date().toISOString(),
    updated_at: "2026-08-24T09:12:00Z",
  }
}

/** A promise whose resolution this test controls, to interleave two pages. */
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => (resolve = r))
  return { promise, resolve }
}

function renderList(props: { filters?: FeedbackFilters; onDataChanged?: () => void } = {}) {
  const view = render(
    <TooltipProvider>
      <FeedbackList
        members={[MEMBER]}
        currentMemberId={MEMBER.ID}
        overview={null}
        filters={props.filters ?? {}}
        onFiltersChange={() => {}}
        search=""
        onSearchChange={() => {}}
        onOpenDetail={() => {}}
        onDataChanged={props.onDataChanged ?? (() => {})}
        onItemsLoaded={() => {}}
        projects={[]}
        reloadToken={0}
      />
    </TooltipProvider>,
  )
  return view
}

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(cleanup)

describe("FeedbackList", () => {
  it("reloads the record when a quick action loses the concurrency race", async () => {
    listFeedback.mockResolvedValue({ items: [row("f1", "上传失败")], next_cursor: "" })
    updateFeedback.mockRejectedValue(new ApiError(409, "conflict", "stale updated_at"))
    const onDataChanged = vi.fn()

    const { container } = renderList({ onDataChanged })
    await screen.findByText("上传失败")

    const claim = [...container.querySelectorAll("button")].find((b) => b.textContent === "认领")!
    claim.click()

    // Without the reload the row keeps its stale updated_at and every retry
    // fails identically.
    await waitFor(() => expect(onDataChanged).toHaveBeenCalled())
    expect(updateFeedback).toHaveBeenCalledWith("f1", {
      assignee_id: MEMBER.ID,
      updated_at: "2026-08-24T09:12:00Z",
    })
  })

  it("discards a 加载更多 page that arrives after the filters changed", async () => {
    const stalePage = deferred<{ items: FeedbackListItem[]; next_cursor: string }>()
    listFeedback
      .mockResolvedValueOnce({ items: [row("f1", "第一页")], next_cursor: "cursor-1" })
      .mockReturnValueOnce(stalePage.promise)
      .mockResolvedValueOnce({ items: [row("f9", "筛选后的行")], next_cursor: "" })

    const { rerender, container } = renderList()
    await screen.findByText("第一页")

    const more = [...container.querySelectorAll("button")].find((b) => b.textContent === "加载更多")!
    more.click()

    // The filter changes while that page is still in flight.
    rerender(
      <TooltipProvider>
        <FeedbackList
          members={[MEMBER]}
          currentMemberId={MEMBER.ID}
          overview={null}
          filters={{ status: "open" }}
          onFiltersChange={() => {}}
          search=""
          onSearchChange={() => {}}
          onOpenDetail={() => {}}
          onDataChanged={() => {}}
          onItemsLoaded={() => {}}
          projects={[]}
          reloadToken={0}
        />
      </TooltipProvider>,
    )
    await screen.findByText("筛选后的行")

    stalePage.resolve({ items: [row("f2", "过期的第二页")], next_cursor: "cursor-stale" })

    await waitFor(() => expect(listFeedback).toHaveBeenCalledTimes(3))
    expect(screen.queryByText("过期的第二页")).toBeNull()
    expect(screen.getByText("筛选后的行")).toBeTruthy()
  })
})
