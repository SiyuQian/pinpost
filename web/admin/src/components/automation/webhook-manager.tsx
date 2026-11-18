import { useEffect, useRef, useState } from "react"
import { Bell, ChevronDown, Copy, Pause, Play, Plus, RotateCcw, Trash2 } from "lucide-react"
import { toast } from "sonner"
import {
  api,
  UnauthenticatedError,
  type WebhookDelivery,
  type WebhookDeliveryStatus,
  type WebhookEndpoint,
  type WebhookEventType,
  type WebhookStatus,
} from "@/lib/api"
import { formatDateTime, formatRelative } from "@/lib/format"
import {
  DELIVERY_STATUS_LABEL,
  WEBHOOK_EVENT_TYPES,
  WEBHOOK_EVENT_TYPE_LABEL,
  WEBHOOK_STATUS_LABEL,
} from "@/lib/labels"
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
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

/**
 * The "自动化" tab: webhook endpoint management. The server already exposes the
 * full CRUD surface (list/create/pause/delete, delivery history and replay), so
 * this page is pure wiring — no new backend. Every mutation is owner/admin-only
 * server-side; `canManage` mirrors that gate so a viewer never opens a 403.
 */
export function WebhookManager({
  canManage,
  onDataChanged,
}: {
  canManage: boolean
  /** Re-reads the workspace so the onboarding checklist tracks endpoint count. */
  onDataChanged?: () => void
}) {
  const [endpoints, setEndpoints] = useState<WebhookEndpoint[]>([])
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState<string | null>(null)

  // Create-dialog state. `created` holds the signing secret, which the API
  // returns exactly once; like the project write key, the dialog flips to a
  // "copy it now" phase that refuses to close until acknowledged.
  const [creating, setCreating] = useState(false)
  const [url, setUrl] = useState("")
  const [eventTypes, setEventTypes] = useState<Set<WebhookEventType>>(new Set())
  const [saving, setSaving] = useState(false)
  const [created, setCreated] = useState<{ url: string; secret: string } | null>(null)

  // Delivery history for the one endpoint expanded at a time.
  const [deliveriesFor, setDeliveriesFor] = useState<string | null>(null)
  const [deliveries, setDeliveries] = useState<WebhookDelivery[]>([])
  const [deliveriesLoading, setDeliveriesLoading] = useState(false)
  // The endpoint whose history was requested last: a slower earlier response
  // must not paint its rows under a different endpoint's header.
  const requestedDeliveriesFor = useRef<string | null>(null)

  const load = async () => {
    try {
      const { endpoints } = await api.webhookEndpoints()
      setEndpoints(endpoints)
    } catch (e) {
      // A lapsed session drops to the login card via App's loadWorkspace, so
      // stay silent here instead of a red toast over stale rows.
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "加载 Webhook 端点失败")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (canManage) void load()
    // A viewer never fetches: every webhook route 403s below admin.
    else setLoading(false)
  }, [canManage])

  async function submitCreate() {
    const trimmed = url.trim()
    if (!trimmed || saving) return
    // The server owns full validation; this just catches the obvious miss
    // before a round-trip so the user is not told "invalid URL" by the API.
    if (!/^https?:\/\//i.test(trimmed)) {
      toast.error("URL 需要以 http:// 或 https:// 开头")
      return
    }
    setSaving(true)
    try {
      const ep = await api.createWebhookEndpoint(trimmed, [...eventTypes])
      setCreated({ url: ep.url, secret: ep.secret ?? "" })
      // Keep the one-time secret out of the list state: it has no further use
      // once on screen, and the list would otherwise hold it until the page
      // unmounts. The server omits it on list, so mirroring that here matches.
      const listed: WebhookEndpoint = { ...ep }
      delete listed.secret
      setEndpoints((prev) => [...prev, listed])
      onDataChanged?.()
    } catch (e) {
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "新建 Webhook 失败")
    } finally {
      setSaving(false)
    }
  }

  function closeCreate() {
    // Belt and braces: the create is in flight, and its one-time secret would
    // land on a closed dialog.
    if (saving) return
    setCreating(false)
    setUrl("")
    setEventTypes(new Set())
    setCreated(null)
  }

  async function copySecret() {
    if (!created) return
    try {
      await navigator.clipboard.writeText(created.secret)
      toast.success("签名密钥已复制")
    } catch {
      toast.error("复制失败，请手动选择复制")
    }
  }

  async function setStatus(ep: WebhookEndpoint, status: "active" | "paused") {
    setBusyId(ep.id)
    try {
      await api.setWebhookEndpointStatus(ep.id, status)
      // The server zeroes consecutive_failures when resuming; mirror it or the
      // card renders 启用 next to a failure count the server already cleared.
      setEndpoints((prev) =>
        prev.map((e) =>
          e.id === ep.id
            ? { ...e, status, consecutive_failures: status === "active" ? 0 : e.consecutive_failures }
            : e,
        ),
      )
      onDataChanged?.()
      toast.success(status === "paused" ? "已暂停" : "已恢复")
    } catch (e) {
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "更新状态失败")
    } finally {
      setBusyId(null)
    }
  }

  async function remove(ep: WebhookEndpoint) {
    setBusyId(ep.id)
    try {
      await api.deleteWebhookEndpoint(ep.id)
      setEndpoints((prev) => prev.filter((e) => e.id !== ep.id))
      if (deliveriesFor === ep.id) setDeliveriesFor(null)
      onDataChanged?.()
      toast.success("已删除")
    } catch (e) {
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "删除失败")
    } finally {
      setBusyId(null)
    }
  }

  async function loadDeliveries(id: string) {
    requestedDeliveriesFor.current = id
    setDeliveriesLoading(true)
    try {
      const { deliveries } = await api.webhookDeliveries(id)
      if (requestedDeliveriesFor.current !== id) return
      setDeliveries(deliveries)
    } catch (e) {
      if (requestedDeliveriesFor.current !== id) return
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "加载投递历史失败")
      setDeliveries([])
    } finally {
      if (requestedDeliveriesFor.current === id) setDeliveriesLoading(false)
    }
  }

  async function toggleDeliveries(ep: WebhookEndpoint) {
    if (deliveriesFor === ep.id) {
      setDeliveriesFor(null)
      requestedDeliveriesFor.current = null
      return
    }
    setDeliveriesFor(ep.id)
    await loadDeliveries(ep.id)
  }

  async function replay(delivery: WebhookDelivery) {
    setBusyId(delivery.id)
    try {
      await api.replayWebhookDelivery(delivery.id)
      toast.success("已重新投递")
      // The replay rewrites the delivery in place and can auto-pause the
      // endpoint, so re-read both rather than leave the pre-replay row up.
      const id = deliveriesFor
      if (id) await loadDeliveries(id)
      await load()
    } catch (e) {
      if (e instanceof UnauthenticatedError) return
      toast.error(e instanceof Error ? e.message : "重放失败")
    } finally {
      setBusyId(null)
    }
  }

  if (!canManage) {
    return (
      <main className="grid place-items-center gap-2 px-5 py-24 text-center">
        <span className="cap">自动化</span>
        <h2 className="text-lg font-semibold">只有所有者和管理员可以配置 Webhook</h2>
        <p className="text-muted-foreground max-w-[46ch] text-sm">
          你的角色无法查看或改动这些设置。需要的话请让管理员来配置。
        </p>
      </main>
    )
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-4 px-5 py-5 pb-16">
      <div className="flex items-center gap-3">
        <div className="grid gap-0.5">
          <h2 className="text-lg font-semibold">自动化</h2>
          <p className="text-muted-foreground text-[13px]">
            新反馈、状态变更、分配和备注都能通过 Webhook 推到你自己的系统。
          </p>
        </div>
        <Button size="sm" className="ml-auto rounded-full" onClick={() => setCreating(true)}>
          <Plus /> 新建 Webhook
        </Button>
      </div>

      {loading ? (
        <div className="grid gap-3">
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-20 w-full rounded-2xl" />
          ))}
        </div>
      ) : endpoints.length === 0 ? (
        <div className="bg-card shadow-lift grid place-items-center gap-2 rounded-2xl border px-6 py-16 text-center">
          <span className="bg-panel-2 grid size-11 place-items-center rounded-xl">
            <Bell className="size-5" />
          </span>
          <span className="text-foreground text-[15px] font-semibold">还没有 Webhook 端点</span>
          <span className="text-muted-foreground max-w-[44ch] text-[13px]">
            接一个端点，新反馈进来时就会自动推送到你的系统。事件会带上签名，你可以用它校验来源。
          </span>
          <Button size="sm" className="mt-1 rounded-full" onClick={() => setCreating(true)}>
            <Plus /> 新建 Webhook
          </Button>
        </div>
      ) : (
        <div className="grid gap-3">
          {endpoints.map((ep) => {
            const expanded = deliveriesFor === ep.id
            return (
              <section key={ep.id} className="bg-card shadow-lift grid gap-3 rounded-2xl border p-4">
                <div className="flex min-w-0 items-start gap-3">
                  <div className="grid min-w-0 flex-1 gap-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <code className="truncate font-mono text-[13px]" title={ep.url}>
                        {ep.url}
                      </code>
                      <StatusPill status={ep.status} label={WEBHOOK_STATUS_LABEL[ep.status]} />
                    </div>
                    <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-[12.5px]">
                      {ep.event_types.length === 0 ? (
                        <span className="text-muted-foreground">全部事件</span>
                      ) : (
                        ep.event_types.map((t) => (
                          <Badge key={t} variant="outline" className="font-normal">
                            {WEBHOOK_EVENT_TYPE_LABEL[t]}
                          </Badge>
                        ))
                      )}
                      {ep.consecutive_failures > 0 && (
                        <span className="text-prio-urgent font-medium">
                          连续失败 {ep.consecutive_failures} 次
                        </span>
                      )}
                      <span>创建于 {formatDateTime(ep.created_at)}</span>
                    </div>
                  </div>

                  <div className="flex shrink-0 items-center gap-1.5">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="rounded-full"
                      disabled={busyId === ep.id}
                      onClick={() => toggleDeliveries(ep)}
                      aria-expanded={expanded}
                    >
                      投递历史
                      <ChevronDown className={cn("size-3.5 transition-transform", expanded && "rotate-180")} />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="rounded-full"
                      disabled={busyId === ep.id}
                      aria-label={ep.status === "active" ? "暂停" : "恢复"}
                      onClick={() => setStatus(ep, ep.status === "active" ? "paused" : "active")}
                    >
                      {ep.status === "active" ? <Pause /> : <Play />}
                    </Button>
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          className="hover:text-destructive rounded-full"
                          disabled={busyId === ep.id}
                          aria-label="删除端点"
                        >
                          <Trash2 />
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent size="sm">
                        <AlertDialogHeader>
                          <AlertDialogTitle>删除这个 Webhook？</AlertDialogTitle>
                          <AlertDialogDescription>
                            「{ep.url}」将不再收到任何事件；已有的投递记录将不再可见，并在 30 天后过期。
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>取消</AlertDialogCancel>
                          <AlertDialogAction variant="destructive" onClick={() => remove(ep)}>
                            删除
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  </div>
                </div>

                {expanded && (
                  <DeliveryHistory
                    deliveries={deliveries}
                    loading={deliveriesLoading}
                    busyId={busyId}
                    onReplay={replay}
                  />
                )}
              </section>
            )
          })}
        </div>
      )}

      <Dialog open={creating} onOpenChange={(open) => !open && closeCreate()}>
        {/*
          While `created` holds the secret this dialog is the only place it
          exists: the server stores no recoverable copy and offers no reveal
          endpoint. Leave 完成 as the single exit, exactly like the write key.
        */}
        <DialogContent
          className="max-w-sm"
          showCloseButton={!created && !saving}
          onEscapeKeyDown={(e) => (created || saving) && e.preventDefault()}
          onInteractOutside={(e) => (created || saving) && e.preventDefault()}
        >
          <DialogHeader>
            <DialogTitle>{created ? "Webhook 已创建" : "新建 Webhook"}</DialogTitle>
          </DialogHeader>
          {created ? (
            <div className="grid gap-3">
              <p className="text-muted-foreground text-sm">
                「{created.url}」已接上。下面的签名密钥只显示这一次，用它校验推送到你服务器的请求。
              </p>
              <div className="bg-panel-2 flex items-center gap-2 rounded-md border p-2">
                <code className="min-w-0 flex-1 break-all font-mono text-xs">{created.secret}</code>
                <Button variant="outline" size="sm" className="h-7 shrink-0 rounded-md" onClick={copySecret}>
                  <Copy className="size-3.5" /> 复制
                </Button>
              </div>
              <DialogFooter>
                <Button onClick={closeCreate}>完成</Button>
              </DialogFooter>
            </div>
          ) : (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void submitCreate()
              }}
              className="grid gap-3"
            >
              <div className="grid gap-1.5">
                <Label htmlFor="wh-url" className="text-muted-foreground text-xs">
                  目标 URL
                </Label>
                <Input
                  id="wh-url"
                  autoFocus
                  value={url}
                  maxLength={2048}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://api.example.com/pinpost"
                />
              </div>
              <div className="grid gap-1.5">
                <span className="text-muted-foreground text-xs">
                  订阅事件（不选则订阅全部）
                </span>
                <div className="grid gap-2">
                  {WEBHOOK_EVENT_TYPES.map((type) => (
                    <label key={type} className="flex items-center gap-2 text-sm">
                      <Checkbox
                        aria-label={WEBHOOK_EVENT_TYPE_LABEL[type]}
                        checked={eventTypes.has(type)}
                        onCheckedChange={(checked) =>
                          setEventTypes((prev) => {
                            const next = new Set(prev)
                            if (checked) next.add(type)
                            else next.delete(type)
                            return next
                          })
                        }
                      />
                      {WEBHOOK_EVENT_TYPE_LABEL[type]}
                      <code className="text-muted-foreground ml-auto font-mono text-[11px]">{type}</code>
                    </label>
                  ))}
                </div>
              </div>
              <DialogFooter>
                <Button type="submit" disabled={saving || !url.trim()}>
                  {saving ? "创建中…" : "创建"}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </main>
  )
}

