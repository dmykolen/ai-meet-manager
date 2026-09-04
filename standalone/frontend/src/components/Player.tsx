import { useEffect, useImperativeHandle, useRef, useState, type Ref } from "react"
import { Pause, Play, Rewind, Volume2 } from "lucide-react"
import { clock } from "../api"

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
  file,
  onTime,
  ref,
}: {
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
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
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

      <input
        type="range"
        min={0}
        max={length || 1}
        step={0.5}
        value={at}
        onChange={(e) => audio.current && (audio.current.currentTime = Number(e.target.value))}
        className="h-1 min-w-0 flex-1 cursor-pointer appearance-none rounded-full bg-raised accent-accent"
        style={{
          background: `linear-gradient(to right, var(--color-accent) ${(at / (length || 1)) * 100}%, var(--color-raised) 0%)`,
        }}
      />

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
