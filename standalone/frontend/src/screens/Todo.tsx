import { useCallback, useEffect, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Check, ListChecks } from "lucide-react"
import NeedsKey from "../components/NeedsKey"
import { Meetings, when, type Outstanding } from "../api"

/**
 * Everything anybody committed to, across every meeting.
 *
 * The summaries have carried task, owner and deadline all along; nothing ever
 * gathered them into one place, so the answer to "what did I promise" was to
 * open meetings one at a time and read.
 */
export default function Todo({ onOpen }: { onOpen: (id: number) => void }) {
  const [items, setItems] = useState<Outstanding[] | null>(null)
  const [showDone, setShowDone] = useState(false)

  const load = useCallback(async () => {
    try {
      setItems((await Meetings.Actions(showDone)) as Outstanding[])
    } catch {
      setItems([])
    }
  }, [showDone])

  useEffect(() => {
    load()
  }, [load])

  const owners = [...new Set((items ?? []).map((i) => i.owner).filter(Boolean))]

  return (
    <div className="flex h-full flex-col">
      <header className="no-drag flex items-baseline justify-between px-6 pb-3 pt-2">
        <h1 className="text-[24px] font-semibold tracking-[-0.02em]">To do</h1>
        <button
          onClick={() => setShowDone((v) => !v)}
          className={`rounded-lg px-2.5 py-1 text-[11.5px] transition-colors ${
            showDone ? "bg-raised text-text" : "text-faint hover:bg-raised hover:text-soft"
          }`}
        >
          {showDone ? "Hiding nothing" : "Show finished"}
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10">
        {items === null ? null : items.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center pb-16 text-center">
            <ListChecks size={26} className="text-faint" />
            <h2 className="mt-4 text-[14px] font-medium">Nothing outstanding</h2>
            <p className="mt-1 max-w-xs text-[12.5px] leading-relaxed text-soft">
              Anything anybody commits to in a meeting turns up here on its own.
            </p>
            <div className="mt-5 max-w-sm text-left">
              <NeedsKey what="Reading action items out of a meeting" />
            </div>
          </div>
        ) : (
          <>
            {owners.length > 0 && (
              <p className="mb-3 text-[11px] text-faint">
                {items.length} open · {owners.join(", ")}
              </p>
            )}
            <ul className="flex flex-col gap-1">
              <AnimatePresence initial={false}>
                {items.map((a) => (
                  <motion.li
                    key={`${a.recording}-${a.index}`}
                    layout
                    initial={{ opacity: 0, y: 6 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, height: 0 }}
                    transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
                    className="group flex items-start gap-2.5 rounded-lg px-2 py-2 transition-colors hover:bg-raised/50"
                  >
                    <button
                      onClick={async () => {
                        await Meetings.Tick(a.recording, a.index, !a.done)
                        load()
                      }}
                      className={`mt-[2px] flex size-[16px] shrink-0 items-center justify-center rounded border transition-colors ${
                        a.done ? "border-good bg-good/20 text-good" : "border-line hover:border-accent"
                      }`}
                    >
                      {a.done && <Check size={11} strokeWidth={3} />}
                    </button>

                    <div className="min-w-0 flex-1">
                      <p className={`text-[13px] leading-relaxed ${a.done ? "text-faint line-through" : ""}`}>
                        {a.task}
                      </p>
                      <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[10.5px] text-faint">
                        {a.owner && <span className="text-accent">{a.owner}</span>}
                        {a.due && <span>{a.due}</span>}
                        <button
                          onClick={() => onOpen(a.recording)}
                          className="truncate transition-colors hover:text-soft hover:underline"
                        >
                          {a.title}
                        </button>
                        <span>{when(a.started)}</span>
                      </div>
                    </div>
                  </motion.li>
                ))}
              </AnimatePresence>
            </ul>
          </>
        )}
      </div>
    </div>
  )
}
