import { useEffect, useState } from "react"
import { ChevronDown, Paperclip } from "lucide-react"
import { toast } from "sonner"
import {
  api,
  ApiError,
  type Activity,
  type FeedbackDetail,
  type FeedbackPriority,
  type FeedbackStatus,
  type Member,
} from "@/lib/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import {
  PRIORITIES,
  PRIORITY_LABEL,
  SCAN_LABEL,
  STATUS_LABEL,
  STATUS_TRANSITIONS,
  TYPE_LABEL,
} from "@/lib/labels"
import { ActivityTimeline } from "@/components/feedback/activity-timeline"
import { Rating, StatusBadge } from "@/components/feedback/indicators"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"

const UNASSIGNED = "__unassigned__"

/**
 * Scheme A's detail pane, hosted in a Sheet so triage never loses the table
 * behind it. Saving sends `updated_at` for optimistic concurrency; a 409 means
 * someone else edited the record, so we reload rather than clobber their work.
 */
export function FeedbackSheet({
  feedbackId,
  members,
  onOpenChange,
  onSaved,
}: {
  feedbackId: string | null
  members: Member[]
  onOpenChange: (open: boolean) => void
  onSaved: (detail: FeedbackDetail) => void
}) {
  const [detail, setDetail] = useState<FeedbackDetail | null>(null)
  const [activities, setActivities] = useState<Activity[]>([])
  const [draft, setDraft] = useState({ status: "open" as FeedbackStatus, priority: "none" as FeedbackPriority, assignee: UNASSIGNED, tags: "" })
  const [note, setNote] = useState("")
  const [saving, setSaving] = useState(false)

  function hydrate(next: FeedbackDetail) {
    setDetail(next)
    setDraft({
      status: next.status,
      priority: next.priority,
      assignee: next.assignee_id ?? UNASSIGNED,
      tags: next.tags.join(", "),
    })
  }

  useEffect(() => {
    if (!feedbackId) return
    let cancelled = false
    setDetail(null)
    setActivities([])
    setNote("")

    void (async () => {
      try {
        const [next, log] = await Promise.all([api.getFeedback(feedbackId), api.activities(feedbackId)])
        if (cancelled) return
        hydrate(next)
        setActivities(log.activities)
      } catch (e) {
        if (!cancelled) toast.error(e instanceof Error ? e.message : "加载详情失败")
      }
    })()

    return () => {
      cancelled = true
    }
  }, [feedbackId])

  async function refresh(id: string) {
    const [next, log] = await Promise.all([api.getFeedback(id), api.activities(id)])
    hydrate(next)
    setActivities(log.activities)
    return next
  }

  async function save() {
    if (!detail) return
    setSaving(true)
    try {
      const updated = await api.updateFeedback(detail.id, {
        status: draft.status,
        priority: draft.priority,
        assignee_id: draft.assignee === UNASSIGNED ? "" : draft.assignee,
        tags: draft.tags.split(",").map((t) => t.trim()).filter(Boolean),
        updated_at: detail.updated_at,
      })
      hydrate(updated)
      setActivities((await api.activities(detail.id)).activities)
      onSaved(updated)
      toast.success("已保存")
    } catch (e) {
      if (e instanceof ApiError && e.isConflict) {
        const fresh = await refresh(detail.id)
        onSaved(fresh)
        toast.error("这条反馈刚被其他人改过，已为你载入最新内容，请确认后重新保存")
      } else {
        toast.error(e instanceof Error ? e.message : "保存失败")
      }
    } finally {
      setSaving(false)
    }
  }

  async function submitNote() {
    if (!detail || !note.trim()) return
    setSaving(true)
    try {
      await api.addNote(detail.id, note.trim())
      setNote("")
      setActivities((await api.activities(detail.id)).activities)
      toast.success("备注已添加")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "添加备注失败")
    } finally {
      setSaving(false)
    }
  }

  // Only offer transitions the server state machine accepts, plus the current value.
  const statusOptions = detail
    ? [detail.status, ...STATUS_TRANSITIONS[detail.status]]
    : ([] as FeedbackStatus[])

  return (
    <Sheet open={feedbackId !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-[440px]">
        {!detail ? (
          <div className="grid gap-3 p-6">
            <Skeleton className="h-6 w-40" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-32 w-full" />
          </div>
        ) : (
          <>
            <SheetHeader className="gap-2">
              <SheetTitle className="flex items-center gap-2 text-base">
                {TYPE_LABEL[detail.type]}反馈
                <StatusBadge status={detail.status} />
              </SheetTitle>
              <SheetDescription className="bg-muted border-primary text-foreground rounded-md border-l-2 p-3 text-sm whitespace-pre-wrap">
                {detail.message}
              </SheetDescription>
            </SheetHeader>

            <div className="grid gap-5 px-4 pb-8">
              <dl className="grid grid-cols-[72px_1fr] gap-y-1.5 text-sm">
                <dt className="text-muted-foreground">评分</dt>
                <dd>
                  <Rating value={detail.rating} />
                </dd>
                <dt className="text-muted-foreground">联系人</dt>
                <dd className="truncate">{detail.contact_email || "匿名"}</dd>
                <dt className="text-muted-foreground">用户 ID</dt>
                <dd className="truncate font-mono text-xs">{detail.external_user_id || "匿名"}</dd>
                <dt className="text-muted-foreground">提交于</dt>
                <dd className="tnum">{formatDateTime(detail.created_at)}</dd>
              </dl>

              {detail.attachments.length > 0 && (
                <div className="grid gap-2">
                  {detail.attachments.map((attachment) => (
                    <a
                      key={attachment.id}
                      href={api.attachmentUrl(attachment.id)}
                      target="_blank"
                      rel="noreferrer"
                      className="bg-muted hover:bg-accent flex items-center gap-2 rounded-md border p-2 text-xs"
                    >
                      <Paperclip className="size-3.5 shrink-0" />
                      <span className="text-primary truncate">{attachment.original_name}</span>
                      <span className="text-muted-foreground tnum ml-auto shrink-0">
                        {formatBytes(attachment.size)} · {SCAN_LABEL[attachment.scan_status]}
                      </span>
                    </a>
                  ))}
                </div>
              )}

              {Object.keys(detail.context).length > 0 && (
                <Collapsible className="text-sm">
                  <CollapsibleTrigger className="text-muted-foreground hover:text-foreground group flex items-center gap-1 [&[data-state=open]>svg]:rotate-180">
                    运行环境
                    <ChevronDown className="size-3.5 transition-transform" />
                  </CollapsibleTrigger>
                  <CollapsibleContent>
                    {/* The clamp has to land on the viewport: ScrollArea's root
                        is only `relative`, so a max-height there leaves the
                        `size-full` viewport auto-sized and nothing ever
                        scrolls. Long values wrap rather than overflow sideways,
                        since only the vertical scrollbar is mounted. */}
                    <ScrollArea className="bg-muted mt-2 rounded-md [&>[data-slot=scroll-area-viewport]]:max-h-40">
                      <pre className="p-3 font-mono text-xs break-all whitespace-pre-wrap">
                        {JSON.stringify(detail.context, null, 2)}
                      </pre>
                    </ScrollArea>
                  </CollapsibleContent>
                </Collapsible>
              )}

              <Separator />

              <div className="grid grid-cols-2 gap-3">
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">状态</Label>
                  <Select
                    value={draft.status}
                    onValueChange={(v) => setDraft({ ...draft, status: v as FeedbackStatus })}
                  >
                    <SelectTrigger className="w-full" size="sm">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {statusOptions.map((status) => (
                        <SelectItem key={status} value={status}>
                          {STATUS_LABEL[status]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">优先级</Label>
                  <Select
                    value={draft.priority}
                    onValueChange={(v) => setDraft({ ...draft, priority: v as FeedbackPriority })}
                  >
                    <SelectTrigger className="w-full" size="sm">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PRIORITIES.map((priority) => (
                        <SelectItem key={priority} value={priority}>
                          {PRIORITY_LABEL[priority]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">负责人</Label>
                  <Select value={draft.assignee} onValueChange={(v) => setDraft({ ...draft, assignee: v })}>
                    <SelectTrigger className="w-full" size="sm">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={UNASSIGNED}>未分配</SelectItem>
                      {members.map((member) => (
                        <SelectItem key={member.ID} value={member.ID}>
                          {member.Name || member.Email}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="tags" className="text-muted-foreground text-xs">
                    标签
                  </Label>
                  <Input
                    id="tags"
                    className="h-8"
                    placeholder="逗号分隔"
                    value={draft.tags}
                    onChange={(e) => setDraft({ ...draft, tags: e.target.value })}
                  />
                </div>
              </div>
              <Button onClick={save} disabled={saving}>
                {saving ? "保存中…" : "保存改动"}
              </Button>

              <div className="grid gap-2">
                <Textarea
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  placeholder="写一条内部备注…（团队可见，不会发给用户）"
                  className="min-h-16 resize-none"
                />
                <div className="flex items-center gap-2">
                  <span className="text-muted-foreground text-xs">仅团队可见</span>
                  <Button
                    size="sm"
                    variant="secondary"
                    className="ml-auto"
                    onClick={submitNote}
                    disabled={saving || !note.trim()}
                  >
                    添加备注
                  </Button>
                </div>
              </div>

              <Separator />
              <ActivityTimeline activities={activities} members={members} />
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
