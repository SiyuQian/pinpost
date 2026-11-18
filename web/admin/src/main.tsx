import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import App from "@/App"
import "@/index.css"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {/* The native title= this replaced had the browser's ~1s delay;
        0 would fire a tooltip on every pass over a 50-row table. */}
    <TooltipProvider delayDuration={500}>
      <App />
    </TooltipProvider>
    <Toaster richColors position="bottom-right" />
  </StrictMode>,
)
