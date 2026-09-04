import { useCallback, useEffect, useState } from "react"
import { motion } from "motion/react"
import { Meetings, Status, type Density, type Settings, type SetupState } from "./api"
import Setup from "./screens/Setup"
import Today from "./screens/Today"
import Library from "./screens/Library"
import Search from "./screens/Search"
import Todo from "./screens/Todo"
import Ask from "./screens/Ask"
import SettingsScreen from "./screens/Settings"
import Transcript from "./screens/Transcript"
import Rail, { type Screen } from "./components/Rail"

export default function App() {
  const [setup, setSetup] = useState<SetupState | null>(null)
  const [screen, setScreen] = useState<Screen>("today")
  const [open, setOpen] = useState<number | null>(null)
  const [density, setDensity] = useState<Density>("compact")
  const [refresh, setRefresh] = useState(0)

  // Polled rather than pushed: one small object once a second, and an event
  // channel for it would be more machinery than the thing it carries.
  useEffect(() => {
    let alive = true
    const read = async () => {
      try {
        const state = (await Status.State()) as SetupState
        if (alive) setSetup(state)
      } catch {
        // The window can outlive a reload in development; a failed poll is not
        // worth a red screen.
      }
    }
    read()
    const timer = setInterval(read, 1000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [])

  // The density lives with the rest of the settings so that it survives a
  // reinstall and travels with the folder, rather than in browser storage.
  useEffect(() => {
    Meetings.Settings()
      .then((s) => setDensity(((s as { density?: Density }).density ?? "compact") as Density))
      .catch(() => {})
  }, [])

  const changeDensity = useCallback(async (d: Density) => {
    setDensity(d)
    try {
      const current = (await Meetings.Settings()) as Settings
      await Meetings.SaveSettings({ ...current, density: d } as never)
    } catch {
      // A preference that failed to save is not worth interrupting anybody for.
    }
  }, [])

  const working = setup?.stage === "ready"

  // ⌘1..4 and ⌘, move between screens, ⌘K goes straight to search. Every rail
  // button carries its own shortcut in the tooltip.
  useEffect(() => {
    if (!working) return
    const jump = (e: KeyboardEvent) => {
      if (!e.metaKey && !e.ctrlKey) return
      const to: Record<string, Screen> = {
        "1": "today", "2": "library", "3": "search", "4": "todo", "5": "ask",
        ",": "settings", k: "search",
      }
      const next = to[e.key.toLowerCase()]
      if (!next) return
      e.preventDefault()
      setOpen(null)
      setScreen(next)
    }
    window.addEventListener("keydown", jump)
    return () => window.removeEventListener("keydown", jump)
  }, [working])

  const show = (id: number) => {
    setOpen(id)
    setScreen("library")
  }

  return (
    <div className="flex h-full flex-col bg-ink/80">
      <div className="titlebar shrink-0" />

      {/* No AnimatePresence around this one. It switches once per launch, and
          mode="wait" holds the incoming screen until every animation in the
          outgoing subtree has finished — which deadlocked here and left a black
          window with the app mounted behind an invisible setup screen. A plain
          conditional cannot do that. */}
      {!working ? (
        <div className="flex-1">
          <Setup state={setup} />
        </div>
      ) : (
        <motion.div
          className="flex min-h-0 flex-1"
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, ease: [0.22, 1, 0.36, 1] }}
        >
          <Rail
            screen={screen}
            onChange={(s) => {
              setOpen(null)
              setScreen(s)
            }}
          />
          {/* Keyed, but deliberately not wrapped in AnimatePresence. An exit
              animation here would mean mode="wait", and that holds the incoming
              screen until every animation in the outgoing one has finished —
              including the pulse on a recording that is still being processed,
              which repeats for ever. The screen simply stopped changing. The
              new screen animating in is the whole effect anyway. */}
          <main className="min-w-0 flex-1">
              <motion.div
                key={open !== null ? `meeting-${open}` : screen}
                className="h-full"
                initial={{ opacity: 0, y: 10 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
              >
                {open !== null ? (
                  <Transcript
                    id={open}
                    density={density}
                    onDensity={changeDensity}
                    onBack={() => setOpen(null)}
                    onChanged={() => setRefresh((n) => n + 1)}
                  />
                ) : (
                  <>
                    {screen === "today" && <Today onOpen={show} />}
                    {screen === "library" && <Library key={refresh} onOpen={setOpen} />}
                    {screen === "search" && <Search onOpen={show} />}
                    {screen === "todo" && <Todo onOpen={show} />}
                    {screen === "ask" && <Ask onOpen={show} />}
                    {screen === "settings" && <SettingsScreen onDensity={setDensity} />}
                  </>
                )}
              </motion.div>
          </main>
        </motion.div>
      )}
    </div>
  )
}
