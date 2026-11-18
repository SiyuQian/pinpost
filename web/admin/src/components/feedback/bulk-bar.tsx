import { useState } from "react"
import { toast } from "sonner"
import { api, type FeedbackListItem, type FeedbackStatus, type Member } from "@/lib/api"
import { STATUS_LABEL, STATUS_TRANSITIONS } from "@/lib/labels"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

/**
 * Bulk edits fan out to one PATCH per row — the API has no batch endpoint yet,
 * so we run them together and report per-row outcomes instead of pretending
 * the whole set succeeded. Rows whose status transition is illegal come back
 * as failures and are named in the toast.
 */
export function BulkBar({
  selected,
  members,
  onDone,
  onClear,
}: {
  selected: FeedbackListItem[]
  members: Member[]
  onDone: () => void
  onClear: () => void
}) {
  const [running, setRunning] = useState(false)

  async function apply(label: string, patch: (item: FeedbackListItem) => Parameters<typeof api.updateFeedback>[1]) {
    setRunning(true)
    const results = await Promise.allSettled(
      selected.map((item) => api.updateFeedback(item.id, { ...patch(item), updated_at: item.updated_at })),
    )
    setRunning(false)

    const failed = results.filter((r) => r.status === "rejected").length
    const ok = results.length - failed
    if (failed === 0) toast.success(`${label}：${ok} 条已更新`)
    else if (ok === 0) toast.error(`${label}失败：${failed} 条都没能更新`)
    else toast.warning(`${label}：${ok} 条已更新，${failed} 条失败（状态流转不允许或已被他人修改）`)
    onDone()
  }

  // Only offer a status every selected row can legally move to.
  const commonStatuses = (["open", "in_progress", "resolved", "spam"] as FeedbackStatus[]).filter((target) =>
    selected.every((item) => item.status === target || STATUS_TRANSITIONS[item.status].includes(target)),
  )

  return (
    <div className="bg-primary/10 flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm">
      <span className="tnum font-medium">{selected.length} 项已选</span>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" disabled={running || commonStatuses.length === 0}>
            改状态
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          {commonStatuses.map((status) => (
            <DropdownMenuItem key={status} onSelect={() => apply(`改为${STATUS_LABEL[status]}`, () => ({ status }))}>
              {STATUS_LABEL[status]}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" disabled={running}>
            指派给
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem onSelect={() => apply("取消指派", () => ({ assignee_id: "" }))}>
            未分配
          </DropdownMenuItem>
          {members.map((member) => (
            <DropdownMenuItem
              key={member.ID}
              onSelect={() => apply(`指派给 ${member.Name || member.Email}`, () => ({ assignee_id: member.ID }))}
            >
              {member.Name || member.Email}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button size="sm" variant="outline" disabled={running || !commonStatuses.includes("spam")}>
            标为垃圾
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>把 {selected.length} 条反馈标为垃圾？</AlertDialogTitle>
            <AlertDialogDescription>
              这些反馈会被标记为垃圾，但仍会留在默认列表里 —— 可以用状态筛选单独查看，也可以随时改回待处理。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => apply("标为垃圾", () => ({ status: "spam" }))}>
              标为垃圾
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Button size="sm" variant="ghost" className="ml-auto" onClick={onClear} disabled={running}>
        取消选择
      </Button>
    </div>
  )
}
