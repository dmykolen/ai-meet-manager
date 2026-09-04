import { useEffect, useState } from "react"
import { motion } from "motion/react"
import { Search as SearchIcon, X } from "lucide-react"
import { Meetings, clock, type Hit } from "../api"

/**
 * Search across every transcript.
 *
 * Words, not meaning — that is what Ask is for. This is the tool for "where did
 * somebody say Vodafone", and it answers while you are still typing.
 */
export default function Search({ onOpen }: { onOpen: (id: number) => void }) {
  const [query, setQuery] = useState("")
  const [hits, setHits] = useState<Hit[] | null>(null)

  useEffect(() => {
    const q = query.trim()
    if (q.length < 2) {
      setHits(null)
      return
    }
    // Debounced, because every keystroke would otherwise run a query across
    // every transcript there is.
    const timer = setTimeout(async () => {
      try {
        setHits((await Meetings.Search(q)) as Hit[])
      } catch {
        setHits([])
      }
    }, 180)
    return () => clearTimeout(timer)
  }, [query])

  return (
    <div className="flex h-full flex-col">
      <header className="no-drag px-6 pb-3 pt-2">
        <h1 className="text-[24px] font-semibold tracking-[-0.02em]">Search</h1>
      </header>

      <div className="no-drag px-6">
        <div className="flex items-center gap-2.5 rounded-panel border border-line/60 bg-surface/60 px-4 py-2.5 transition-colors focus-within:border-accent/50">
          <SearchIcon size={15} className="shrink-0 text-faint" />
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="A word anybody said, in any meeting…"
            className="min-w-0 flex-1 bg-transparent text-[13.5px] outline-none placeholder:text-faint"
          />
          {query && (
            <button onClick={() => setQuery("")} className="shrink-0 text-faint hover:text-text">
              <X size={14} />
            </button>
          )}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10 pt-4">
        {hits === null ? (
          <p className="text-[12.5px] text-faint">Two letters is enough to start.</p>
        ) : hits.length === 0 ? (
          <p className="text-[12.5px] text-faint">Nobody said that.</p>
        ) : (
          <>
            <p className="mb-2.5 text-[11px] text-faint">
              {hits.length} {hits.length === 1 ? "passage" : "passages"}
            </p>
            <ul className="flex flex-col gap-1.5">
              {hits.map((h, i) => (
                <motion.li
                  key={i}
                  initial={{ opacity: 0, y: 6 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ duration: 0.2, delay: Math.min(i * 0.015, 0.25) }}
                >
                  <button
                    onClick={() => onOpen(h.recording)}
                    className="w-full rounded-lg border border-line/50 bg-surface/40 px-3.5 py-2.5 text-left transition-colors hover:border-line hover:bg-raised/60"
                  >
                    <div className="flex items-baseline gap-2 text-[10.5px] text-faint">
                      <span className="min-w-0 truncate">{h.title}</span>
                      <span className="tabular-nums">{clock(h.start)}</span>
                      {h.speaker && <span className="text-accent">{h.speaker}</span>}
                    </div>
                    <p className="mt-0.5 text-[12.5px] leading-relaxed text-soft">
                      <Marked text={h.text} needle={query.trim()} />
                    </p>
                  </button>
                </motion.li>
              ))}
            </ul>
          </>
        )}
      </div>
    </div>
  )
}

/** The matched word, lit up. Case-insensitive, and safe with any input. */
function Marked({ text, needle }: { text: string; needle: string }) {
  const last = needle.split(/\s+/).filter(Boolean).pop()
  if (!last) return <>{text}</>
  const at = text.toLowerCase().indexOf(last.toLowerCase())
  if (at < 0) return <>{text}</>
  return (
    <>
      {text.slice(0, at)}
      <mark className="rounded bg-accent/25 px-0.5 text-text">{text.slice(at, at + last.length)}</mark>
      {text.slice(at + last.length)}
    </>
  )
}
