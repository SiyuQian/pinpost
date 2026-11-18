import { cn } from "@/lib/utils"

/**
 * One row in the sidebar. A button rather than an anchor: the console is a
 * single view and every entry changes state in place, so there is no href to
 * give and nothing for a middle-click to open.
 */
export function NavItem({
  icon,
  children,
  count,
  trailing,
  active,
  onClick,
}: {
  icon?: React.ReactNode
  children: React.ReactNode
  count?: number
  trailing?: React.ReactNode
  active?: boolean
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-[13.5px] font-medium transition-colors",
        "focus-visible:ring-ring/50 outline-none focus-visible:ring-[3px]",
        active
          ? "bg-primary/10 text-primary"
          : "text-muted-foreground hover:bg-panel-2 hover:text-foreground",
      )}
    >
      {icon && <span className="grid w-4 shrink-0 place-items-center">{icon}</span>}
      <span className="truncate">{children}</span>
      {trailing}
      {count !== undefined && (
        <span
          className={cn(
            "tnum ml-auto font-mono text-[11px]",
            active ? "text-primary" : "text-muted-foreground",
          )}
        >
          {count}
        </span>
      )}
    </button>
  )
}

/** Section heading inside the sidebar; optional trailing action (e.g. "＋"). */
export function NavGroup({
  title,
  action,
  children,
}: {
  title: string
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="grid gap-px">
      <div className="flex items-center gap-2 px-2.5 pb-1.5">
        <span className="cap">{title}</span>
        {action}
      </div>
      {children}
    </div>
  )
}
