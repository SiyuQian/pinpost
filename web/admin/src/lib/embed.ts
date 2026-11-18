/**
 * Install snippets for the 嵌入与集成 page. One per SDK package, each a
 * complete file a developer pastes as-is — the write key, API origin and
 * environment are already filled in, so the only edit left is where the
 * widget goes in their tree.
 *
 * `apiUrl` is this console's own origin: the API and the console are served
 * from the same host in every environment, and the SDK's built-in default is
 * localhost, which is wrong everywhere but a laptop.
 */
export type Platform = "react" | "react-native" | "headless"

export const PLATFORMS: { key: Platform; label: string; pkg: string; file: string }[] = [
  { key: "react", label: "React", pkg: "@pinpost/react", file: "App.tsx" },
  { key: "react-native", label: "React Native", pkg: "@pinpost/react-native", file: "App.tsx" },
  { key: "headless", label: "无 UI（core）", pkg: "@pinpost/core", file: "pinpost.ts" },
]

export interface EmbedConfig {
  writeKey: string
  apiUrl: string
  environment: string
}

export function installCommand(platform: Platform) {
  return `npm install ${PLATFORMS.find((p) => p.key === platform)!.pkg}`
}

export function snippet(platform: Platform, { writeKey, apiUrl, environment }: EmbedConfig) {
  const props = `writeKey=${JSON.stringify(writeKey)} apiUrl=${JSON.stringify(apiUrl)} environment=${JSON.stringify(environment)}`
  switch (platform) {
    case "react":
      return `import { PinpostProvider, FeedbackWidget } from "@pinpost/react"

export function App() {
  return (
    <PinpostProvider ${props}>
      {/* 你的应用 */}
      <FeedbackWidget />
    </PinpostProvider>
  )
}
`
    case "react-native":
      return `import { PinpostProvider, FeedbackWidget } from "@pinpost/react-native"

export default function App() {
  return (
    <PinpostProvider ${props}>
      {/* 你的应用 */}
      <FeedbackWidget />
    </PinpostProvider>
  )
}
`
    case "headless":
      return `import { createClient } from "@pinpost/core"

export const pinpost = createClient({
  writeKey: ${JSON.stringify(writeKey)},
  apiUrl: ${JSON.stringify(apiUrl)},
  environment: ${JSON.stringify(environment)},
})

// 登录后可选：把反馈关联到用户
// pinpost.identify({ externalUserId: user.id, email: user.email })

await pinpost.submit({ type: "bug", message: "描述问题…" })
`
  }
}
