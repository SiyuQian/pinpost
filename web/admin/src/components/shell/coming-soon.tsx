import { toast } from "sonner"

/**
 * Placeholder handler for the console chrome that is drawn but not wired yet
 * (project settings, integrations, invites…). A toast rather than a disabled
 * control: the entry is real product surface, it just has no backend behind it,
 * and saying so beats a dead click.
 */
export function comingSoon() {
  toast("这块还没接上", { description: "界面先占位，后端接口补上后就能用。" })
}
