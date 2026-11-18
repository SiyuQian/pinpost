import { useEffect, useState } from "react"
import { ArrowRight, Check, Copy, KeyRound, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { api, type Project } from "@/lib/api"
import { PLATFORMS, installCommand, snippet, type Platform } from "@/lib/embed"
import { EnvBadge } from "@/components/shell/workspace-sidebar"
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

/** Polling cadence while waiting for the first submission to land. */
const FIRST_FEEDBACK_POLL_MS = 5000

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error("复制失败，请手动选择复制")
    }
  }
  return (
    <Button variant="outline" size="sm" className="h-7 shrink-0 rounded-md" onClick={copy} aria-label={label}>
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      {copied ? "已复制" : "复制"}
    </Button>
  )
}

function CodeBlock({ code, copyLabel }: { code: string; copyLabel: string }) {
  return (
    <div className="bg-panel-2 relative rounded-md border">
      <pre className="overflow-x-auto p-3 pr-24 font-mono text-[12px] leading-relaxed">
        <code>{code}</code>
      </pre>
      <span className="absolute top-2 right-2">
        <CopyButton text={code} label={copyLabel} />
      </span>
    </div>
  )
}

/**
 * 嵌入与集成: the project's write key, the install snippet with it filled
 * in, and a live "waiting for the first submission" check. The write key is a
 * public identifier (design §9.1), so it is shown in full — the one control
 * that needs care is rotation, which locks out every SDK holding the old key,
 * and that sits behind a confirmation and an owner/admin gate.
 */
