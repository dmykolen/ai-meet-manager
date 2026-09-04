import { useCallback, useEffect, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { FileAudio, Import, Mic, Trash2 } from "lucide-react"
import { Dialogs } from "@wailsio/runtime"
import { Meetings, length, when, type Listening, type Recording } from "../api"
import LiveNow from "../components/LiveNow"

/**
 * The Library: what has been recorded, newest first.
 *
 * The list shows the title the summary gave it rather than a file name, which
 * is the whole difference between an archive and something worth opening.
 */
export default function Library({ onOpen }: { onOpen: (id: number) => void }) {
  const [items, setItems] = useState<Recording[] | null>(null)
  const [live, setLive] = useState<Listening | null>(null)

  const load = useCallback(async () => {
    try {
      setItems((await Meetings.Recent(100)) as Recording[])
    } catch {
      setItems([])
    }
  }, [])

  useEffect(() => {
    const tick = () => {
      load()
      Meetings.Listening()
        .then((s) => setLive(s as Listening))
        .catch(() => {})
    }
    tick()
    // Recordings arrive and progress on their own; the list has to notice.
    const timer = setInterval(tick, 2000)
    return () => clearInterval(timer)
  }, [load])

  return (
    <div className="flex h-full flex-col">
      <header className="no-drag flex items-center justify-between px-6 pb-3 pt-2">
        <div className="flex items-baseline gap-2.5">
          <h1 className="text-[24px] font-semibold tracking-[-0.02em]">Meetings</h1>
          {items && items.length > 0 && (
            <span className="text-[11px] text-faint">{items.length}</span>
          )}
        </div>
        <button
          onClick={async () => {
            const path = await pick()
            if (path) {
              await Meetings.Import(path)
              load()
            }
          }}
          className="flex items-center gap-1.5 rounded-lg px-2.5 py-1 text-[11.5px] text-soft transition-colors hover:bg-raised hover:text-text"
        >
          <Import size={13} /> Add a file
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-8">
        {live && <LiveNow state={live} />}
        {items === null ? null : items.length === 0 ? (
          <Empty />
        ) : (
          <ul className="flex flex-col gap-2">
            <AnimatePresence initial={false}>
              {items.map((r, i) => (
                <motion.li
                  key={r.id}
                  layout
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, height: 0 }}
                  transition={{ duration: 0.28, delay: Math.min(i * 0.025, 0.3), ease: [0.22, 1, 0.36, 1] }}
                >
                  <Card
                    recording={r}
                    onOpen={() => onOpen(r.id)}
                    onDelete={async () => {
                      await Meetings.Delete(r.id)
                      load()
                    }}
                  />
                </motion.li>
              ))}
            </AnimatePresence>
          </ul>
        )}
      </div>
    </div>
  )
}

