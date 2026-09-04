import { useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  ArrowLeft, Check, ChevronRight, Copy, ListChecks, NotebookPen,
  RefreshCw,
  Rows2, Rows3, Search, Trash2, Users, X,
  Sparkles,
} from "lucide-react"
import { Meetings, clock, length, when, type Meeting } from "../api"
import Shape from "../components/Shape"
import Player, { type Controls } from "../components/Player"

/**
 * One meeting.
 *
 * The summary first and the transcript second, because that is the order people
 * want them in: what happened, and only then the words. Chapters are clickable —
 * the timestamps were always in the data and were never used.
 */
export default function Transcript({
  id,
  density,
  onDensity,
  onBack,
  onChanged,
}: {
  id: number
  density: "compact" | "comfortable"
  onDensity: (d: "compact" | "comfortable") => void
  onBack: () => void
  onChanged: () => void
}) {
  const [meeting, setMeeting] = useState<Meeting | null>(null)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [note, setNote] = useState("")
  const [panel, setPanel] = useState<"none" | "note" | "find">("none")
  const [needle, setNeedle] = useState("")
  const [copied, setCopied] = useState(false)
  const [thinking, setThinking] = useState(false)
  const [problem, setProblem] = useState("")
  const [again, setAgain] = useState(false)
  const rows = useRef<HTMLDivElement>(null)
  const player = useRef<Controls>(null)
  const [at, setAt] = useState(0)

  const load = async () => {
    const m = (await Meetings.Open(id)) as Meeting
    setMeeting(m)
    setNote(m.note ?? "")
  }

  useEffect(() => {
    load()
    const keys = (e: KeyboardEvent) => {
      if (e.key === "Escape") panel === "none" ? onBack() : setPanel("none")
      if (e.key === "f" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setPanel("find")
      }
    }
    window.addEventListener("keydown", keys)
    return () => window.removeEventListener("keydown", keys)
  }, [id, panel])

  const speakers = useMemo(() => meeting?.speakers ?? [], [meeting])
  const colours = useMemo(() => {
    const map = new Map<string, string>()
    speakers.forEach((name, i) => map.set(name, VOICES[i % VOICES.length]))
    return map
  }, [speakers])

  const shown = useMemo(() => {
    if (!meeting) return []
    const q = needle.trim().toLowerCase()
    if (!q) return meeting.transcript
    return meeting.transcript.filter((t) => t.text.toLowerCase().includes(q))
  }, [meeting, needle])

  if (!meeting) return null
  const s = meeting.summary
  const tight = density === "compact"

  const jump = (seconds: number) => {
    player.current?.play(seconds)
    setNeedle("")
    requestAnimationFrame(() => {
      const target = rows.current?.querySelector<HTMLElement>(`[data-at="${Math.floor(seconds)}"]`)
      target?.scrollIntoView({ behavior: "smooth", block: "center" })
      target?.animate(
        [{ background: "color-mix(in oklch, var(--color-accent) 20%, transparent)" }, { background: "transparent" }],
        { duration: 1400, easing: "ease-out" },
      )
    })
  }

  const copy = async () => {
    const md = (await Meetings.Markdown(id)) as string
    await navigator.clipboard.writeText(md)
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }

  return (
    <div className="flex h-full flex-col">
      <header className="no-drag flex items-center gap-1 px-6 pb-2 pt-2">
        <Tool onClick={onBack} label="Back">
          <ArrowLeft size={15} />
        </Tool>
        <div className="ml-auto flex items-center gap-0.5">
          <Tool onClick={() => setPanel(panel === "find" ? "none" : "find")} label="Find  ⌘F" on={panel === "find"}>
            <Search size={15} />
          </Tool>
          <Tool onClick={() => setPanel(panel === "note" ? "none" : "note")} label="Notes" on={panel === "note"}>
            <NotebookPen size={15} />
          </Tool>
          <Tool
            onClick={async () => {
              setThinking(true)
              setProblem("")
              try {
                await Meetings.Summarise(id)
                await load()
                onChanged()
              } catch (e) {
                setProblem(String(e).replace(/^Error:\s*/, ""))
              } finally {
                setThinking(false)
              }
            }}
            label={s ? "Summarise again" : "Summarise"}
          >
            <Sparkles size={15} className={thinking ? "animate-pulse text-accent" : ""} />
          </Tool>
          <Tool
            onClick={async () => {
              if (!again) {
                setAgain(true)
                return
              }
              setAgain(false)
              await Meetings.Again(id)
              onChanged()
              onBack()
            }}
            label={
              again
                ? "Press again to confirm — the transcript, speakers and summary are rewritten"
                : "Transcribe again from the audio"
            }
            on={again}
          >
            <RefreshCw size={15} className={again ? "text-accent" : ""} />
          </Tool>
          <Tool onClick={copy} label="Copy as Markdown">
            {copied ? <Check size={15} className="text-good" /> : <Copy size={15} />}
          </Tool>
          <Tool
            onClick={() => onDensity(tight ? "comfortable" : "compact")}
            label={tight ? "Roomier" : "Tighter"}
          >
            {tight ? <Rows3 size={15} /> : <Rows2 size={15} />}
          </Tool>
          <Tool
            onClick={async () => {
              await Meetings.Delete(id)
              onChanged()
              onBack()
            }}
            label="Delete"
            danger
          >
            <Trash2 size={15} />
          </Tool>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10">
        <motion.h1
          className="text-[24px] font-semibold leading-tight tracking-[-0.02em]"
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.3, ease: [0.22, 1, 0.36, 1] }}
        >
          {meeting.title}
        </motion.h1>
        <p className="mt-1 flex flex-wrap items-center gap-x-2 text-[11.5px] text-faint">
          <span>{when(meeting.started)}</span>
          <Dot />
          <span>{length(meeting.duration)}</span>
          <Dot />
          <span>{meeting.turns} rows</span>
          {speakers.length > 0 && (
            <>
              <Dot />
              <span className="inline-flex items-center gap-1">
                <Users size={11} /> {speakers.length}
              </span>
            </>
          )}
        </p>

        <AnimatePresence>
          {panel !== "none" && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
              className="overflow-hidden"
            >
              {panel === "note" ? (
                <textarea
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  onBlur={() => Meetings.SaveNote(id, note)}
                  placeholder="Your own notes on this meeting…"
                  className="mt-3 h-24 w-full resize-none rounded-panel border border-line/60 bg-surface/60 px-3.5 py-2.5 text-[13px] leading-relaxed outline-none transition-colors placeholder:text-faint focus:border-accent/50"
                />
              ) : (
                <div className="mt-3 flex items-center gap-2 rounded-panel border border-line/60 bg-surface/60 px-3.5 py-2 focus-within:border-accent/50">
                  <Search size={14} className="shrink-0 text-faint" />
                  <input
                    autoFocus
                    value={needle}
                    onChange={(e) => setNeedle(e.target.value)}
                    placeholder="Find in this transcript…"
                    className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-faint"
                  />
                  {needle && (
                    <>
                      <span className="shrink-0 text-[11px] tabular-nums text-faint">{shown.length}</span>
                      <button onClick={() => setNeedle("")} className="shrink-0 text-faint hover:text-text">
                        <X size={13} />
                      </button>
                    </>
                  )}
                </div>
              )}
            </motion.div>
          )}
        </AnimatePresence>

        {s && (
          <section className="mt-4 rounded-panel border border-line/60 bg-surface/50 p-4">
            <p className="text-[13.5px] leading-relaxed text-text/90">{s.overview}</p>

            {s.decisions && s.decisions.length > 0 && (
              <Block title="Decided" Icon={Check}>
                {s.decisions.map((d, i) => (
                  <Line key={i}>{d}</Line>
                ))}
              </Block>
            )}

            {s.action_items && s.action_items.length > 0 && (
              <Block title="To do" Icon={ListChecks}>
                {s.action_items.map((a, i) => (
                  <li key={i} className="flex items-start gap-2 text-[12.5px] leading-relaxed">
                    <button
                      onClick={async () => {
                        await Meetings.Tick(id, i, !a.done)
                        await load()
                        onChanged()
                      }}
                      className={`mt-[2px] flex size-[15px] shrink-0 items-center justify-center rounded border transition-colors ${
                        a.done ? "border-good bg-good/20 text-good" : "border-line hover:border-accent"
                      }`}
                    >
                      {a.done && <Check size={10} strokeWidth={3} />}
                    </button>
                    <span className={a.done ? "text-faint line-through" : ""}>
                      {a.task}
                      {a.owner && <span className="ml-1.5 text-accent">{a.owner}</span>}
                      {a.due && <span className="ml-1.5 text-faint">{a.due}</span>}
                    </span>
                  </li>
                ))}
              </Block>
            )}

            {s.chapters && s.chapters.length > 0 && (
              <Block title="Sections" Icon={ChevronRight}>
                {s.chapters.map((c, i) => (
                  <li key={i}>
                    <button
                      onClick={() => jump(c.start)}
                      className="group flex w-full items-baseline gap-2.5 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-raised/70"
                    >
                      <span className="shrink-0 text-[11px] tabular-nums text-faint group-hover:text-accent">
                        {clock(c.start)}
                      </span>
                      <span className="text-[12.5px] leading-relaxed">{c.title}</span>
                    </button>
                  </li>
                ))}
              </Block>
            )}

            {s.open_questions && s.open_questions.length > 0 && (
              <Block title="Left open" Icon={Search}>
                {s.open_questions.map((q, i) => (
                  <Line key={i}>{q}</Line>
                ))}
              </Block>
            )}
          </section>
        )}

        {meeting.audio ? (
          <Player file={meeting.audio} onTime={setAt} ref={player} />
        ) : (
          <p className="mt-4 text-[11.5px] text-faint">
            The audio has been deleted to save space. The transcript is complete.
          </p>
        )}

        {!s && (
          <div className="mt-5 flex items-center gap-3 rounded-panel border border-line/60 bg-surface/40 px-4 py-3">
            <Sparkles size={15} className={`shrink-0 ${thinking ? "animate-pulse text-accent" : "text-faint"}`} />
            <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-soft">
              {problem ? (
                <span className="text-warn">{problem}</span>
              ) : thinking ? (
                "Reading it back…"
              ) : (
                "No summary yet. Read it back for a title, the decisions, who owes what, and chapters you can jump to."
              )}
            </p>
            <button
              onClick={async () => {
                setThinking(true)
                setProblem("")
                try {
                  await Meetings.Summarise(id)
                  await load()
                  onChanged()
                } catch (e) {
                  setProblem(String(e).replace(/^Error:\s*/, ""))
                } finally {
                  setThinking(false)
                }
              }}
              disabled={thinking}
              className="shrink-0 rounded-lg bg-accent px-3 py-1.5 text-[12px] font-medium text-ink transition-opacity disabled:opacity-30"
            >
              {thinking ? "Reading…" : "Summarise"}
            </button>
          </div>
        )}

        <Shape id={id} colours={colours} onJump={jump} />

        {speakers.length > 0 && (
          <div className="mt-5 flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-[10.5px] uppercase tracking-wider text-faint">Voices</span>
            {speakers.map((name) => (
              <SpeakerChip
                key={name}
                name={name}
                colour={colours.get(name)!}
                editing={renaming === name}
                onEdit={() => setRenaming(name)}
                onDone={async (to) => {
                  setRenaming(null)
                  if (to && to !== name) {
                    await Meetings.Rename(id, name, to)
                    await load()
                    onChanged()
                  }
                }}
              />
            ))}
          </div>
        )}

        <div ref={rows} className={`mt-4 flex flex-col ${tight ? "gap-0" : "gap-1.5"}`}>
          {shown.map((t, i) => (
            <div
              key={i}
              data-at={Math.floor(t.start)}
              onClick={() => player.current?.play(t.start)}
              className={`group flex cursor-pointer gap-3 rounded-md px-1.5 transition-colors hover:bg-raised/40 ${
                tight ? "py-[3px]" : "py-1"
              } ${at >= t.start && at < t.end ? "bg-accent-soft/25" : ""}`}
            >
              <span
                className={`w-9 shrink-0 pt-[2px] text-right text-[10.5px] tabular-nums transition-colors ${
                  at >= t.start && at < t.end ? "text-accent" : "text-faint/70 group-hover:text-accent"
                }`}
              >
                {clock(t.start)}
              </span>
              <p className={`min-w-0 ${tight ? "text-[12.5px] leading-[1.45]" : "text-[13.5px] leading-[1.6]"}`}>
                {t.speaker && (
                  <span className="mr-1.5 font-medium" style={{ color: colours.get(t.speaker) }}>
                    {t.speaker}
                  </span>
                )}
                <span className="text-text/85">{t.text}</span>
              </p>
            </div>
          ))}
          {needle && shown.length === 0 && (
            <p className="px-1.5 py-4 text-[12.5px] text-faint">Nothing in this transcript says that.</p>
          )}
        </div>
      </div>
    </div>
  )
}

