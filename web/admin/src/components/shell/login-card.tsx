import { useEffect, useRef, useState } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import { AlertCircle } from "lucide-react"
import { z } from "zod"
import { api, type Member, type Organization } from "@/lib/api"
import { googleButtonWidth, loadGoogleIdentity } from "@/lib/google"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"

const LOGIN_SCHEMA = z.object({
  email: z.string().min(1, "请输入邮箱").email("邮箱格式不正确"),
  password: z.string().min(1, "请输入密码"),
})

export function LoginCard({
  onSignedIn,
}: {
  onSignedIn: (session: { member: Member; organization: Organization }) => void
}) {
  const [error, setError] = useState("")
  const [clientId, setClientId] = useState("")
  const googleSlot = useRef<HTMLDivElement>(null)
  const form = useForm<z.infer<typeof LOGIN_SCHEMA>>({
    resolver: zodResolver(LOGIN_SCHEMA),
    defaultValues: { email: "admin@example.com", password: "" },
  })

  // An empty client id means the deployment has no Google client configured;
  // the password form stays on its own in that case.
  useEffect(() => {
    api.authConfig().then(
      (c) => setClientId(c.google_client_id),
      // A failed config fetch means the API is unreachable, and the password
      // form below would fail the same way — say so instead of silently
      // presenting a form that cannot work.
      () => setError("无法连接服务器，请稍后重试"),
    )
  }, [])

  useEffect(() => {
    if (!clientId) return
    let live = true
    let observer: ResizeObserver | undefined
    loadGoogleIdentity().then(
      (google) => {
        const slot = googleSlot.current
        if (!live || !slot) return
        google.accounts.id.initialize({
          client_id: clientId,
          callback: ({ credential }) => {
            setError("")
            api.googleLogin(credential).then(
              (session) => onSignedIn(session),
              (e) => setError(e instanceof Error ? e.message : "Google 登录失败"),
            )
          },
        })
        // The button is a fixed-pixel iframe, so a viewport change leaves it
        // wider or narrower than its column until it is drawn again. Redrawing
        // only when the clamped width actually moves keeps the observer from
        // reacting to its own output.
        let drawn = 0
        const draw = () => {
          const width = googleButtonWidth(slot.offsetWidth)
          if (width === drawn) return
          drawn = width
          google.accounts.id.renderButton(slot, {
            theme: "outline",
            size: "large",
            width,
            text: "signin_with",
            locale: "zh_CN",
          })
        }
        draw()
        observer = new ResizeObserver(draw)
        observer.observe(slot)
      },
      () => live && setError("无法加载 Google 登录，请检查网络"),
    )
    return () => {
      live = false
      observer?.disconnect()
    }
  }, [clientId, onSignedIn])

  async function submit(values: z.infer<typeof LOGIN_SCHEMA>) {
    setError("")
    try {
      onSignedIn(await api.login(values.email, values.password))
    } catch (e) {
      setError(e instanceof Error ? e.message : "登录失败，请重试")
    }
  }

  return (
    <div className="grid min-h-dvh lg:grid-cols-2">
      {/* Brand panel: pure decoration, so it is the half that goes away when
          there is no room for both columns — and aria-hidden, so nothing here
          is the only copy of something a narrow viewport still needs. The
          gradient tokens are defined once in index.css and never in .dark, so
          the white text on them reads in either theme. */}
      <div
        aria-hidden
        className="hidden flex-col justify-between bg-[linear-gradient(160deg,var(--brand-panel-from),var(--brand-panel-to))] p-14 text-white lg:flex"
      >
        <div className="flex items-center gap-2.5 text-[15px] font-semibold">
          <span className="grid size-6 place-items-center rounded-md bg-white/20 text-xs font-bold">
            F
          </span>
          Pinpost
        </div>
        <div>
          <p className="text-[32px] leading-[1.3] font-semibold tracking-tight">
            把用户反馈
            <br />
            变成可执行的事项
          </p>
          <p className="mt-4 max-w-90 text-sm leading-[22px] text-white/85">
            收集、分派、跟踪。一个后台看完所有渠道进来的反馈。
          </p>
        </div>
        <p className="text-xs text-white/75">Pinpost · open-source feedback</p>
      </div>

      <div className="flex items-center justify-center p-6 lg:p-12">
        <div className="grid w-full max-w-90 gap-6">
          <div>
            {/* Below lg the panel is gone, so this is the only thing naming the
                product — a login page that does not say what it is signing you
                into is indistinguishable from a phishing page. */}
            <div className="mb-5 flex items-center gap-2.5 text-[15px] font-semibold lg:hidden">
              <span className="bg-primary text-primary-foreground grid size-6 place-items-center rounded-md text-xs font-bold">
                F
              </span>
              Pinpost
            </div>
            <h1 className="text-2xl font-semibold tracking-tight">欢迎回来</h1>
            <p className="text-muted-foreground mt-1.5 text-sm">用你的团队成员账号登录。</p>
          </div>
          {clientId && (
            <div className="grid gap-4">
              <div ref={googleSlot} className="flex justify-center" />
              <div className="text-muted-foreground flex items-center gap-3 text-xs">
                <Separator className="flex-1" />
                或使用密码登录
                <Separator className="flex-1" />
              </div>
            </div>
          )}
          <Form {...form}>
            {/* noValidate: without it the browser runs constraint validation on
                type="email" before React sees the submit, so the native bubble
                still pre-empts the zod message. */}
            <form noValidate onSubmit={form.handleSubmit(submit)} className="grid gap-4">
              <FormField
                control={form.control}
                name="email"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>邮箱</FormLabel>
                    <FormControl>
                      <Input type="email" autoComplete="username" {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name="password"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>密码</FormLabel>
                    <FormControl>
                      <Input type="password" autoComplete="current-password" {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {error && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? "登录中…" : "登录"}
              </Button>
            </form>
          </Form>
          <p className="text-muted-foreground text-center text-xs">仅限白名单邮箱访问。</p>
        </div>
      </div>
    </div>
  )
}
