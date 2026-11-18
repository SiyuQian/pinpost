/**
 * Guards a list against results from a query it has already moved on from.
 * Every fetch takes a ticket before it starts and checks it after the await:
 * a page that resolves late is dropped instead of being appended under the new
 * filter, and its cursor never replaces the current one.
 *
 * It lives here rather than inline in the component so the interleaving can be
 * tested — a stale append is invisible until someone reports rows that do not
 * match the filter they are looking at.
 */
export class RequestSequence {
  private current = 0

  /** Supersede any in-flight request and take a ticket for the new one. */
  next() {
    return ++this.current
  }

  /** The ticket for the request in flight, for callers that only follow one. */
  latest() {
    return this.current
  }

  isCurrent(ticket: number) {
    return ticket === this.current
  }
}
