// @vitest-environment jsdom
// downloadBlob is all DOM lifecycle, so this one file runs against jsdom;
// every other spec in lib/ is pure and stays on the default node environment.
import { afterEach, describe, expect, it, vi } from "vitest"
import { downloadBlob } from "@/lib/download"

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("downloadBlob", () => {
  it("attaches the anchor before clicking and cleans it up after", () => {
    // Firefox ignores a click on an anchor that is not in the document.
    let connectedAtClick = false
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        connectedAtClick = this.isConnected
      })
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test")
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {})

    downloadBlob(new Blob(["a,b"], { type: "text/csv" }), "rows.csv")

    expect(click).toHaveBeenCalledOnce()
    expect(connectedAtClick).toBe(true)
    expect(document.querySelector("a[download]")).toBeNull()
  })

  it("revokes the object URL on a later tick, not the same one", () => {
    vi.useFakeTimers()
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test")
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {})

    downloadBlob(new Blob(["a,b"]), "rows.csv")

    // Revoking in the same task truncates the transfer in Firefox and Safari.
    expect(revoke).not.toHaveBeenCalled()
    vi.runAllTimers()
    expect(revoke).toHaveBeenCalledWith("blob:test")
  })
})
