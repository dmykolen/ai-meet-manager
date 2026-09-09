import { useEffect, useImperativeHandle, useRef, useState, type Ref } from "react"
import { Pause, Play, Rewind, Volume2 } from "lucide-react"
import { Meetings, clock } from "../api"

/** What the transcript needs from the player: jump here, and where are we. */
export type Controls = { play: (seconds: number) => void }

/**
 * Playback, docked under the header.
 *
 * A transcript without the audio is a guess you cannot check. Whisper mishears
 * names, numbers and anything said over somebody else, and the only way to
 * settle it is to listen — which until now meant finding the file in Finder.
 *
 * The audio is served by the app itself over a range-capable route, so seeking
 * an hour-long recording starts playing at once rather than after 230 MB.
 */
export default function Player({
  id,
  file,
  onTime,
  ref,
}: {
  id: number
  file: string
  onTime: (seconds: number) => void
  ref: Ref<Controls>
}) {
  const audio = useRef<HTMLAudioElement>(null)
  const [playing, setPlaying] = useState(false)
  const [at, setAt] = useState(0)
  const [length, setLength] = useState(0)
  const [rate, setRate] = useState(1)
  const [broken, setBroken] = useState(false)
  const [shape, setShape] = useState<number[]>([])

  // Closing the reader mid-sentence unmounts this without a pause event, and a
  // listener left deaf would never record another meeting.
  useEffect(() => () => void Meetings.Playing(false), [])

  useEffect(() => {
    Meetings.Waveform(id)
      .then((s) => setShape((s as number[]) ?? []))
      .catch(() => {})
  }, [id])

  useImperativeHandle(ref, () => ({
    play(seconds: number) {
      const el = audio.current
      if (!el) return
      el.currentTime = seconds
      el.play().catch(() => setBroken(true))
    },
  }))

  useEffect(() => {
    if (audio.current) audio.current.playbackRate = rate
  }, [rate])

  // Space plays and pauses, the way every player does — but not while
  // somebody is typing a note or a search.
  useEffect(() => {
    const press = (e: KeyboardEvent) => {
      const on = document.activeElement?.tagName
      if (e.code !== "Space" || on === "INPUT" || on === "TEXTAREA") return
      e.preventDefault()
      const el = audio.current
      if (!el) return
      el.paused ? el.play().catch(() => setBroken(true)) : el.pause()
    }
    window.addEventListener("keydown", press)
    return () => window.removeEventListener("keydown", press)
  }, [])

  if (broken) return null

  return (
    <div className="mt-4 flex items-center gap-3 rounded-panel border border-line/60 bg-surface/50 px-3 py-2">
      <audio
        ref={audio}
        src={`/audio/${encodeURIComponent(file)}`}
        preload="metadata"
        // The always-on listener cannot tell this player from a meeting: the
        // system-audio tap hears it, decides somebody is talking, and records
        // the playback back into the library. It is told to go deaf instead.
        onPlay={() => {
          setPlaying(true)
          Meetings.Playing(true)
        }}
        onPause={() => {
          setPlaying(false)
          Meetings.Playing(false)
        }}
        onEnded={() => Meetings.Playing(false)}
        onError={() => setBroken(true)}
        onLoadedMetadata={(e) => setLength(e.currentTarget.duration || 0)}
        onTimeUpdate={(e) => {
          setAt(e.currentTarget.currentTime)
          onTime(e.currentTarget.currentTime)
        }}
      />

      <button
        onClick={() => {
          const el = audio.current
          if (!el) return
          el.paused ? el.play().catch(() => setBroken(true)) : el.pause()
        }}
        title={playing ? "Pause  ␣" : "Play  ␣"}
        className="flex size-8 shrink-0 items-center justify-center rounded-full bg-accent text-ink transition-transform hover:scale-105"
      >
        {playing ? <Pause size={14} fill="currentColor" /> : <Play size={14} fill="currentColor" className="ml-0.5" />}
      </button>

      <button
        onClick={() => audio.current && (audio.current.currentTime = Math.max(at - 10, 0))}
        title="Back ten seconds"
        className="shrink-0 text-faint transition-colors hover:text-text"
      >
        <Rewind size={15} />
      </button>

      <span className="w-11 shrink-0 text-right text-[11px] tabular-nums text-soft">{clock(at)}</span>

      {/* A seek bar with no waveform is a blind scrub. The bars are the loudness
          of the file actually being played, so what is on screen is what will
          be heard, and the ones already played are lit. */}
      <div
        onClick={(e) => {
          const box = e.currentTarget.getBoundingClientRect()
          const to = ((e.clientX - box.left) / box.width) * (length || 0)
          if (audio.current) audio.current.currentTime = to
        }}
        className="group/bar flex h-7 min-w-0 flex-1 cursor-pointer items-center gap-px"
      >
        {(shape.length ? shape : Array(60).fill(0.25)).map((v, i, all) => {
          const played = (i + 1) / all.length <= at / (length || 1)
          return (
            <span
              key={i}
              style={{ height: `${Math.max(v * 100, 6)}%` }}
              className={`min-w-0 flex-1 rounded-[1px] transition-colors ${
                played ? "bg-accent" : "bg-raised group-hover/bar:bg-line"
              }`}
            />
          )
        })}
      </div>

      <span className="w-11 shrink-0 text-[11px] tabular-nums text-faint">{clock(length)}</span>

      {/* Speed, because half of listening back is skimming. */}
      <button
        onClick={() => setRate((r) => (r >= 2 ? 1 : r === 1 ? 1.5 : 2))}
        title="Playback speed"
        className="flex shrink-0 items-center gap-1 rounded-lg px-1.5 py-1 text-[11px] tabular-nums text-faint transition-colors hover:bg-raised hover:text-text"
      >
        <Volume2 size={13} />
        {rate}×
      </button>
    </div>
  )
}