function Card({
  recording,
  onOpen,
  onDelete,
}: {
  recording: Recording
  onOpen: () => void
  onDelete: () => void
}) {
  const busy = recording.status !== "done" && recording.status !== "failed"
  const summary = recording.summary
  const [sure, setSure] = useState(false)

  // A card is a button, and a button inside a button is not allowed — so the
  // delete control is positioned over it rather than nested in it.
  return (
    <div className="group relative">
      <button
        onClick={onOpen}
        disabled={busy}
        className="w-full rounded-panel border border-line/60 bg-surface/60 px-4 py-3 text-left transition-all hover:border-line hover:bg-raised/70 disabled:cursor-default"
      >
      <div className="flex items-baseline gap-3">
        <h2 className="flex min-w-0 flex-1 items-center gap-2 truncate text-[14px] font-medium tracking-[-0.01em] group-hover:text-text">
          {recording.kind === "note" ? (
            <Mic size={13} className="shrink-0 text-faint" />
          ) : (
            <FileAudio size={13} className="shrink-0 text-faint" />
          )}
          <span className="truncate">{recording.title}</span>
        </h2>
        <span className="shrink-0 text-[11px] tabular-nums text-faint">{when(recording.started)}</span>
      </div>

      {summary?.overview && (
        <p className="mt-1 line-clamp-2 text-[12.5px] leading-relaxed text-soft">{summary.overview}</p>
      )}

      <div className="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[10.5px] text-faint">
        {busy ? (
          <Working recording={recording} />
        ) : recording.status === "failed" ? (
          <span className="text-warn">{recording.problem || "Failed"}</span>
        ) : (
          <>
            <span className="tabular-nums">{length(recording.duration)}</span>
            {recording.speakers && recording.speakers.length > 0 && (
              <span>
                {recording.speakers.length} {recording.speakers.length === 1 ? "voice" : "voices"}
              </span>
            )}
            {summary?.action_items && summary.action_items.length > 0 && (
              <span className="rounded-full bg-accent-soft/60 px-2 py-0.5 font-medium text-accent">
                {summary.action_items.length} to do
              </span>
            )}
            {summary?.decisions && summary.decisions.length > 0 && (
              <span>
                {summary.decisions.length} {summary.decisions.length === 1 ? "decision" : "decisions"}
              </span>
            )}
          </>
        )}
      </div>
      </button>

      {/* Two presses, not a dialog. A dialog for one row is heavier than the
          thing it is protecting, and an accidental first press is undone by
          moving the mouse away. */}
      <div
        onMouseLeave={() => setSure(false)}
        className="absolute right-3 top-2.5 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100"
      >
        {sure ? (
          <motion.button
            initial={{ opacity: 0, scale: 0.9 }}
            animate={{ opacity: 1, scale: 1 }}
            onClick={onDelete}
            className="rounded-lg bg-warn px-2 py-1 text-[11px] font-medium text-ink"
          >
            Delete for ever
          </motion.button>
        ) : (
          <button
            onClick={() => setSure(true)}
            title="Delete this recording"
            className="rounded-lg p-1.5 text-faint transition-colors hover:bg-raised hover:text-warn"
          >
            <Trash2 size={14} />
          </button>
        )}
      </div>
    </div>
  )
}

/** Every wait says what it is waiting for, and roughly how far along it is. */
function Working({ recording }: { recording: Recording }) {
  const label =
    recording.status === "queued"
      ? "Waiting its turn"
      : recording.status === "transcribing"
        ? "Writing it down"
        : "Reading it back"

  return (
    <span className="flex items-center gap-2">
      <span className="relative flex size-1.5">
        <motion.span
          className="absolute inline-flex size-full rounded-full bg-accent"
          animate={{ opacity: [1, 0.3, 1] }}
          transition={{ duration: 1.6, repeat: Infinity, ease: "easeInOut" }}
        />
      </span>
      <span className="text-accent">{label}</span>
      <span className="h-0.5 w-16 overflow-hidden rounded-full bg-raised">
        <motion.span
          className="block h-full rounded-full bg-accent/70"
          animate={{ width: `${Math.max(recording.progress * 100, 4)}%` }}
          transition={{ ease: "easeOut", duration: 0.5 }}
        />
      </span>
    </span>
  )
}

function Empty() {
  return (
    <motion.div
      className="flex h-full flex-col items-center justify-center pb-16 text-center"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ delay: 0.15 }}
    >
      <div className="flex size-14 items-center justify-center rounded-2xl border border-line/70 bg-surface/50">
        <svg viewBox="0 0 24 24" fill="none" strokeWidth={1.5} strokeLinecap="round" className="size-6 stroke-faint">
          <path d="M12 4v9m0 0a3 3 0 0 0 3-3V7a3 3 0 0 0-6 0v3a3 3 0 0 0 3 3Zm-6 0a6 6 0 0 0 12 0M12 16v4" />
        </svg>
      </div>
      <h2 className="mt-5 text-[15px] font-medium">Nothing recorded yet</h2>
      <p className="mt-1.5 max-w-xs text-[13px] leading-relaxed text-soft">
        Have a meeting and it will appear here on its own. Nothing to press.
      </p>
    </motion.div>
  )
}

/** The native file picker, through the Wails dialog runtime. */
async function pick(): Promise<string | null> {
  try {
    const chosen = await Dialogs.OpenFile({
      Title: "Add a recording",
      CanChooseFiles: true,
      Filters: [
        {
          DisplayName: "Audio and video",
          Pattern: "*.wav;*.m4a;*.mp3;*.mp4;*.mov;*.webm;*.aac;*.flac;*.ogg",
        },
      ],
    })
    return typeof chosen === "string" && chosen ? chosen : null
  } catch {
    // No bridge (design mode) or the person cancelled. Neither is an error.
    return null
  }
}
