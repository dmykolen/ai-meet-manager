import { motion } from "motion/react"
import { AudioLines, ListChecks, Search, Settings2, Sparkles, Sunrise } from "lucide-react"
import Ear from "./Ear"

export type Screen = "today" | "library" | "search" | "todo" | "ask" | "settings"

const screens = [
  { id: "today", label: "Сьогодні", Icon: Sunrise, key: "1" },
  { id: "library", label: "Записи", Icon: AudioLines, key: "2" },
  { id: "search", label: "Пошук", Icon: Search, key: "3" },
  { id: "todo", label: "Зобовʼязання", Icon: ListChecks, key: "4" },
  { id: "ask", label: "Запитати", Icon: Sparkles, key: "5" },
  { id: "settings", label: "Налаштування", Icon: Settings2, key: "," },
] as const satisfies readonly { id: Screen; label: string; Icon: typeof Search; key: string }[]

/**
 * The left rail. Five places, always visible, never a hamburger.
 *
 * Each carries its shortcut in the tooltip rather than in the label — the badge
 * that teaches you the keyboard without taking up room once you know it.
 */
export default function Rail({ screen, onChange }: { screen: Screen; onChange: (s: Screen) => void }) {
  return (
    <nav className="no-drag flex w-[78px] shrink-0 flex-col items-center gap-0.5 border-r border-line/60 px-2 pt-2">
      {screens.map(({ id, label, Icon, key }) => {
        const active = id === screen
        return (
          <button
            key={id}
            onClick={() => onChange(id)}
            title={`${label}   ⌘${key}`}
            className="group relative flex w-full flex-col items-center gap-1 rounded-xl px-1 py-2.5"
          >
            {active && (
              <motion.div
                layoutId="rail-active"
                className="absolute inset-0 rounded-xl bg-raised"
                transition={{ type: "spring", stiffness: 480, damping: 38 }}
              />
            )}
            <Icon
              size={18}
              strokeWidth={1.75}
              className={`relative z-10 transition-colors ${
                active ? "text-accent" : "text-faint group-hover:text-soft"
              }`}
            />
            <span
              className={`relative z-10 w-full truncate text-center text-[9.5px] font-medium transition-colors ${
                active ? "text-text" : "text-faint group-hover:text-soft"
              }`}
            >
              {label}
            </span>
          </button>
        )
      })}

      <Ear />
    </nav>
  )
}
