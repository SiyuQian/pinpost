import type { FeedbackListItem } from "@/lib/api"
import { describeSource, formatDateTime } from "@/lib/format"
import { SOURCE_LABEL, STATUS_LABEL, TYPE_LABEL } from "@/lib/labels"

const HEAD = [
  "提交时间",
  "类型",
  "状态",
  "优先级",
  "评分",
  "负责人",
  "提交者",
  "提交方式",
  "来源",
  "标签",
  "反馈内容",
]

/**
 * Spreadsheets evaluate a cell that starts with `=`, `+`, `-`, `@`, a tab or a
 * carriage return, quoted or not — and `message` and `tags` are free text from
 * anyone who can reach the embed widget, opened later by an admin on their own
 * machine. Prefixing an apostrophe makes the cell a literal string in Excel,
 * LibreOffice, WPS and Sheets; RFC 4180 quoting alone does not.
 */
function neutralize(cell: unknown) {
  const text = String(cell)
  return /^[=+\-@\t\r]/.test(text) ? "'" + text : text
}

/** RFC 4180: quote every field, double the inner quotes, separate rows with CRLF. */
export function toCsv(
  items: FeedbackListItem[],
  memberName: (id?: string | null) => string | null,
) {
  const rows = items.map((i) => [
    formatDateTime(i.created_at),
    TYPE_LABEL[i.type],
    STATUS_LABEL[i.status],
    i.priority,
    i.rating ?? "",
    memberName(i.assignee_id) ?? "未分配",
    i.contact_email ?? "匿名",
    SOURCE_LABEL[i.source],
    describeSource(i).join(" · "),
    i.tags.join(" "),
    i.message,
  ])
  return [HEAD, ...rows]
    .map((row) => row.map((cell) => `"${neutralize(cell).replaceAll('"', '""')}"`).join(","))
    .join("\r\n")
}
