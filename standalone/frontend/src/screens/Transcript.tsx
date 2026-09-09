import { useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  Check, ChevronLeft, ChevronRight, Copy, ListChecks, MoreHorizontal, NotebookPen,
  RefreshCw, Rows2, Rows3, Search, Sparkles, Trash2, Users, X,
} from "lucide-react"
import { Meetings, clock, length, when, type Group, type Meeting, type Person } from "../api"
import { colourOf, palette } from "../colours"
import Shape from "../components/Shape"
import Player, { type Controls } from "../components/Player"
import Voices from "../components/Voices"

/**
 * One meeting.
 *
 * The summary first and the transcript second, because that is the order people
 * want them in: what happened, and only then the words. Chapters are clickable —
 * the timestamps were always in the data and were never used.
 */
export default function Transcript({
  id,
  groups,
  density,
  onDensity,
  onBack,
  onChanged,
  at: openAt,
}: {
  id: number
  /** So the reader can say which project this belongs to. */
  groups: Group[]
  density: "compact" | "comfortable"
  onDensity: (d: "compact" | "comfortable") => void
  onBack: () => void
  onChanged: () => void
  /** Open at this second, and show the transcript rather than the summary. */
  at?: number
}) {
  const [meeting, setMeeting] = useState<Meeting | null>(null)
  const [title, setTitle] = useState("")
  const [people, setPeople] = useState<Person[]>([])
  const [tab, setTab] = useState<"summary" | "turns">(openAt ? "turns" : "summary")
  /** A voice picked out on the track, so the transcript can follow it. */
  const [lit, setLit] = useState<string | null>(null)
  const [copiedTurn, setCopiedTurn] = useState<number | null>(null)
  const heading = useRef<HTMLTextAreaElement>(null)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [note, setNote] = useState("")
  const [panel, setPanel] = useState<"none" | "note" | "find">("none")
  const [needle, setNeedle] = useState("")
  const [copied, setCopied] = useState(false)
  const [thinking, setThinking] = useState(false)
  const [problem, setProblem] = useState("")
  const rows = useRef<HTMLDivElement>(null)
  const player = useRef<Controls>(null)
  const [at, setAt] = useState(0)

  const load = async () => {
    const m = (await Meetings.Open(id)) as Meeting
    setMeeting(m)
    setTitle(m.title)
    setNote(m.note ?? "")
    setPeople(((await Meetings.People()) as Person[]) ?? [])
    // Asked for a moment: show the words, at that moment, and play from there.
    if (openAt) {
      setTab("turns")
      requestAnimationFrame(() => jump(openAt))
    }
  }

  // A textarea does not grow on its own, so the box is set from the text it
  // holds — on every change and on load, since the title arrives after mount.
  useEffect(() => {
    const el = heading.current
    if (!el) return
    el.style.height = "auto"
    el.style.height = `${el.scrollHeight}px`
  }, [title])

  useEffect(() => {
    load()
    const keys = (e: KeyboardEvent) => {
      // Escape belongs to whatever is being typed in — cancelling a rename
      // must not also navigate away. ⌘F still works from anywhere.
      const typing = document.activeElement?.tagName
      const inField = typing === "INPUT" || typing === "TEXTAREA"
      // A menu is open: Escape belongs to it. The browser dismisses a popover
      // by itself, and closing the whole meeting underneath it as well would
      // be two things happening for one key.
      const menu = document.querySelector(":popover-open")
      if (e.key === "Escape" && !inField && !menu) panel === "none" ? onBack() : setPanel("none")
      if (e.key === "f" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setPanel("find")
      }
    }
    window.addEventListener("keydown", keys)
    return () => window.removeEventListener("keydown", keys)
  }, [id, panel])

  const speakers = useMemo(() => meeting?.speakers ?? [], [meeting])
  // Derived from the name, not from who spoke first, so one person is one
  // colour in every meeting — and overridden by whatever they were painted.
  const filed = groups.find((g) => g.id === meeting?.group)

  const colours = useMemo(() => {
    const chosen = new Map(people.map((p) => [p.name, p.colour]))
    // Loudest first. There are five hues and a meeting can hold more people
    // than that, so somebody eventually repeats a hue at the other weight —
    // and it should be whoever said least, not whoever the array happened to
    // list fifth. Every view that draws speakers ranks them this way.
    const said = new Map<string, number>()
    for (const t of meeting?.transcript ?? []) {
      if (t.speaker) said.set(t.speaker, (said.get(t.speaker) ?? 0) + Math.max(t.end - t.start, 0))
    }
    const ranked = [...speakers].sort((a, b) => (said.get(b) ?? 0) - (said.get(a) ?? 0))
    return palette(ranked, chosen)
  }, [speakers, people, meeting])

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
    <div className="@container/read flex h-full flex-col">
      {/* Left: the way out, named. A bare ✕ alone at one end of a bar whose
          other end holds seven icons is two controls that never look related.
          Right: what you reach for while reading. Everything that rewrites or
          destroys is one press further away, in a menu — which is a native
          popover, so it lives in the top layer, closes on Escape or a click
          outside, and is positioned by CSS rather than by measuring anything. */}
      <header className="no-drag flex h-bar flex-none items-center gap-1 border-b border-line/70 px-2.5">
        <button
          onClick={onBack}
          className="group flex items-center gap-1.5 rounded-lg py-1 pl-1.5 pr-2.5 text-[11.5px] text-faint transition-colors hover:bg-raised hover:text-text"
        >
          <ChevronLeft size={15} className="transition-transform group-hover:-translate-x-0.5" />
          Записи
        </button>

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

          <span className="mx-1 h-4 w-px bg-line" />

          <button
            popoverTarget="reader-more"
            style={{ anchorName: "--reader-more" } as React.CSSProperties}
            aria-label="Більше дій"
            className="flex size-8 items-center justify-center rounded-lg text-faint transition-colors hover:bg-raised hover:text-text"
          >
            <MoreHorizontal size={15} />
          </button>
          <div
            popover="auto"
            id="reader-more"
            style={{
              positionAnchor: "--reader-more",
              positionArea: "bottom span-left",
              positionTryFallbacks: "flip-block, flip-inline",
              marginTop: "6px",
            } as React.CSSProperties}
            className="w-[236px] rounded-xl border border-line bg-raised/95 p-1 text-text shadow-[0_20px_50px_-16px_rgba(0,0,0,0.8)] backdrop-blur-xl [&:popover-open]:animate-[rise_.16s_cubic-bezier(.22,1,.36,1)]"
          >
            <Item onClick={copy} Icon={copied ? Check : Copy} tint={copied ? "text-good" : ""}>
              {copied ? "Скопійовано" : "Копіювати як Markdown"}
            </Item>
            <Item
              onClick={() => onDensity(tight ? "comfortable" : "compact")}
              Icon={tight ? Rows3 : Rows2}
            >
              {tight ? "Просторіше" : "Щільніше"}
            </Item>
            <Item
              onClick={async () => {
                await Meetings.Again(id)
                onChanged()
                onBack()
              }}
              Icon={RefreshCw}
              note="Розшифровку, мовців і підсумок буде переписано"
            >
              Розшифрувати заново
            </Item>

            <span className="my-1 block h-px bg-line" />

            <Item
              onClick={async () => {
                await Meetings.Delete(id)
                onChanged()
                onBack()
              }}
              Icon={Trash2}
              danger
              note="У кошик — можна повернути"
            >
              Видалити
            </Item>
          </div>
        </div>
      </header>

      <div className="flex-none px-5 pt-3">
        {/* The title is the field. A model's guess at what the hour was about
            is often wrong and often just not what you would call it, so this
            takes a cursor like any other text — no pencil to find, nothing that
            only appears on hover, and no dialog.

            A textarea rather than an input because titles wrap: an input would
            silently turn a two-line heading into one line that scrolls. Form
            controls also do not inherit type or colour, hence font-[inherit]
            and text-text. */}
        <motion.textarea
          ref={heading}
          rows={1}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            e.stopPropagation()
            if (e.key === "Enter") {
              e.preventDefault()
              e.currentTarget.blur()
            }
            if (e.key === "Escape") {
              setTitle(meeting.title)
              e.currentTarget.blur()
            }
          }}
          onBlur={async () => {
            const to = title.trim()
            if (!to || to === meeting.title) return setTitle(meeting.title)
            await Meetings.Retitle(id, to)
            await load()
            onChanged()
          }}
          spellCheck={false}
          title="Rename this meeting"
          className="-mx-1.5 block w-[calc(100%+0.75rem)] resize-none overflow-hidden rounded-lg bg-transparent px-1.5 font-[inherit] text-[24px] font-semibold leading-tight tracking-[-0.02em] text-text outline-none transition-colors hover:bg-raised/40 focus:bg-raised/60"
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.3, ease: [0.22, 1, 0.36, 1] }}
        />
        <p className="mt-1 flex flex-wrap items-center gap-x-2 text-[11.5px] text-faint">
          {/* The date is editable, because it is not a label — the project
              document is built by replaying its meetings oldest first, so a
              recording carrying the wrong day is read into the project's
              history at the wrong point. Imported files guess from the file's
              own timestamp, and the guess has to be correctable. */}
          <label className="group/date relative -mx-1 cursor-text rounded px-1 transition-colors hover:bg-raised/60 hover:text-soft">
            {when(meeting.started)}
            <input
              type="datetime-local"
              value={local(meeting.started)}
              onChange={async (e) => {
                if (!e.target.value) return
                await Meetings.Redate(id, new Date(e.target.value).toISOString())
                await load()
                onChanged()
              }}
              className="absolute inset-0 cursor-text opacity-0"
            />
          </label>
          <Dot />
          <span>{length(meeting.duration)}</span>
          {speakers.length > 0 && (
            <>
              <Dot />
              <span className="inline-flex items-center gap-1" title={speakers.join(", ")}>
                <Users size={11} /> {speakers.length}
              </span>
            </>
          )}
          {meeting.group > 0 && filed && (
            <>
              <Dot />
              <span className="inline-flex items-center gap-1.5">
                <span
                  className="size-[7px] rounded-[2px]"
                  style={{ background: colourOf(filed.name, filed.colour) }}
                />
                {filed.name}
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
                  placeholder="Ваші нотатки до цієї наради…"
                  className="mt-3 h-24 w-full resize-none rounded-panel border border-line/60 bg-surface/60 px-3.5 py-2.5 text-[13px] leading-relaxed outline-none transition-colors placeholder:text-faint focus:border-accent/50"
                />
              ) : (
                <div className="mt-3 flex items-center gap-2 rounded-panel border border-line/60 bg-surface/60 px-3.5 py-2 focus-within:border-accent/50">
                  <Search size={14} className="shrink-0 text-faint" />
                  <input
                    autoFocus
                    value={needle}
                    onChange={(e) => setNeedle(e.target.value)}
                    placeholder="Знайти в цій розшифровці…"
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

        <Voices
          turns={meeting.transcript}
          colours={colours}
          at={at}
          lit={lit}
          onJump={jump}
          onLight={setLit}
        />
        {meeting.audio ? (
          <Player id={id} file={meeting.audio} onTime={setAt} ref={player} />
        ) : (
          <p className="mt-4 text-[11.5px] text-faint">
            Аудіо видалено, щоб не займало місце. Розшифровка ціла.
          </p>
        )}

      </div>

      {/* The document and the people who made it, side by side. Reading a
          meeting and following one voice through it are the same act. */}
      {/* Both columns breathe: the document takes what is left, the people
          take a quarter of the pane between 180 and 330. The sidebar only goes
          under the document when there is genuinely no room for it beside. */}
      <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)] @min-[560px]/read:grid-cols-[minmax(0,1fr)_clamp(180px,24cqw,330px)]">
        <div className="flex min-h-0 flex-col">
          <div className="flex h-bar flex-none items-center gap-1 border-b border-line/50 px-4">
            {(["summary", "turns"] as const).map((t) => (
              <button
                key={t}
                onClick={() => setTab(t)}
                className={`rounded-md px-2.5 py-1 text-[11.5px] transition-colors ${
                  tab === t ? "bg-raised text-text" : "text-faint hover:text-soft"
                }`}
              >
                {t === "summary" ? "Підсумок" : "Розшифровка"}
              </button>
            ))}
            {tab === "turns" && (
              <span className="ml-auto text-[10px] tabular-nums text-faint">
                {shown.length} рядків
              </span>
            )}
          </div>

          {/* Chapters as a strip rather than a list inside the summary: they
              are the fastest way into an hour, and a list buried three blocks
              down is not where somebody looks for a way in. */}
          {s?.chapters && s.chapters.length > 0 && (
            <div className="flex flex-none gap-1.5 overflow-x-auto border-b border-line/40 px-4 py-2">
              {s.chapters.map((c, i) => (
                <button
                  key={i}
                  onClick={() => jump(c.start)}
                  className="flex shrink-0 items-center gap-2 rounded-md border border-line/70 bg-surface/60 px-2.5 py-1.5 text-[10.5px] text-soft transition-[transform,border-color,color] duration-150 hover:-translate-y-0.5 hover:border-soft hover:text-text active:translate-y-0"
                >
                  <span className="tabular-nums text-faint">{clock(c.start)}</span>
                  {c.title}
                </button>
              ))}
            </div>
          )}

          <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-10 pt-3">
            {tab === "summary" ? (
              <>
                {s && (
                  <section className="mt-1">
                    {/* The overview is the largest thing here on purpose:
                        somebody opening a meeting wants a sentence they can
                        repeat, not a card with a border round it. */}
                    <p className="max-w-[68ch] text-[14.5px] font-light leading-[1.75] text-text/90">
                      {s.overview}
                    </p>

                    {s.decisions && s.decisions.length > 0 && (
                      <Block title="Вирішено" Icon={Check}>
                        {s.decisions.map((d, i) => (
                          <Line key={i}>{d}</Line>
                        ))}
                      </Block>
                    )}

                    {s.action_items && s.action_items.length > 0 && (
                      <Block title="Зобовʼязання" Icon={ListChecks}>
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


                    {s.open_questions && s.open_questions.length > 0 && (
                      <Block title="Лишилось відкритим" Icon={Search}>
                        {s.open_questions.map((q, i) => (
                          <Line key={i}>{q}</Line>
                        ))}
                      </Block>
                    )}
                  </section>
                )}

                {!s && (
                  <div className="mt-6 flex items-center gap-3 rounded-lg border border-line/50 bg-surface/40 px-3.5 py-2.5">
                    <Sparkles size={15} className={`shrink-0 ${thinking ? "animate-pulse text-accent" : "text-faint"}`} />
                    <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-soft">
                      {problem ? (
                        <span className="text-warn">{problem}</span>
                      ) : thinking ? (
                        "Читаю назад…"
                      ) : (
                        "Саммарі ще немає. Прочитайте назад — буде назва, рішення, хто що винен, і розділи, якими можна стрибати."
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
                      {thinking ? "Читаю…" : "Прочитати"}
                    </button>
                  </div>
                )}

              </>
            ) : (
              <div ref={rows} className={`mt-4 flex flex-col ${tight ? "gap-0" : "gap-1.5"}`}>
                {shown.map((t, i) => {
                  const voice = (t.speaker && colours.get(t.speaker)) || undefined
                  const now = at >= t.start && at < t.end
                  const fresh = t.speaker !== shown[i - 1]?.speaker
                  return (
                    <div
                      key={i}
                      data-at={Math.floor(t.start)}
                      onClick={() => player.current?.play(t.start)}
                      style={{
                        // The turn being played is tinted by whoever is
                        // speaking, not by the app's own colour. One accent for
                        // six voices told nobody which of them it was.
                        background: now && voice ? `color-mix(in oklch, ${voice} 9%, transparent)` : undefined,
                        boxShadow: now && voice ? `inset 2px 0 0 ${voice}` : undefined,
                      }}
                      className={`group flex cursor-pointer gap-3 rounded-md px-1.5 transition-colors hover:bg-raised/40 ${
                        tight ? "py-[3px]" : "py-1"
                      } ${now && !voice ? "bg-accent-soft/25" : ""} ${
                        // Picking a voice out on the track above dims everything
                        // that is not them, so one person can be followed down
                        // the page without reading a word.
                        lit && t.speaker !== lit ? "opacity-25" : ""
                      }`}
                    >
                      <span
                        style={{ color: now ? voice : undefined }}
                        className={`w-9 shrink-0 pt-[2px] text-right text-[10.5px] tabular-nums transition-colors ${
                          now ? "" : "text-faint/70 group-hover:text-soft"
                        }`}
                      >
                        {clock(t.start)}
                      </span>
                      <p className={`min-w-0 flex-1 ${tight ? "text-[12.5px] leading-[1.45]" : "text-[13.5px] leading-[1.6]"}`}>
                        {/* The name only when it changes hands. Repeating it on
                            every row of a four-minute answer is noise, and the
                            dot keeps the voice identifiable without it. */}
                        {t.speaker && fresh && (
                          <span className="mr-1.5 inline-flex items-baseline gap-1.5 font-medium" style={{ color: voice }}>
                            <span className="size-[5px] shrink-0 translate-y-[-1px] rounded-full" style={{ background: voice }} />
                            {t.speaker}
                          </span>
                        )}
                        <span className="text-text/85">{t.text}</span>
                      </p>
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          navigator.clipboard.writeText(`${t.speaker ? t.speaker + ": " : ""}${t.text}`)
                          setCopiedTurn(i)
                          setTimeout(() => setCopiedTurn(null), 1200)
                        }}
                        title="Скопіювати репліку"
                        className="mt-[2px] shrink-0 self-start rounded p-0.5 text-faint opacity-0 transition-opacity hover:text-text group-hover:opacity-100 focus-visible:opacity-100"
                      >
                        {copiedTurn === i ? <Check size={11} className="text-good" /> : <Copy size={11} />}
                      </button>
                    </div>
                  )
                })}
                {needle && shown.length === 0 && (
                  <p className="px-1.5 py-4 text-[12.5px] text-faint">У цій розшифровці такого немає.</p>
                )}
              </div>
            )}
          </div>
        </div>

        <aside className="min-h-0 overflow-y-auto border-t border-line/70 px-3 pb-10 @min-[560px]/read:border-l @min-[560px]/read:border-t-0">
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

        </aside>
      </div>
    </div>
  )
}

