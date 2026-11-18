import { useEffect } from "react"
import type { FeedbackListItem } from "@/lib/api"
import { formatRelative } from "@/lib/format"
import { STATUS_LABEL, TYPE_LABEL } from "@/lib/labels"
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"

/**
 * ⌘K search. Typing does two things: cmdk narrows the candidates below, and the
 * query goes to `onSearch`, which is debounced into the list's server-side `q`
 * — this is the console's only search entry, so it has to reach rows that are
 * not loaded yet.
 *
 * The candidates are whatever the list currently holds, so a server-side match
 * that arrives while the dialog is open is selectable — that is the case this
 * exists for. cmdk tracks the highlight by an item's `value` string, not by its
 * React key, so the id is part of the value: two reports with the same message
 * and tags (a repeated template submission, several 崩溃 reports) would
 * otherwise be indistinguishable, and Enter could open the wrong one.
 *
 * The query deliberately survives closing — the toolbar renders it as a chip
 * with a clear button, so a filtered list always says why.
 */
export function CommandPalette({
  open,
  onOpenChange,
  items,
  onSelect,
  onSearch,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  items: FeedbackListItem[]
  onSelect: (id: string) => void
  onSearch: (query: string) => void
}) {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        onOpenChange(!open)
      }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [open, onOpenChange])

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="搜索反馈"
      description="搜索反馈，回车打开详情"
    >
      <CommandInput placeholder="搜索反馈内容、标签…" onValueChange={onSearch} />
      <CommandList>
        <CommandEmpty>没有匹配的反馈。</CommandEmpty>
        <CommandGroup heading="反馈">
          {items.slice(0, 50).map((item) => (
            <CommandItem
              key={item.id}
              value={`${item.message} ${item.tags.join(" ")} ${item.id}`}
              onSelect={() => {
                onSelect(item.id)
                onOpenChange(false)
              }}
            >
              <span className="truncate">{item.message}</span>
              <span className="text-muted-foreground ml-auto shrink-0 font-mono text-[11px]">
                {TYPE_LABEL[item.type]} · {STATUS_LABEL[item.status]} · {formatRelative(item.created_at)}
              </span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