/** A voice gets a colour so the eye can follow one person down the page. */
const VOICES = [
  "oklch(72% 0.225 295)", // violet — the app's own colour goes to the first voice
  "oklch(80% 0.185 200)", // cyan
  "oklch(83% 0.205 148)", // spring green
  "oklch(85% 0.185 92)",  // acid yellow
  "oklch(72% 0.215 22)",  // coral
  "oklch(74% 0.215 340)", // magenta
  "oklch(72% 0.185 262)", // indigo
  "oklch(80% 0.175 122)", // lime
]

function Dot() {
  return <span className="text-faint/50">·</span>
}

function Block({
  title,
  Icon,
  children,
}: {
  title: string
  Icon: typeof Check
  children: React.ReactNode
}) {
  return (
    <div className="mt-4 border-t border-line/50 pt-3">
      <h3 className="mb-1.5 flex items-center gap-1.5 text-[10.5px] font-semibold uppercase tracking-wider text-faint">
        <Icon size={11} /> {title}
      </h3>
      <ul className="flex flex-col gap-1">{children}</ul>
    </div>
  )
}

function Line({ children }: { children: React.ReactNode }) {
  return (
    <li className="flex gap-2 text-[12.5px] leading-relaxed">
      <span className="mt-[7px] size-1 shrink-0 rounded-full bg-faint" />
      <span>{children}</span>
    </li>
  )
}

