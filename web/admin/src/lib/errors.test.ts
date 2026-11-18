import { describe, expect, it } from "vitest"
import { ApiError, UnauthenticatedError } from "@/lib/api"
import { classifyWriteError } from "@/lib/errors"

describe("classifyWriteError", () => {
  it("asks for a reload when a concurrent edit won the race", () => {
    // Without the reload the stale row stays on screen with its stale
    // updated_at, and every retry fails the same way.
    const result = classifyWriteError(new ApiError(409, "conflict", "stale"))

    expect(result.reload).toBe(true)
    expect(result.message).toContain("刚被其他人改过")
  })

  it("passes any other failure through as its own message", () => {
    expect(classifyWriteError(new ApiError(422, "invalid", "状态不允许"))).toEqual({
      reload: false,
      message: "状态不允许",
      description: undefined,
    })
    expect(classifyWriteError(new UnauthenticatedError()).reload).toBe(false)
    expect(classifyWriteError("not an error").message).toBe("保存失败")
  })
})
