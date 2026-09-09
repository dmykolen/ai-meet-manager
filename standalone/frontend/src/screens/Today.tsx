import { useEffect, useState } from "react"
import { motion } from "motion/react"
import {
  AlarmClock,
  CalendarRange,
  CircleCheck,
  Gavel,
  RefreshCw,
  Users,
} from "lucide-react"
import Head from "../components/Head"
import { Meetings, length, many, when, type Briefing, type Outstanding } from "../api"
import NeedsKey from "../components/NeedsKey"

const windows = [
  { days: 1, label: "Сьогодні" },
  { days: 7, label: "Цей тиждень" },
  { days: 30, label: "Цей місяць" },
] as const

/**
 * The first screen, and the reason the rest exists.
 *
 * A pile of transcripts is an archive. The two questions somebody actually
 * opens this with are "what did I miss" and "what did I say I would do", and
 * nothing else here answers either — the Library answers "what do I have".
 *
 * Every number on this screen is read out of summaries already written. No
 * model is called and nothing leaves the machine, so it is instant and it is
 * the same whether there is a key or not.
 */
export default function Today({ onOpen }: { onOpen: (id: number) => void }) {
  const [days, setDays] = useState<number>(1)
  const [brief, setBrief] = useState<Briefing | null>(null)

  useEffect(() => {
    let alive = true
    Meetings.Brief(days)
      .then((b) => alive && setBrief(b as Briefing))
      .catch(() => alive && setBrief(null))
    return () => {
      alive = false
    }
  }, [days])

  const empty =
    brief &&
    !brief.meetings.length &&
    !brief.mine.length &&
    !brief.overdue.length &&
    !brief.nagging.length

  return (
    <div className="flex h-full flex-col">
      <Head title={windows.find((w) => w.days === days)?.label ?? "Сьогодні"}>
        <div className="flex rounded-md bg-raised p-0.5">
          {windows.map((w) => (
            <button
              key={w.days}
              onClick={() => setDays(w.days)}
              className="relative rounded-[6px] px-2.5 py-1 text-[11.5px] transition-colors"
            >
              {days === w.days && (
                <motion.span
                  layoutId="window"
                  className="absolute inset-0 rounded-[6px] bg-surface"
                  transition={{ type: "spring", stiffness: 500, damping: 38 }}
                />
              )}
              <span className={`relative z-10 ${days === w.days ? "text-text" : "text-faint"}`}>
                {w.label}
              </span>
            </button>
          ))}
        </div>
      </Head>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-10">
        {!brief ? null : empty ? (
          <Quiet />
        ) : (
          <div className="flex max-w-3xl flex-col gap-6">
            <Numbers brief={brief} />

            {brief.overdue.length > 0 && (
              <Block
                title="Строк минув"
                Icon={AlarmClock}
                tone="text-warn"
                hint="Обіцяне, чий строк уже минув."
              >
                {brief.overdue.map((a, i) => (
                  <Item key={i} action={a} overdue onOpen={onOpen} />
                ))}
              </Block>
            )}

            {brief.nagging.length > 0 && (
              <Block
                title="Повертається знову"
                Icon={RefreshCw}
                hint="Питали не на одній нараді, і відповіді досі немає. Цього ніхто не помічає, бо кожна нарада памʼятає лише себе."
              >
                {brief.nagging.map((n, i) => (
                  <button
                    key={i}
                    onClick={() => onOpen(n.said[0].recording)}
                    className="flex w-full items-start gap-3 border-b border-line/40 px-4 py-2.5 text-left last:border-b-0 hover:bg-raised/50"
                  >
                    <span className="mt-px shrink-0 rounded-full bg-accent-soft/60 px-1.5 py-0.5 text-[10px] font-semibold tabular-nums text-accent">
                      ×{n.times}
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-[13px] leading-snug">{n.text}</span>
                      <span className="mt-0.5 block truncate text-[11px] text-faint">
                        {n.said.map((s) => s.title).join(" · ")}
                      </span>
                    </span>
                  </button>
                ))}
              </Block>
            )}

            {brief.mine.length > 0 && (
              <Block title="Досі відкрите" Icon={CircleCheck} hint="Усе, що хтось пообіцяв і не закрив.">
                {brief.mine.slice(0, 12).map((a, i) => (
                  <Item key={i} action={a} onOpen={onOpen} />
                ))}
              </Block>
            )}

            {brief.decided.length > 0 && (
              <Block title="Вирішено" Icon={Gavel} hint="Вирішене за цей проміжок.">
                {brief.decided.map((d, i) => (
                  <button
                    key={i}
                    onClick={() => onOpen(d.recording)}
                    className="flex w-full items-start gap-3 border-b border-line/40 px-4 py-2.5 text-left last:border-b-0 hover:bg-raised/50"
                  >
                    <span className="min-w-0 flex-1 text-[13px] leading-snug">{d.text}</span>
                    <span className="shrink-0 text-[11px] text-faint">{d.title}</span>
                  </button>
                ))}
              </Block>
            )}

            {brief.meetings.length > 0 && (
              <Block title="Записано" Icon={CalendarRange} hint="">
                {brief.meetings.map((m) => (
                  <button
                    key={m.id}
                    onClick={() => onOpen(m.id)}
                    className="flex w-full items-baseline gap-3 border-b border-line/40 px-4 py-2.5 text-left last:border-b-0 hover:bg-raised/50"
                  >
                    <span className="min-w-0 flex-1 truncate text-[13px]">{m.title}</span>
                    <span className="shrink-0 text-[11px] tabular-nums text-faint">
                      {length(m.duration)} · {when(m.started)}
                    </span>
                  </button>
                ))}
              </Block>
            )}

            <NeedsKey what="Усе на цьому екрані" />
          </div>
        )}
      </div>
    </div>
  )
}