/** What an <input type="datetime-local"> wants: local time, no zone, no seconds. */
function local(iso: string) {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

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
    <div className="mt-6 border-t border-line/50 pt-3.5">
      <h3 className="mb-2 flex items-center gap-2 text-[10px] uppercase tracking-[0.14em] text-faint">
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

/**
 * One line of the reader's menu.
 *
 * A name and a sentence saying what will happen, because "Розшифрувати заново"
 * and "Видалити" are both irreversible-looking and neither of them said so when
 * they were unlabelled icons in a row of seven.
 */
function Item({
  children,
  note,
  Icon,
  tint,
  danger,
  onClick,
}: {
  children: React.ReactNode
  note?: string
  Icon: React.ComponentType<{ size?: number; className?: string }>
  tint?: string
  danger?: boolean
  onClick: () => void
}) {
  return (
    <button
      // The popover closes itself; nothing here has to track a click outside.
      onClick={(e) => {
        e.currentTarget.closest("[popover]") instanceof HTMLElement &&
          (e.currentTarget.closest("[popover]") as HTMLElement).hidePopover()
        onClick()
      }}
      className={`flex w-full items-start gap-2.5 rounded-lg px-2.5 py-1.5 text-left transition-colors ${
        danger ? "text-soft hover:bg-warn/15 hover:text-warn" : "text-soft hover:bg-surface hover:text-text"
      }`}
    >
      <Icon size={14} className={`mt-[3px] shrink-0 ${tint ?? ""}`} />
      <span className="min-w-0">
        <span className="block text-[12px] leading-tight">{children}</span>
        {note && <span className="mt-0.5 block text-[10.5px] leading-snug text-faint">{note}</span>}
      </span>
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