function Tool({
  children,
  onClick,
  label,
  on,
  danger,
}: {
  children: React.ReactNode
  onClick: () => void
  label: string
  on?: boolean
  danger?: boolean
}) {
  return (
    <button
      onClick={onClick}
      title={label}
      className={`flex size-8 items-center justify-center rounded-lg transition-colors ${
        on ? "bg-raised text-text" : danger ? "text-faint hover:bg-warn/15 hover:text-warn" : "text-faint hover:bg-raised hover:text-text"
      }`}
    >
      {children}
    </button>
  )
}

/** Renaming a speaker is the commonest edit there is, so it takes one click. */
function SpeakerChip({
  name,
  colour,
  editing,
  onEdit,
  onDone,
}: {
  name: string
  colour: string
  editing: boolean
  onEdit: () => void
  onDone: (to: string) => void
}) {
  const [value, setValue] = useState(name)
  useEffect(() => setValue(name), [name, editing])

  if (editing) {
    return (
      <input
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onBlur={() => onDone(value.trim())}
        onKeyDown={(e) => {
          if (e.key === "Enter") onDone(value.trim())
          if (e.key === "Escape") onDone("")
        }}
        className="w-32 rounded-full border border-accent/50 bg-surface px-2.5 py-0.5 text-[11.5px] outline-none"
      />
    )
  }
  return (
    <button
      onClick={onEdit}
      title="Click to name this voice"
      className="flex items-center gap-1.5 rounded-full border border-line/60 bg-surface/60 px-2.5 py-0.5 text-[11.5px] text-soft transition-colors hover:border-accent/40 hover:text-text"
    >
      <span className="size-1.5 rounded-full" style={{ background: colour }} />
      {name}
    </button>
  )
}
