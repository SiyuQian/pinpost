import { memo, useEffect, useMemo, useRef, useState } from "react"
import { Download, Mail, Plus, Rows3, X } from "lucide-react"
import { toast } from "sonner"
import {
  api,
  UnauthenticatedError,
  type FeedbackFilters,
  type FeedbackListItem,
  type FeedbackStatus,
  type Member,
  type Overview,
  type Project,
} from "@/lib/api"
import { avatarTint } from "@/lib/avatar"
import { toCsv } from "@/lib/csv"
import { downloadBlob } from "@/lib/download"
import { classifyWriteError } from "@/lib/errors"
import {
  describeSource,
  displayWidth,
  formatDateTime,
  formatRelative,
  initials,
} from "@/lib/format"
import {
  PRIORITY_RANK,
  SOURCE_LABEL,
  SOURCES,
  STATUS_LABEL,
  STATUS_TRANSITIONS,
  STATUSES,
  TYPE_LABEL,
  TYPES,
} from "@/lib/labels"
import { RequestSequence } from "@/lib/sequence"
import { isOverdue } from "@/lib/sla"
import { BulkBar } from "@/components/feedback/bulk-bar"
import { CreateFeedbackDialog } from "@/components/feedback/create-feedback-dialog"
import { FacetedFilter } from "@/components/feedback/faceted-filter"
import { IntegrationsCard } from "@/components/feedback/integrations-card"
import { PriorityTag, Rating, SourceChip, StatusBadge, TagChip } from "@/components/feedback/indicators"
import { TeamLoad } from "@/components/feedback/team-load"
import { comingSoon } from "@/components/shell/coming-soon"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

const PAGE_SIZE = 50
/**
 * The message line is clamped to this rendered width (`max-w-[46ch]` on the
 * cell), which is also what the tooltip gate measures against — the two have to
 * stay the same number or rows truncate with no way to read them.
 */
const MESSAGE_CLAMP = 46

/** Column template shared by the header row and every item row. */
const ROW_GRID =
  "grid grid-cols-[24px_minmax(0,1fr)_100px_84px_70px_122px_84px] items-center gap-3 px-4"

type SortMode = "waiting" | "newest" | "priority" | "rating"

type ViewKey = "all" | "mine" | "overdue" | "low_rating"

/**
 * Saved views are filter presets with a name. `all` is unfiltered and `mine`
 * narrows via the API, so both hold across the whole set; `overdue` and
 * `low_rating` are predicates over loaded rows, because the list endpoint has
 * no age or rating filter — their counts are therefore "in what's loaded", the
 * same caveat the metrics carry. They are local presets, not a shared-view
 * feature: nothing is persisted or visible to anyone else.
 */
const VIEWS: {
  key: ViewKey
  label: string
  filters?: (memberId: string) => Partial<FeedbackFilters>
  predicate?: (item: FeedbackListItem) => boolean
}[] = [
  { key: "all", label: "全部反馈" },
  { key: "mine", label: "需要我处理", filters: (id) => ({ assignee_id: id }) },
  { key: "overdue", label: "已超时", predicate: isOverdue },
  { key: "low_rating", label: "低分反馈", predicate: (i) => typeof i.rating === "number" && i.rating <= 2 },
]

/**
 * Whether a message clips depends on rendered width, not character count: the
 * line clamps at `46ch` and a full-width CJK glyph costs about two `ch`, so
 * counting characters hides the tooltip on exactly the Chinese rows that do
 * overflow — and this console is Chinese end to end.
 */
const MessageLine = memo(function MessageLine({ message }: { message: string }) {
  const clipped = displayWidth(message, MESSAGE_CLAMP) > MESSAGE_CLAMP
  const text = <span className="truncate text-sm font-medium tracking-[-0.01em]">{message}</span>
  if (!clipped) return text

  return (
    <Tooltip>
      <TooltipTrigger asChild>{text}</TooltipTrigger>
      <TooltipContent className="max-w-sm whitespace-pre-wrap">{message}</TooltipContent>
    </Tooltip>
  )
})

