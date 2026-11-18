/** Minimal surface of Google Identity Services, loaded on demand. */
type GoogleIdentity = {
  accounts: {
    id: {
      initialize(o: { client_id: string; callback: (r: { credential: string }) => void }): void
      renderButton(el: HTMLElement, o: Record<string, string | number>): void
    }
  }
}

declare global {
  interface Window {
    google?: GoogleIdentity
  }
}

/**
 * Google bakes a pixel width into the rendered button — it does not accept
 * "100%" and rejects anything outside 200-400 — so a measured column width has
 * to be clamped before it is handed over. A 0 here means the slot has not been
 * laid out yet, which clamps up to the 200 minimum rather than rendering
 * nothing.
 */
export function googleButtonWidth(offsetWidth: number): number {
  return Math.min(400, Math.max(200, offsetWidth))
}

const SRC = "https://accounts.google.com/gsi/client"
let pending: Promise<GoogleIdentity> | undefined

/**
 * Loads the Google script the first time it is needed. Deployments without a
 * Google client never make the third-party request, and React strict mode's
 * double effect reuses the same promise instead of injecting two tags.
 */
export function loadGoogleIdentity(): Promise<GoogleIdentity> {
  if (window.google) return Promise.resolve(window.google)
  pending ??= new Promise<GoogleIdentity>((resolve, reject) => {
    const tag = document.createElement("script")
    tag.src = SRC
    tag.async = true
    tag.onload = () =>
      window.google ? resolve(window.google) : reject(new Error("google identity unavailable"))
    tag.onerror = () => {
      pending = undefined // let a later attempt retry a transient network failure
      reject(new Error("google identity failed to load"))
    }
    document.head.append(tag)
  })
  return pending
}
