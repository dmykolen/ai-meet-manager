import { useEffect, useState } from "react"
import { motion } from "motion/react"
import { Brain, Check, Ear, FolderOpen, Mic, Rows2, Sparkles, Trash2, UserRound, X } from "lucide-react"
import { Meetings, type Density, type Person, type Settings as Values } from "../api"

export default function Settings({ onDensity }: { onDensity: (d: Density) => void }) {
  const [values, setValues] = useState<Values | null>(null)
  const [saved, setSaved] = useState(false)
  const [people, setPeople] = useState<Person[]>([])
  const [busy, setBusy] = useState("")
  const [said, setSaid] = useState("")
  const [me, setMe] = useState("")

  const teach = () =>
    run("me", async () => {
      const message = await Meetings.ThisIsMe(me.trim())
      setMe("")
      voices()
      return message
    })

  // Both of these can take a moment and both have something to report, so they
  // share one line rather than each growing its own spinner.
  const run = async (what: string, job: () => Promise<unknown>) => {
    setBusy(what)
    setSaid("")
    try {
      setSaid(String(await job()))
    } catch (e) {
      setSaid(String(e).replace(/^Error:\s*/, ""))
    } finally {
      setBusy("")
    }
  }

  const voices = () => Meetings.People().then((p) => setPeople((p as Person[]) ?? []))

  useEffect(() => {
    Meetings.Settings().then((v) => setValues(v as Values))
    voices().catch(() => {})
  }, [])

  if (!values) return null

  const save = async (next: Values) => {
    setValues(next)
    await Meetings.SaveSettings(next)
    if (next.density !== values.density) onDensity(next.density)
    setSaved(true)
    setTimeout(() => setSaved(false), 1600)
  }

  return (
    <div className="flex h-full flex-col">
      <header className="no-drag flex items-baseline gap-2.5 px-6 pb-4 pt-2">
        <h1 className="text-[24px] font-semibold tracking-[-0.02em]">Settings</h1>
        <motion.span
          className="flex items-center gap-1 text-[11.5px] text-good"
          initial={{ opacity: 0 }}
          animate={{ opacity: saved ? 1 : 0 }}
          transition={{ duration: 0.2 }}
        >
          <Check size={12} /> Saved
        </motion.span>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-10">
        <div className="flex max-w-2xl flex-col gap-7">
          <Group title="Listening" Icon={Ear}>
            <Row label="Record meetings on its own" hint="Nothing leaves this machine.">
              <Toggle on={values.listening} onChange={(on) => save({ ...values, listening: on })} />
            </Row>
            <Row
              label="Start after"
              hint="How much talking there has to be before it decides this is a meeting."
            >
              <Number
                value={values.startSpeech}
                unit="sec"
                min={5}
                max={300}
                onChange={(n) => save({ ...values, startSpeech: n })}
              />
            </Row>
            <Row label="Stop after" hint="How much quiet ends it.">
              <Number
                value={values.quietEnds}
                unit="sec"
                min={15}
                max={1800}
                onChange={(n) => save({ ...values, quietEnds: n })}
              />
            </Row>
            <Row
              label="Reach back"
              hint="How far into the past a recording starts, so a meeting noticed late keeps its opening."
            >
              <Number
                value={values.preroll}
                unit="sec"
                min={0}
                max={600}
                onChange={(n) => save({ ...values, preroll: n })}
              />
            </Row>
          </Group>

          <Group title="Transcripts" Icon={Rows2}>
            <Row
              label="Language"
              hint={'A code such as uk or en, or "auto" to work it out per meeting. Naming it is more accurate — left to guess, a Ukrainian meeting with a few borrowed words comes back written in Russian.'}
            >
              <Text
                value={values.language}
                placeholder="uk"
                width="w-20"
                onChange={(v) => setValues({ ...values, language: v })}
                onDone={() => save(values)}
              />
            </Row>
            <Row
              label="Transcription model"
              hint="Measured on three minutes of a real Ukrainian meeting: Whisper found 320 words in Ukrainian, Parakeet 161 in Russian — it picks the language itself and cannot be told. Parakeet is three times faster and better on clean, close-miked speech. Choosing it downloads 670 MB."
            >
              <Choice
                options={[
                  { id: "whisper", label: "Whisper" },
                  { id: "parakeet", label: "Parakeet" },
                ]}
                value={values.transcriber}
                onChange={(id) => save({ ...values, transcriber: id as Values["transcriber"] })}
              />
            </Row>
            <Row label="Density" hint="How tightly the rows of a transcript are packed.">
              <Choice
                options={[
                  { id: "compact", label: "Compact" },
                  { id: "comfortable", label: "Roomy" },
                ]}
                value={values.density}
                onChange={(d) => save({ ...values, density: d as Density })}
              />
            </Row>
          </Group>

          <Group title="Voices it knows" Icon={UserRound}>
            <Row
              label="This is me"
              hint="Everything the microphone hears is already one person. Say who, once, and every note and every one of your own turns carries your name from then on."
            >
              <div className="flex items-center gap-1.5">
                <input
                  value={me}
                  onChange={(e) => setMe(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && teach()}
                  placeholder="Your name"
                  className="w-32 rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 text-[12px] outline-none transition-colors placeholder:text-faint focus:border-accent/50"
                />
                <button
                  onClick={teach}
                  disabled={busy !== "" || !me.trim()}
                  className="flex items-center gap-1.5 rounded-lg bg-accent px-2.5 py-1.5 text-[11.5px] font-medium text-ink transition-opacity disabled:opacity-30"
                >
                  <Mic size={13} /> {busy === "me" ? "Learning…" : "Learn"}
                </button>
              </div>
            </Row>
            {people.length === 0 ? (
              <p className="px-4 py-3 text-[12px] leading-relaxed text-faint">
                Nobody yet. Open a meeting, click a speaker and type their name — the app
                learns what they sound like and names them by itself from then on.
              </p>
            ) : (
              people.map((p) => (
                <div
                  key={p.id}
                  className="flex items-center justify-between gap-4 border-b border-line/40 px-4 py-2.5 last:border-b-0"
                >
                  <div className="min-w-0">
                    <h3 className="text-[13px] font-medium">{p.name}</h3>
                    <p className="mt-0.5 text-[11px] text-faint">
                      {p.samples} {p.samples === 1 ? "voiceprint" : "voiceprints"} ·{" "}
                      {p.meetings} {p.meetings === 1 ? "meeting" : "meetings"}
                    </p>
                  </div>
                  <button
                    onClick={async () => {
                      await Meetings.Forget(p.name)
                      voices()
                    }}
                    title="Forget this voice. Transcripts keep the name; new meetings stop guessing it."
                    className="shrink-0 rounded-lg p-1.5 text-faint transition-colors hover:bg-raised hover:text-warn"
                  >
                    <X size={14} />
                  </button>
                </div>
              ))
            )}
          </Group>

          <Group title="Summaries" Icon={Sparkles}>
            <Row
              label="Summarise every meeting"
              hint="Off makes this a transcriber and nothing more."
            >
              <Toggle on={values.summarise} onChange={(on) => save({ ...values, summarise: on })} />
            </Row>
            <Row
              label="OpenAI key"
              hint="Only for summaries and questions. Transcription and voices run here and never leave."
            >
              <Text
                value={values.openaiKey}
                placeholder="sk-…"
                secret
                width="w-56"
                onChange={(v) => setValues({ ...values, openaiKey: v })}
                onDone={() => save(values)}
              />
            </Row>
            <Row
              label="Search by meaning"
              hint="Passages are embedded so a question finds the answer even when it uses none of the same words. Index the meetings recorded before the key was added."
            >
              <button
                onClick={() => run("index", () => Meetings.Reindex())}
                disabled={busy !== ""}
                className="flex items-center gap-1.5 rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 text-[11.5px] text-soft transition-colors hover:border-accent/40 hover:text-text disabled:opacity-40"
              >
                <Brain size={13} /> {busy === "index" ? "Indexing…" : "Index now"}
              </button>
            </Row>
            <Row label="Model" hint="Any model your key can reach.">
              <Text
                value={values.openaiModel}
                placeholder="gpt-5.4-mini"
                width="w-40"
                onChange={(v) => setValues({ ...values, openaiModel: v })}
                onDone={() => save(values)}
              />
            </Row>
          </Group>

          <Group title="Storage" Icon={FolderOpen}>
            <Row
              label="Keep audio for"
              hint="Transcripts are kept for ever. Audio is large and stops being interesting. 0 keeps everything."
            >
              <Number
                value={values.keepAudioDays}
                unit="days"
                min={0}
                max={3650}
                onChange={(n) => save({ ...values, keepAudioDays: n })}
              />
            </Row>
            <Row
              label="Delete old audio now"
              hint="Runs the same sweep that happens daily. Transcripts, summaries and analytics are never deleted — only the sound."
            >
              <button
                onClick={() => run("tidy", () => Meetings.Tidy())}
                disabled={busy !== ""}
                className="flex items-center gap-1.5 rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 text-[11.5px] text-soft transition-colors hover:border-warn/50 hover:text-text disabled:opacity-40"
              >
                <Trash2 size={13} /> {busy === "tidy" ? "Deleting…" : "Free up space"}
              </button>
            </Row>
            <Row label="Your folder" hint={values.folder}>
              <button
                onClick={() => Meetings.RevealFolder()}
                className="flex items-center gap-1.5 rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 text-[11.5px] text-soft transition-colors hover:border-accent/40 hover:text-text"
              >
                <FolderOpen size={13} /> Show
              </button>
            </Row>
          </Group>

          {said && (
            <motion.p
              initial={{ opacity: 0, y: -4 }}
              animate={{ opacity: 1, y: 0 }}
              className="rounded-panel border border-line/60 bg-surface/50 px-4 py-2.5 text-[12px] leading-relaxed text-soft"
            >
              {said}
            </motion.p>
          )}

          <p className="flex items-start gap-2 text-[11px] leading-relaxed text-faint">
            <Trash2 size={12} className="mt-0.5 shrink-0" />
            Everything this app owns is in that one folder. Drag it to the Bin and the app is gone.
          </p>
        </div>
      </div>
    </div>
  )
}

function Group({ title, Icon, children }: { title: string; Icon: typeof Ear; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="mb-2 flex items-center gap-1.5 text-[10.5px] font-semibold uppercase tracking-wider text-faint">
        <Icon size={12} /> {title}
      </h2>
      <div className="rounded-panel border border-line/60 bg-surface/40">{children}</div>
    </section>
  )
}

function Row({ label, hint, children }: { label: string; hint: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-6 border-b border-line/40 px-4 py-3 last:border-b-0">
      <div className="min-w-0">
        <h3 className="text-[13px] font-medium">{label}</h3>
        <p className="mt-0.5 text-[11px] leading-relaxed text-faint">{hint}</p>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  )
}

function Toggle({ on, onChange }: { on: boolean; onChange: (on: boolean) => void }) {
  return (
    <button
      onClick={() => onChange(!on)}
      className={`flex h-[22px] w-10 items-center rounded-full px-0.5 transition-colors ${on ? "bg-accent" : "bg-raised"}`}
    >
      <motion.span
        layout
        transition={{ type: "spring", stiffness: 620, damping: 34 }}
        className={`size-[18px] rounded-full bg-ink ${on ? "ml-auto" : ""}`}
      />
    </button>
  )
}

function Text({
  value,
  placeholder,
  width,
  secret,
  onChange,
  onDone,
}: {
  value: string
  placeholder: string
  width: string
  secret?: boolean
  onChange: (v: string) => void
  onDone: () => void
}) {
  return (
    <input
      type={secret ? "password" : "text"}
      value={value}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      onBlur={onDone}
      onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
      className={`${width} rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 text-[12px] outline-none transition-colors placeholder:text-faint focus:border-accent/50`}
    />
  )
}

/** A number with its unit attached, clamped where it is typed. */
function Number({
  value,
  unit,
  min,
  max,
  onChange,
}: {
  value: number
  unit: string
  min: number
  max: number
  onChange: (n: number) => void
}) {
  const [draft, setDraft] = useState(String(value))
  useEffect(() => setDraft(String(value)), [value])

  return (
    <div className="flex items-center gap-1.5 rounded-lg border border-line/60 bg-surface/60 px-2.5 py-1.5 focus-within:border-accent/50">
      <input
        value={draft}
        onChange={(e) => setDraft(e.target.value.replace(/[^\d]/g, ""))}
        onBlur={() => onChange(Math.min(Math.max(parseInt(draft || "0", 10), min), max))}
        onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
        className="w-10 bg-transparent text-right text-[12px] tabular-nums outline-none"
      />
      <span className="text-[11px] text-faint">{unit}</span>
    </div>
  )
}

function Choice({
  options,
  value,
  onChange,
}: {
  options: { id: string; label: string }[]
  value: string
  onChange: (id: string) => void
}) {
  return (
    <div className="flex rounded-lg bg-raised p-0.5">
      {options.map((o) => (
        <button
          key={o.id}
          onClick={() => onChange(o.id)}
          className="relative rounded-[6px] px-2.5 py-1 text-[11.5px] transition-colors"
        >
          {value === o.id && (
            <motion.span
              layoutId="choice"
              className="absolute inset-0 rounded-[6px] bg-surface"
              transition={{ type: "spring", stiffness: 500, damping: 38 }}
            />
          )}
          <span className={`relative z-10 ${value === o.id ? "text-text" : "text-faint"}`}>{o.label}</span>
        </button>
      ))}
    </div>
  )
}