export function FeedbackList({
  members,
  currentMemberId,
  overview,
  filters,
  onFiltersChange,
  search,
  onSearchChange,
  onOpenDetail,
  onDataChanged,
  onItemsLoaded,
  reloadToken,
  onOpenAutomation,
  onOpenEmbed,
  webhookConnected,
  projects,
}: {
  members: Member[]
  currentMemberId: string
  overview: Overview | null
  filters: FeedbackFilters
  onFiltersChange: (filters: FeedbackFilters) => void
  search: string
  onSearchChange: (search: string) => void
  onOpenDetail: (id: string) => void
  onDataChanged: () => void
  onItemsLoaded: (items: FeedbackListItem[]) => void
  reloadToken: number
  /** Opens the 自动化 tab, where Webhook (the one wired integration) lives. */
  onOpenAutomation?: () => void
  /** Opens 嵌入与集成: write key and install snippet. */
  onOpenEmbed?: () => void
  /** Real endpoint state, so the Webhook chip only claims a connection it has. */
  webhookConnected?: boolean
  /** Targets for a new internal report; the form needs an explicit project. */
  projects: Project[]
}) {
  const [items, setItems] = useState<FeedbackListItem[]>([])
  const [cursor, setCursor] = useState("")
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [sort, setSort] = useState<SortMode>("waiting")
  const [view, setView] = useState<ViewKey>("all")
  const [compact, setCompact] = useState(false)
  const [creating, setCreating] = useState(false)
  const [selection, setSelection] = useState<Set<string>>(new Set())
  const [busyId, setBusyId] = useState<string | null>(null)

  // Debounce the free-text query so typing doesn't fire a request per keystroke.
  const [debouncedSearch, setDebouncedSearch] = useState("")
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedSearch(search.trim()), 300)
    return () => clearTimeout(timer)
  }, [search])

  const activeView = VIEWS.find((v) => v.key === view)!
  const viewFilters = activeView.filters?.(currentMemberId) ?? {}
  const activeFilters = useMemo<FeedbackFilters>(
    () => ({ ...filters, ...viewFilters, q: debouncedSearch || undefined }),
    // viewFilters is derived from `view`; spreading it directly would make a
    // new object every render and refetch on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [filters, view, currentMemberId, debouncedSearch],
  )

  // Guards against an older in-flight page overwriting a newer filter's
  // results; see lib/sequence.ts, where the interleaving is tested.
  const requests = useRef(new RequestSequence())

  useEffect(() => {
    const ticket = requests.current.next()
    setLoading(true)
    setSelection(new Set())
    void (async () => {
      try {
        const page = await api.listFeedback(activeFilters, undefined, PAGE_SIZE)
        if (!requests.current.isCurrent(ticket)) return
        setItems(page.items)
        setCursor(page.next_cursor)
      } catch (e) {
        if (!requests.current.isCurrent(ticket) || e instanceof UnauthenticatedError) return
        toast.error(e instanceof Error ? e.message : "加载反馈列表失败")
      } finally {
        if (requests.current.isCurrent(ticket)) setLoading(false)
      }
    })()
  }, [activeFilters, reloadToken])

  useEffect(() => onItemsLoaded(items), [items, onItemsLoaded])

  async function loadMore() {
    if (!cursor) return
    // This page continues the query already on screen, so it rides its ticket:
    // a filter, view or search change while it is in flight supersedes both,
    // and the resolved page is dropped instead of appended under new filters.
    const ticket = requests.current.latest()
    setLoadingMore(true)
    try {
      const page = await api.listFeedback(activeFilters, cursor, PAGE_SIZE)
      if (!requests.current.isCurrent(ticket)) return
      setItems((prev) => [...prev, ...page.items])
      setCursor(page.next_cursor)
    } catch (e) {
      if (!requests.current.isCurrent(ticket)) return
      toast.error(e instanceof Error ? e.message : "加载更多失败")
    } finally {
      if (requests.current.isCurrent(ticket)) setLoadingMore(false)
    }
  }

  /**
   * Sorting and the predicate views run client-side over loaded rows only — the
   * API returns newest-first and takes no sort, age or rating parameter.
   */
  const rows = useMemo(() => {
    const visible = activeView.predicate ? items.filter(activeView.predicate) : items
    if (sort === "newest") return visible
    const copy = [...visible]
    if (sort === "waiting") copy.sort((a, b) => a.created_at.localeCompare(b.created_at))
    else if (sort === "priority") copy.sort((a, b) => PRIORITY_RANK[b.priority] - PRIORITY_RANK[a.priority])
    else copy.sort((a, b) => (a.rating ?? 6) - (b.rating ?? 6))
    return copy
  }, [items, sort, activeView])

  const memberName = (id?: string | null) => {
    if (!id) return null
    const member = members.find((m) => m.ID === id)
    return member ? member.Name || member.Email : "未知成员"
  }

  const selected = rows.filter((r) => selection.has(r.id))
  const hasFilters = Boolean(
    filters.status ||
      filters.type ||
      filters.source ||
      filters.assignee_id ||
      filters.project_id ||
      search,
  )

  function toggle(id: string) {
    setSelection((prev) => {
      const next = new Set(prev)
      if (!next.delete(id)) next.add(id)
      return next
    })
  }

  async function patch(item: FeedbackListItem, body: Parameters<typeof api.updateFeedback>[1], done: string) {
    setBusyId(item.id)
    try {
      await api.updateFeedback(item.id, { ...body, updated_at: item.updated_at })
      toast.success(done)
      onDataChanged()
    } catch (e) {
      const { reload, message, description } = classifyWriteError(e)
      // A stale updated_at lost the race: reload so the row shows the current
      // state instead of failing identically on the next click.
      if (reload) onDataChanged()
      toast.error(message, description ? { description } : undefined)
    } finally {
      setBusyId(null)
    }
  }

  function exportCsv() {
    const blob = new Blob(["﻿" + toCsv(rows, memberName)], { type: "text/csv;charset=utf-8" })
    downloadBlob(blob, `pinpost-${new Date().toISOString().slice(0, 10)}.csv`)
  }

  const statusOptions = STATUSES.map((status) => ({
    value: status,
    label: STATUS_LABEL[status],
    count: overview?.by_status[status],
  }))
  const typeOptions = TYPES.map((type) => ({
    value: type,
    label: TYPE_LABEL[type],
    count: overview?.by_type[type],
  }))
  // No counts: the overview aggregates by status and type only, and a count
  // derived from the loaded page would disagree with the other two facets.
  const sourceOptions = SOURCES.map((source) => ({ value: source, label: SOURCE_LABEL[source] }))
  const memberOptions = members.map((m) => ({ value: m.ID, label: m.Name || m.Email }))

  return (
    <div className="grid gap-3.5">
      {/* Saved views */}
      <div className="flex flex-wrap items-center gap-2">
        {VIEWS.map((v) => {
          const count = v.predicate ? items.filter(v.predicate).length : undefined
          return (
            <button
              key={v.key}
              type="button"
              onClick={() => setView(v.key)}
              aria-pressed={view === v.key}
              className={cn(
                "focus-visible:ring-ring/50 inline-flex h-[30px] items-center gap-1.5 rounded-full border px-3 text-[13px] outline-none focus-visible:ring-[3px]",
                view === v.key
                  ? "bg-primary text-primary-foreground border-transparent font-semibold"
                  : "bg-card text-muted-foreground hover:text-foreground",
              )}
            >
              {v.label}
              {count !== undefined && <span className="tnum font-mono text-[10.5px]">{count}</span>}
            </button>
          )
        })}
        <button
          type="button"
          onClick={comingSoon}
          className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 inline-flex h-[30px] items-center rounded-full border border-dashed px-3 text-[13px] outline-none focus-visible:ring-[3px]"
        >
          ＋ 新建视图
        </button>

        <div className="ml-auto flex items-center gap-2">
          <Select value={sort} onValueChange={(v) => setSort(v as SortMode)}>
            <SelectTrigger size="sm" className="h-[30px] rounded-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="waiting">等待最久</SelectItem>
              <SelectItem value="newest">最新优先</SelectItem>
              <SelectItem value="priority">优先级优先</SelectItem>
              <SelectItem value="rating">评分最低优先</SelectItem>
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            className="h-[30px] rounded-full"
            aria-pressed={compact}
            onClick={() => setCompact((c) => !c)}
          >
            <Rows3 /> {compact ? "宽松" : "紧凑"}
          </Button>
          <Button variant="outline" size="sm" className="h-[30px] rounded-full" onClick={exportCsv}>
            <Download /> 导出 CSV
          </Button>
          <Button size="sm" className="h-[30px] rounded-full" onClick={() => setCreating(true)}>
            <Plus /> 新建反馈
          </Button>
        </div>
      </div>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-2">
        <FacetedFilter
          title="状态"
          options={statusOptions}
          value={filters.status}
          onChange={(v) => onFiltersChange({ ...filters, status: v as FeedbackFilters["status"] })}
        />
        <FacetedFilter
          title="类型"
          options={typeOptions}
          value={filters.type}
          onChange={(v) => onFiltersChange({ ...filters, type: v as FeedbackFilters["type"] })}
        />
        <FacetedFilter
          title="来源"
          options={sourceOptions}
          value={filters.source}
          onChange={(v) => onFiltersChange({ ...filters, source: v as FeedbackFilters["source"] })}
        />
        <FacetedFilter
          title="负责人"
          options={memberOptions}
          value={filters.assignee_id}
          onChange={(v) => onFiltersChange({ ...filters, assignee_id: v })}
        />
        {search && (
          <button
            type="button"
            onClick={() => onSearchChange("")}
            className="bg-primary/10 text-primary focus-visible:ring-ring/50 inline-flex h-[29px] items-center gap-1.5 rounded-full px-3 text-[12.5px] font-medium outline-none focus-visible:ring-[3px]"
          >
            搜索 · {search}
            <X className="size-3" />
          </button>
        )}
        {hasFilters && (
          <>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                onFiltersChange({})
                onSearchChange("")
              }}
            >
              重置 <X />
            </Button>
            <span className="text-muted-foreground text-xs">
              筛选有改动 ·{" "}
              <button type="button" onClick={comingSoon} className="text-primary font-semibold">
                存为视图
              </button>
            </span>
          </>
        )}
      </div>

      {/* List */}
      <div className="bg-card overflow-hidden rounded-2xl border shadow-lift">
        {selected.length > 0 && (
          <BulkBar
            selected={selected}
            members={members}
            onClear={() => setSelection(new Set())}
            onDone={() => {
              setSelection(new Set())
              onDataChanged()
            }}
          />
        )}

        <div className="overflow-x-auto">
          <div className="min-w-[820px]">
            <div className={cn(ROW_GRID, "text-muted-foreground border-b py-2.5")}>
              <Checkbox
                checked={
                  selected.length === 0
                    ? false
                    : selected.length === rows.length
                      ? true
                      : "indeterminate"
                }
                onCheckedChange={(value) =>
                  setSelection(value ? new Set(rows.map((r) => r.id)) : new Set())
                }
                aria-label="全选"
              />
              <span className="cap">反馈</span>
              <span className="cap">状态</span>
              <span className="cap">优先级</span>
              <span className="cap">评分</span>
              <span className="cap">负责人</span>
              <span className="cap text-right">等待</span>
            </div>

            {loading ? (
              Array.from({ length: 6 }, (_, i) => (
                <div key={i} className={cn(ROW_GRID, "border-b py-3")}>
                  <Skeleton className="col-span-7 h-5 w-full" />
                </div>
              ))
            ) : rows.length === 0 ? (
              <div className="text-muted-foreground grid place-items-center gap-2 px-6 py-14 text-center">
                <span className="bg-panel-2 grid size-11 place-items-center rounded-xl text-lg">
                  <Mail className="size-5" />
                </span>
                <span className="text-foreground text-[15px] font-semibold">
                  {hasFilters || view !== "all" ? "没有符合条件的反馈" : "还没有收到反馈"}
                </span>
                <span className="max-w-[44ch] text-[13px]">
                  {hasFilters || view !== "all"
                    ? "试试放宽筛选，或切回「全部反馈」视图。"
                    : "把嵌入代码放到网站上，用户提交的第一条反馈就会出现在这里。"}
                </span>
                {!hasFilters && view === "all" && (
                  <div className="mt-1 flex flex-wrap justify-center gap-2">
                    <Button size="sm" className="rounded-full" onClick={onOpenEmbed ?? comingSoon}>
                      复制嵌入代码
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      className="rounded-full"
                      onClick={() => setCreating(true)}
                    >
                      <Plus /> 新建反馈
                    </Button>
                  </div>
                )}
              </div>
            ) : (
              rows.map((item) => {
                const name = memberName(item.assignee_id)
                const overdue = isOverdue(item)
                return (
                  <div
                    key={item.id}
                    tabIndex={0}
                    onClick={() => onOpenDetail(item.id)}
                    onKeyDown={(e) => {
                      // Only when the row itself holds focus: the checkbox and
                      // the quick actions inside it own their own Space/Enter.
                      if (e.target !== e.currentTarget) return
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault()
                        onOpenDetail(item.id)
                      }
                    }}
                    className={cn(
                      ROW_GRID,
                      "group hover:bg-panel-2 focus-visible:ring-ring/50 relative cursor-pointer border-b outline-none last:border-b-0 focus-visible:ring-[3px]",
                      compact ? "py-1.5" : "py-3",
                      selection.has(item.id) && "bg-primary/8",
                    )}
                  >
                    <Checkbox
                      checked={selection.has(item.id)}
                      onCheckedChange={() => toggle(item.id)}
                      onClick={(e) => e.stopPropagation()}
                      aria-label="选择这一行"
                    />
                    <span className="grid min-w-0 max-w-[46ch] gap-0.5">
                      <MessageLine message={item.message} />
                      {!compact && (
                        <span className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
                          <SourceChip source={item.source} />
                          <TagChip>{TYPE_LABEL[item.type]}</TagChip>
                          {item.tags.slice(0, 2).map((tag) => (
                            <TagChip key={tag}>{tag}</TagChip>
                          ))}
                          {/* Who sent it and from where, so triaging a row does
                              not need the detail sheet open. */}
                          <span className="truncate" title={formatDateTime(item.created_at)}>
                            {[item.contact_email || "匿名", ...describeSource(item)].join(" · ")}
                          </span>
                        </span>
                      )}
                    </span>
                    <StatusBadge status={item.status} />
                    <PriorityTag priority={item.priority} />
                    <Rating value={item.rating} />
                    {name ? (
                      <span className="flex min-w-0 items-center gap-2 text-[13px]">
                        <Avatar className="size-[22px]">
                          <AvatarFallback
                            className="avatar-tint font-mono text-[9.5px] font-bold"
                            style={avatarTint(name)}
                          >
                            {initials(name)}
                          </AvatarFallback>
                        </Avatar>
                        <span className="truncate">{name}</span>
                      </span>
                    ) : (
                      <span className="text-muted-foreground text-xs">未分配</span>
                    )}
                    <span
                      className={cn(
                        "tnum text-right font-mono text-[11.5px]",
                        overdue ? "text-prio-urgent font-semibold" : "text-muted-foreground",
                      )}
                    >
                      {formatRelative(item.created_at)}
                    </span>

                    {/* Quick actions: the three things done to a row without
                        needing to read the whole thread first. */}
                    <span
                      className="bg-card absolute right-4 flex gap-1.5 rounded-full border p-1 opacity-0 shadow-lift transition-opacity group-focus-within:opacity-100 group-hover:opacity-100"
                      onClick={(e) => e.stopPropagation()}
                    >
                      {!item.assignee_id && (
                        <Button
                          variant="ghost"
                          size="xs"
                          className="rounded-full"
                          disabled={busyId === item.id}
                          onClick={() =>
                            patch(item, { assignee_id: currentMemberId }, "已认领这条反馈")
                          }
                        >
                          认领
                        </Button>
                      )}
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="xs"
                            className="rounded-full"
                            disabled={busyId === item.id}
                          >
                            改状态
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          {STATUS_TRANSITIONS[item.status].map((next: FeedbackStatus) => (
                            <DropdownMenuItem
                              key={next}
                              onSelect={() =>
                                patch(item, { status: next }, `已改为${STATUS_LABEL[next]}`)
                              }
                            >
                              改为 {STATUS_LABEL[next]}
                            </DropdownMenuItem>
                          ))}
                        </DropdownMenuContent>
                      </DropdownMenu>
                      <Button
                        size="xs"
                        className="rounded-full font-semibold"
                        onClick={() => onOpenDetail(item.id)}
                      >
                        {item.status === "resolved" ? "查看" : "回复"}
                      </Button>
                    </span>
                  </div>
                )
              })
            )}
          </div>
        </div>

        <div className="text-muted-foreground bg-panel-2/50 flex items-center gap-3 border-t px-4 py-2.5 text-xs">
          <span className="tnum font-mono">
            已加载 {items.length}
            {overview ? ` / 共 ${overview.total.toLocaleString("zh-CN")}` : ""}
          </span>
          {rows.length !== items.length && (
            <span>
              当前视图显示 <span className="tnum font-mono">{rows.length}</span> 条
            </span>
          )}
          <span className="ml-auto">
            {cursor ? (
              <Button variant="outline" size="sm" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? "加载中…" : "加载更多"}
              </Button>
            ) : (
              items.length > 0 && "已经到底了"
            )}
          </span>
        </div>
      </div>

      <div className="grid gap-3.5 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <IntegrationsCard
          onOpenAutomation={onOpenAutomation}
          onOpenEmbed={onOpenEmbed}
          webhookConnected={webhookConnected}
        />
        <TeamLoad items={items} members={members} />
      </div>

      <CreateFeedbackDialog
        open={creating}
        onOpenChange={setCreating}
        projects={projects}
        projectId={filters.project_id}
        onCreated={onDataChanged}
      />
    </div>
  )
}
