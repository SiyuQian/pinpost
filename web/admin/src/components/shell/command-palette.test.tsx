// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { FeedbackListItem } from "@/lib/api"
import { CommandPalette } from "@/components/shell/command-palette"

/**
 * cmdk tracks the highlight by an item's `value` string, not by its React key,
 * so the id has to be part of the value: two reports with the same message and
 * tags are otherwise one value to cmdk, and Enter opens whichever it resolved
 * first. This pins the disambiguation — it fails if the id leaves the value.
 */

beforeAll(() => {
  // cmdk scrolls the highlighted item into view and observes the list's size;
  // jsdom has neither layout nor ResizeObserver.
  Element.prototype.scrollIntoView = vi.fn()
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

afterEach(cleanup)

function row(id: string): FeedbackListItem {
  return {
    id,
    type: "bug",
    message: "崩溃",
    source: "widget",
    status: "open",
    priority: "none",
    assignee_id: null,
    tags: ["crash"],
    project_id: "p1",
    created_at: new Date().toISOString(),
    updated_at: "2026-08-24T09:12:00Z",
  }
}

describe("CommandPalette", () => {
  it("opens the second of two identical-looking reports, not the first", () => {
    const onSelect = vi.fn()
    render(
      <CommandPalette
        open
        onOpenChange={() => {}}
        items={[row("fb_1"), row("fb_2")]}
        onSelect={onSelect}
        onSearch={() => {}}
      />,
    )

    const input = screen.getByPlaceholderText("搜索反馈内容、标签…")
    fireEvent.keyDown(input, { key: "ArrowDown" })
    fireEvent.keyDown(input, { key: "Enter" })

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith("fb_2")
  })
})
