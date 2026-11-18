import { ApiError } from "@/lib/api"

/**
 * How a failed write should be surfaced to the list. A lost
 * optimistic-concurrency race is the one case that needs more than a message:
 * the row on screen is stale, so it has to be reloaded or the next click fails
 * identically. Split out of the component so the branch is covered by a test
 * rather than by a review note.
 *
 * The detail sheet makes the same distinction but refetches the one record it
 * is showing (feedback-sheet.tsx), which is a different remedy for the same
 * cause — worth keeping in step by hand if either side's wording changes.
 */
export function classifyWriteError(error: unknown) {
  if (error instanceof ApiError && error.isConflict) {
    return {
      reload: true,
      message: "这条反馈刚被其他人改过",
      description: "已为你载入最新内容，请确认后重试。",
    }
  }
  return {
    reload: false,
    message: error instanceof Error ? error.message : "保存失败",
    description: undefined,
  }
}