export function EmbedPage({
  projects,
  projectId,
  canManage,
  onProjectsChanged,
  onOpenAutomation,
  webhookCount,
}: {
  projects: Project[]
  /** The project the sidebar has selected, if any; defaults to the first. */
  projectId?: string
  canManage: boolean
  /** Called after a rotation, so the workspace lists refetch the new key. */
  onProjectsChanged: () => void
  onOpenAutomation: () => void
  webhookCount: number
}) {
  const [chosenId, setChosenId] = useState<string | undefined>(projectId)
  const project = projects.find((p) => p.ID === chosenId) ?? projects[0]
  const [platform, setPlatform] = useState<Platform>("react")
  const [rotating, setRotating] = useState(false)
  // Filled by a rotation in this session, so the new key shows before the
  // workspace refetch lands.
  const [rotatedKey, setRotatedKey] = useState<{ id: string; key: string } | null>(null)
  const [received, setReceived] = useState<number | null>(null)

  const writeKey = rotatedKey?.id === project?.ID ? rotatedKey.key : project?.WriteKey
  // The console and the API share an origin in every environment.
  const apiUrl = window.location.origin

  // Poll the project's total until the first submission lands, then stop:
  // the snippet page is where someone sits while wiring the SDK, and "it
  // worked" should not need a manual refresh.
  useEffect(() => {
    if (!project) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const tick = async () => {
      try {
        const { total } = await api.overview(project.ID)
        if (cancelled) return
        setReceived(total)
        if (total === 0) timer = setTimeout(tick, FIRST_FEEDBACK_POLL_MS)
      } catch {
        // Transient; the next mount retries.
      }
    }
    setReceived(null)
    void tick()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [project?.ID])

  const rotate = async () => {
    if (!project || rotating) return
    setRotating(true)
    try {
      const p = await api.rotateWriteKey(project.ID)
      setRotatedKey({ id: p.ID, key: p.WriteKey })
      toast.success("写密钥已更换", { description: "把新密钥填进 SDK 后重新发布，旧密钥已经失效。" })
      onProjectsChanged()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "更换写密钥失败")
    } finally {
      setRotating(false)
    }
  }

  if (!project) {
    return (
      <main className="grid place-items-center gap-2 px-5 py-24 text-center">
        <span className="cap">嵌入与集成</span>
        <h2 className="text-lg font-semibold">先新建一个项目</h2>
        <p className="text-muted-foreground max-w-[46ch] text-sm">
          每个项目有自己的写密钥和嵌入代码。在左侧「项目」旁点「＋」新建。
        </p>
      </main>
    )
  }

  const chip = (active: boolean) =>
    cn(
      "focus-visible:ring-ring/50 inline-flex h-[30px] items-center rounded-full border px-3 text-[13px] outline-none focus-visible:ring-[3px]",
      active ? "border-primary bg-primary/10 text-primary font-semibold" : "bg-card text-muted-foreground hover:text-foreground",
    )

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-4 px-5 py-5 pb-16">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0">
          <span className="cap">工作区</span>
          <h2 className="text-lg font-semibold">嵌入与集成</h2>
          <p className="text-muted-foreground text-sm">把反馈组件装进你的应用，用户提交的反馈就会出现在收件箱。</p>
        </div>
        {projects.length > 1 && (
          <Select value={project.ID} onValueChange={setChosenId}>
            <SelectTrigger size="sm" className="ml-auto h-8 rounded-full" aria-label="选择项目">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {projects.map((p) => (
                <SelectItem key={p.ID} value={p.ID}>
                  {p.Name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>

      {/* Write key */}
      <section className="bg-card shadow-lift grid gap-3 rounded-2xl border p-4">
        <div className="flex items-center gap-2">
          <KeyRound className="text-muted-foreground size-4" />
          <span className="text-[13px] font-semibold">写密钥 · {project.Name}</span>
          <EnvBadge environment={project.Environment} />
          {canManage && (
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="ghost" size="sm" className="ml-auto rounded-full" disabled={rotating}>
                  <RefreshCw className={cn("size-3.5", rotating && "animate-spin")} />
                  {writeKey ? "更换密钥" : "生成密钥"}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent size="sm">
                <AlertDialogHeader>
                  <AlertDialogTitle>{writeKey ? "更换这个项目的写密钥？" : "为这个项目生成写密钥？"}</AlertDialogTitle>
                  <AlertDialogDescription>
                    {writeKey
                      ? "旧密钥会立刻失效：所有还在用它的 App 和网站都会提交失败，直到换上新密钥重新发布。密钥泄露或被滥用时才需要这么做。"
                      : "这个项目创建时没有把密钥存下来。生成一次后，密钥会一直显示在这里；旧密钥（如果还在用）会失效。"}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction onClick={() => void rotate()}>
                    {writeKey ? "更换并让旧密钥失效" : "生成密钥"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
        </div>

        {writeKey ? (
          <div className="bg-panel-2 flex items-center gap-2 rounded-md border p-2">
            <code className="min-w-0 flex-1 break-all font-mono text-xs" data-testid="write-key">
              {writeKey}
            </code>
            <CopyButton text={writeKey} label="复制写密钥" />
          </div>
        ) : (
          <p className="text-muted-foreground text-[13px]">
            这个项目的写密钥没有存下来（旧版本创建的项目）。
            {canManage ? "点「生成密钥」拿到一个新的。" : "请让所有者或管理员生成一次。"}
          </p>
        )}

        <p className="text-muted-foreground text-[12.5px]">
          写密钥是公开标识，会随你的前端代码一起发布；它只能提交反馈，读不到任何数据。
        </p>

        <div className="flex items-center gap-2 text-[12.5px]" aria-live="polite">
          {received === null ? (
            <span className="text-muted-foreground">正在检查…</span>
          ) : received > 0 ? (
            <>
              <span className="bg-status-resolved size-2 rounded-full" />
              <span className="text-status-resolved font-medium">已接通 · 收到 {received} 条反馈</span>
            </>
          ) : (
            <>
              <span className="bg-status-progress size-2 animate-pulse rounded-full" />
              <span className="text-muted-foreground">等待第一条反馈… 装好后提交一条试试，这里会自动更新。</span>
            </>
          )}
        </div>
      </section>

      {/* Install */}
      <section className="bg-card shadow-lift grid gap-3 rounded-2xl border p-4">
        <span className="text-[13px] font-semibold">安装</span>
        <div className="flex flex-wrap gap-1.5" aria-label="平台">
          {PLATFORMS.map((p) => (
            <button
              key={p.key}
              type="button"
              aria-pressed={platform === p.key}
              className={chip(platform === p.key)}
              onClick={() => setPlatform(p.key)}
            >
              {p.label}
            </button>
          ))}
        </div>
        <CodeBlock code={installCommand(platform)} copyLabel="复制安装命令" />
        <span className="text-muted-foreground text-[12.5px]">
          {PLATFORMS.find((p) => p.key === platform)!.file}
          {writeKey ? "" : " —— 生成写密钥后这里会自动填上"}
        </span>
        <CodeBlock
          code={snippet(platform, {
            writeKey: writeKey ?? "<写密钥>",
            apiUrl,
            environment: project.Environment,
          })}
          copyLabel="复制嵌入代码"
        />
      </section>

      {/* Integrations */}
      <section className="bg-card shadow-lift grid gap-3 rounded-2xl border p-4">
        <span className="text-[13px] font-semibold">集成</span>
        <button
          type="button"
          onClick={onOpenAutomation}
          className="bg-panel-2 hover:bg-panel-3 focus-visible:ring-ring/50 flex items-center gap-3 rounded-md border p-3 text-left outline-none focus-visible:ring-[3px]"
        >
          <span className="min-w-0 flex-1">
            <span className="block text-[13px] font-semibold">Webhook</span>
            <span className="text-muted-foreground block text-[12.5px]">
              新反馈、状态变更等事件带签名推送到你的接口。
            </span>
          </span>
          <span
            className={cn(
              "tnum inline-flex items-center gap-1 font-mono text-[11.5px]",
              webhookCount > 0 ? "text-status-resolved" : "text-muted-foreground",
            )}
          >
            {webhookCount > 0 ? (
              `${webhookCount} 个在用`
            ) : (
              <>
                去配置
                <ArrowRight className="size-3" />
              </>
            )}
          </span>
        </button>
        <p className="text-muted-foreground text-[12.5px]">
          Slack、GitHub、Linear 和企业微信还没接。需要的话先用 Webhook 转发到你自己的服务。
        </p>
      </section>
    </main>
  )
}
