import { Check, PlusCircle } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Separator } from "@/components/ui/separator"
import { cn } from "@/lib/utils"

export interface FacetOption {
  value: string
  label: string
  count?: number
}

/**
 * Single-select facet in the shape of shadcn's faceted filter. The API takes
 * one value per filter (status/type/assignee_id), so selecting an option
 * replaces the current one instead of accumulating.
 */
export function FacetedFilter({
  title,
  options,
  value,
  onChange,
}: {
  title: string
  options: FacetOption[]
  value?: string
  onChange: (value?: string) => void
}) {
  const selected = options.find((o) => o.value === value)

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="border-dashed">
          <PlusCircle />
          {title}
          {selected && (
            <>
              <Separator orientation="vertical" className="mx-0.5 data-[orientation=vertical]:h-4" />
              <Badge variant="secondary" className="rounded-sm px-1 font-normal">
                {selected.label}
              </Badge>
            </>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-52 p-0" align="start">
        <Command>
          <CommandInput placeholder={title} />
          <CommandList>
            <CommandEmpty>无匹配项</CommandEmpty>
            <CommandGroup>
              {options.map((option) => {
                const isSelected = option.value === value
                return (
                  <CommandItem
                    key={option.value}
                    onSelect={() => onChange(isSelected ? undefined : option.value)}
                  >
                    <div
                      className={cn(
                        "border-primary flex size-4 items-center justify-center rounded-sm border",
                        isSelected ? "bg-primary text-primary-foreground" : "opacity-50",
                      )}
                    >
                      <Check className="size-3" />
                    </div>
                    <span className="truncate">{option.label}</span>
                    {option.count !== undefined && (
                      <span className="tnum text-muted-foreground ml-auto text-xs">{option.count}</span>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {selected && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem onSelect={() => onChange(undefined)} className="justify-center">
                    清除筛选
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
