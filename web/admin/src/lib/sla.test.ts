import { describe, expect, it } from "vitest"
import type { FeedbackListItem, FeedbackStatus } from "@/lib/api"
import { isOverdue, OVERDUE_HOURS } from "@/lib/sla"

function row(hoursAgo: number, status: FeedbackStatus = "open"): FeedbackListItem {
  return {
    id: "f1",
    type: "bug",
    message: "占位",
    source: "widget",
    status,
    priority: "none",
    tags: [],
    project_id: "p1",
    created_at: new Date(Date.now() - hoursAgo * 3_600_000).toISOString(),
    updated_at: new Date().toISOString(),
  }
}

/**
 * Three call sites read this: the 待处理 tile's badge, the 已超时 view and the
 * red 等待 column. They were unified precisely so they cannot disagree, which
 * also means one flipped comparison would be wrong in all three at once.
 */
describe("isOverdue", () => {
  it("flags an untouched report past the SLA", () => {
    expect(isOverdue(row(OVERDUE_HOURS + 1))).toBe(true)
  })

  it("leaves one still inside the window alone", () => {
    expect(isOverdue(row(OVERDUE_HOURS - 1))).toBe(false)
    // The boundary is strict: exactly at the SLA is not yet a breach.
    expect(isOverdue(row(OVERDUE_HOURS))).toBe(false)
  })

  it("only counts reports nobody has picked up", () => {
    for (const status of ["in_progress", "resolved", "spam"] as FeedbackStatus[]) {
      expect(isOverdue(row(OVERDUE_HOURS + 100, status))).toBe(false)
    }
  })
})
