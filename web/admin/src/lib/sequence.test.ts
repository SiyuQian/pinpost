import { describe, expect, it } from "vitest"
import { RequestSequence } from "@/lib/sequence"

/**
 * The failure this prevents: change a filter while 加载更多 is in flight, and
 * the old page appends under the new filter with its cursor overwriting the
 * new one — so the list silently stops matching the query it claims to show.
 */
describe("RequestSequence", () => {
  it("keeps only the newest request", () => {
    const seq = new RequestSequence()
    const first = seq.next()
    const second = seq.next()

    expect(seq.isCurrent(first)).toBe(false)
    expect(seq.isCurrent(second)).toBe(true)
  })

  it("drops a page that resolves after the filter changed", async () => {
    const seq = new RequestSequence()
    const rows: string[] = []
    const load = async (label: string, delay: number) => {
      const ticket = seq.next()
      await new Promise((r) => setTimeout(r, delay))
      if (!seq.isCurrent(ticket)) return
      rows.push(label)
    }

    // The slow first query is superseded by a fast second one.
    await Promise.all([load("stale", 20), load("fresh", 1)])

    expect(rows).toEqual(["fresh"])
  })

  it("lets a follow-up page reuse the ticket of the query it continues", async () => {
    const seq = new RequestSequence()
    seq.next() // initial page
    const ticket = seq.latest() // 加载更多 follows the same query

    await Promise.resolve()
    expect(seq.isCurrent(ticket)).toBe(true)

    seq.next() // a filter change supersedes both
    expect(seq.isCurrent(ticket)).toBe(false)
  })
})
