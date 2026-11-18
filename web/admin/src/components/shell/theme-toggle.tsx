import { useEffect, useState } from "react"
import { Moon, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"

const KEY = "pinpost-theme"

/** Mirrors the pre-paint script in index.html; persists the explicit choice. */
export function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"))

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark)
    localStorage.setItem(KEY, dark ? "dark" : "light")
  }, [dark])

  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setDark((d) => !d)}
      aria-label={dark ? "切换到浅色主题" : "切换到深色主题"}
    >
      {dark ? <Sun /> : <Moon />}
    </Button>
  )
}