/** The four numbers worth a glance before any list. */
function Numbers({ brief }: { brief: Briefing }) {
  const cells: { n: number; of: string; tone?: string }[] = [
    { n: brief.meetings.length, of: many(brief.meetings.length, "нарада", "наради", "нарад") },
    { n: brief.minutes, of: "хвилин у них" },
    { n: brief.mine.length, of: "не закрито", tone: brief.mine.length ? "text-text" : "" },
    { n: brief.overdue.length, of: "прострочено", tone: brief.overdue.length ? "text-warn" : "" },
  ]
  // Only when it has actually done something. A row saying "0 discarded" is a
  // setting asking for credit it has not earned.
  if (brief.skipped > 0) {
    cells.push({
      n: brief.skipped,
      of: `відкинуто, ${brief.spared} хв заощаджено`,
      tone: "text-good",
    })
  }
  // auto-fit rather than a fixed count: there are four of these most days and
  // five once the listener has thrown something away, and a lone cell on a
  // second row looks like a mistake.
  return (
    <div
      className="grid gap-2"
      style={{ gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))" }}
    >
      {cells.map((c, i) => (
        <motion.div
          key={c.of}
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ delay: i * 0.04, duration: 0.3 }}
          className="rounded-panel border border-line/60 bg-surface/50 px-4 py-3"
        >
          <div className={`text-[22px] font-semibold tabular-nums tracking-tight ${c.tone || ""}`}>
            {c.n}
          </div>
          <div className="mt-0.5 text-[11px] text-faint">{c.of}</div>
        </motion.div>
      ))}
    </div>
  )
}

function Block({
  title,
  Icon,
  hint,
  tone,
  children,
}: {
  title: string
  Icon: typeof Users
  hint: string
  tone?: string
  children: React.ReactNode
}) {
  return (
    <section>
      <h2 className={`mb-1.5 flex items-center gap-1.5 text-[10.5px] font-semibold uppercase tracking-wider ${tone || "text-faint"}`}>
        <Icon size={12} /> {title}
      </h2>
      {hint && <p className="mb-2 text-[11px] leading-relaxed text-faint">{hint}</p>}
      <div className="overflow-hidden rounded-panel border border-line/60 bg-surface/40">{children}</div>
    </section>
  )
}

function Item({
  action,
  overdue,
  onOpen,
}: {
  action: Outstanding
  overdue?: boolean
  onOpen: (id: number) => void
}) {
  return (
    <button
      onClick={() => onOpen(action.recording)}
      className="flex w-full items-start gap-3 border-b border-line/40 px-4 py-2.5 text-left last:border-b-0 hover:bg-raised/50"
    >
      <span className="min-w-0 flex-1">
        <span className="block text-[13px] leading-snug">{action.task}</span>
        <span className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[11px] text-faint">
          {action.owner && <span className="font-medium text-accent">{action.owner}</span>}
          {action.due && <span className={overdue ? "text-warn" : ""}>{action.due}</span>}
          <span className="truncate">{action.title}</span>
        </span>
      </span>
    </button>
  )
}

function Quiet() {
  return (
    <motion.div
      className="flex h-full flex-col items-center justify-center pb-16 text-center"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ delay: 0.1 }}
    >
      <Users size={26} className="text-faint" />
      <h2 className="mt-4 text-[14px] font-medium">Nothing to report</h2>
      <p className="mt-1 max-w-xs text-[12.5px] leading-relaxed text-soft">
        No meetings in this window, and nothing outstanding from before it.
      </p>
    </motion.div>
  )
}