/**
 * One tone map and one pill for both endpoint status and delivery status, so
 * the geometry comes from Badge rather than being hand-written per call site.
 */
const PILL_TONE: Record<WebhookStatus | WebhookDeliveryStatus, string> = {
  active: "bg-status-resolved-bg text-status-resolved",
  succeeded: "bg-status-resolved-bg text-status-resolved",
  failed: "bg-status-spam-bg text-status-spam",
  paused: "bg-panel-2 text-muted-foreground",
  pending: "bg-panel-2 text-muted-foreground",
}

function StatusPill({
  status,
  label,
}: {
  status: WebhookStatus | WebhookDeliveryStatus
  label: string
}) {
  return (
    <Badge
      variant="secondary"
      className={cn("rounded-[5px] px-1.5 py-px font-mono text-[9.5px] tracking-wider", PILL_TONE[status])}
    >
      {label}
    </Badge>
  )
}

function DeliveryHistory({
  deliveries,
  loading,
  busyId,
  onReplay,
}: {
  deliveries: WebhookDelivery[]
  loading: boolean
  busyId: string | null
  onReplay: (delivery: WebhookDelivery) => void
}) {
  if (loading) {
    return (
      <div className="grid gap-2 border-t pt-3">
        {Array.from({ length: 2 }, (_, i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    )
  }
  if (deliveries.length === 0) {
    return (
      <div className="text-muted-foreground border-t pt-3 text-[12.5px]">
        还没有投递记录 —— 事件第一次送达后会出现在这里。
      </div>
    )
  }
  return (
    <div className="grid gap-1 border-t pt-3">
      {deliveries.map((d) => (
        <div key={d.id} className="flex items-center gap-2.5 py-1">
          <span className="min-w-0 flex-1 truncate text-[12.5px]">
            {WEBHOOK_EVENT_TYPE_LABEL[d.event_type]}
            {d.attempt > 1 && (
              <span className="text-muted-foreground ml-1 font-mono text-[11px]">第 {d.attempt} 次</span>
            )}
          </span>
          {d.response_status != null && (
            <span className="tnum font-mono text-[11.5px]">{d.response_status}</span>
          )}
          {d.latency_ms != null && (
            <span className="text-muted-foreground tnum font-mono text-[11px]">{d.latency_ms}ms</span>
          )}
          <StatusPill status={d.status} label={DELIVERY_STATUS_LABEL[d.status]} />
          <span className="text-muted-foreground text-[11px]" title={formatDateTime(d.created_at)}>
            {formatRelative(d.created_at)}
          </span>
          {(d.status === "succeeded" || d.status === "failed") && (
            <Button
              variant="ghost"
              size="xs"
              className="rounded-full"
              disabled={busyId === d.id}
              aria-label="重新投递"
              onClick={() => onReplay(d)}
            >
              <RotateCcw />
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}
