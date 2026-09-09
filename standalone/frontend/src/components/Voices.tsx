import { useMemo, useState } from "react"
import type { Turn } from "../api"
import { clock } from "../api"

/** Nobody can hold eight rows in their head, and the tail is never the point. */
const ROWS = 6

/**
 * Who held the floor, in order — a row each.
 *
 * This was one row for everybody, a segment per turn in that person's colour.
 * On a real meeting — twenty-three minutes, six people, 281 turns — that is two
 * hundred hairlines in six colours packed into a strip eight pixels tall: a
 * barcode, and the loudest object on the screen. Merging consecutive turns by
 * one person helped and did not fix it, because the meeting genuinely was that
 * much back-and-forth. The failure was not the merging, it was asking one row
 * to carry six voices.
 *
 * A row each fixes it by construction. Every row holds one colour, so no row
 * can ever become a barcode however finely the talking is cut; the shape of the
 * conversation is in the *pattern between* rows, which is where it always was.
 * One long bar against five empty rows is somebody presenting. Five rows all
 * dashed is an argument. That is legible at a glance and the strip never was.
 *
 * Click anywhere to send the audio and the transcript there.
 */
export default function Voices({
  turns,
  colours,
  at,
  lit,
  onJump,
  onLight,
}: {
  turns: Turn[]
  colours: Map<string, string>
  /** Where the player is, in seconds. */
  at: number
  /** A speaker to pick out, or null for all of them. */
  lit: string | null
  onJump: (seconds: number) => void
  onLight: (speaker: string | null) => void
}) {
  const [over, setOver] = useState<{ who: string; start: number; end: number } | null>(null)

  const { people, first, span } = useMemo(() => {
    const held = new Map<string, { who: string; said: number; bars: [number, number][] }>()
    for (const t of turns) {
      const who = t.speaker || "—"
      const row = held.get(who) ?? { who, said: 0, bars: [] }
      const last = row.bars[row.bars.length - 1]
      // A breath is not a new turn. Merging inside a row keeps the DOM small
      // and stops a sentence read in three gulps looking like three remarks.
      if (last && t.start - last[1] < 1.5) last[1] = Math.max(last[1], t.end)
      else row.bars.push([t.start, Math.max(t.end, t.start + 0.5)])
      row.said += Math.max(t.end - t.start, 0)
      held.set(who, row)
    }
    const ranked = [...held.values()].sort((a, b) => b.said - a.said)
    const first = turns[0]?.start ?? 0
    const last = turns.reduce((m, t) => Math.max(m, t.end), first + 1)
    return { people: ranked.slice(0, ROWS), first, span: Math.max(last - first, 1) }
  }, [turns])

  if (people.length === 0) return null
  const where = Math.min(Math.max((at - first) / span, 0), 1) * 100

  return (
    <div className="mt-3" onMouseLeave={() => (onLight(null), setOver(null))}>
      <div className="relative">
        {people.map((p) => {
          const colour = colours.get(p.who) || "var(--color-line)"
          const dim = lit !== null && lit !== p.who
          return (
            <div
              key={p.who}
              onMouseEnter={() => onLight(p.who)}
              className="group flex items-center gap-2 py-[1.5px]"
            >
              <span
                className={`w-[86px] shrink-0 truncate text-right text-[9.5px] leading-none transition-colors ${
                  dim ? "text-faint/50" : "text-faint group-hover:text-soft"
                }`}
              >
                {p.who}
              </span>
              <span
                className={`relative h-[7px] flex-1 rounded-full transition-colors ${
                  dim ? "bg-raised/30" : "bg-raised/70"
                }`}
              >
                {p.bars.map(([s, e], i) => (
                  <button
                    key={i}
                    onClick={() => onJump(s)}
                    onMouseEnter={() => setOver({ who: p.who, start: s, end: e })}
                    aria-label={`${p.who}, ${clock(s)}`}
                    style={{
                      left: `${((s - first) / span) * 100}%`,
                      width: `max(2px, ${((e - s) / span) * 100}%)`,
                      background: colour,
                      opacity: dim ? 0.2 : 1,
                    }}
                    className="absolute inset-y-0 rounded-full transition-opacity duration-200 hover:!opacity-100"
                  />
                ))}
              </span>
            </div>
          )
        })}

        {/* One playhead through every row, so the rows read as one instrument. */}
        <span
          style={{ left: `calc(86px + 0.5rem + ${where}% - ${where / 100} * (86px + 0.5rem))` }}
          className="pointer-events-none absolute inset-y-0 w-px bg-text/70 transition-[left] duration-200"
        />
      </div>

      {/* One readout for the whole block, in space it already occupies. A native
          title waits half a second and arrives in a font nothing else here uses. */}
      <p className="mt-1 h-3 pl-[94px] text-[10px] leading-3 text-faint">
        {over && (
          <span className="tabular-nums">
            {clock(over.start)} — {clock(over.end)} · {Math.round(over.end - over.start)} с
          </span>
        )}
      </p>
    </div>
  )
}
