import { useState } from "react"
import Head from "../components/Head"
import { AnimatePresence, motion } from "motion/react"
import { Sparkles } from "lucide-react"
import { Meetings, clock, type Answer } from "../api"
import NeedsKey from "../components/NeedsKey"

/**
 * Ask.
 *
 * Every answer carries the passages it came from, because an assistant that
 * cannot show its working is one you have to check by hand anyway.
 */
export default function Ask({ onOpen }: { onOpen: (id: number) => void }) {
  const [question, setQuestion] = useState("")
  const [answer, setAnswer] = useState<Answer | null>(null)
  const [thinking, setThinking] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)

  const send = async () => {
    const q = question.trim()
    if (!q || thinking) return
    setThinking(true)
    setProblem(null)
    setAnswer(null)
    try {
      setAnswer((await Meetings.Ask(q)) as Answer)
    } catch (e) {
      setProblem(String(e).replace(/^Error:\s*/, ""))
    } finally {
      setThinking(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <Head title="Запитати" />

      <div className="no-drag px-4">
        <div className="mx-auto w-full max-w-[78ch]">
          <NeedsKey what="Ask" />
        <div className="mt-2 flex items-center gap-2 rounded-lg border border-line/60 bg-surface/60 px-3 py-2.5 transition-colors focus-within:border-accent/50">
          <input
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && send()}
            placeholder="Що ми вирішили щодо міграції?"
            className="min-w-0 flex-1 bg-transparent text-[14px] outline-none placeholder:text-faint"
          />
          <button
            onClick={send}
            disabled={thinking || !question.trim()}
            className="shrink-0 rounded-lg bg-accent px-3 py-1.5 text-[12px] font-medium text-ink transition-opacity disabled:opacity-30"
          >
            {thinking ? "Думаю…" : "Ask"}
          </button>
        </div>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-10 pt-5">
        <div className="mx-auto w-full max-w-[78ch]">
        <AnimatePresence mode="wait">
          {thinking && (
            <motion.div key="wait" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
              <div className="flex flex-col gap-2">
                {[0, 1, 2].map((i) => (
                  <motion.div
                    key={i}
                    className="h-3 rounded bg-raised"
                    style={{ width: `${[92, 78, 46][i]}%` }}
                    animate={{ opacity: [0.35, 0.7, 0.35] }}
                    transition={{ duration: 1.5, repeat: Infinity, delay: i * 0.15 }}
                  />
                ))}
              </div>
            </motion.div>
          )}

          {problem && !thinking && (
            <motion.p key="problem" initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="text-[13px] text-warn">
              {problem}
            </motion.p>
          )}

          {answer && !thinking && (
            <motion.div
              key="answer"
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.3, ease: [0.22, 1, 0.36, 1] }}
            >
              <p className="whitespace-pre-wrap text-[15px] leading-[1.7]">{answer.text}</p>

              {answer.sources.length > 0 && (
                <div className="mt-7 border-t border-line/50 pt-5">
                  <h3 className="mb-3 text-[11px] font-semibold uppercase tracking-wider text-faint">
                    From these passages
                  </h3>
                  <ul className="flex flex-col gap-2">
                    {answer.sources.slice(0, 8).map((h, i) => (
                      <motion.li
                        key={i}
                        initial={{ opacity: 0, y: 6 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ delay: 0.05 * i }}
                        className="rounded-lg border border-line/50 bg-surface/40"
                      >
                        <button onClick={() => onOpen(h.recording)} className="w-full px-3.5 py-2.5 text-left transition-colors hover:bg-raised/50">
                        <div className="flex items-baseline gap-2 text-[10.5px] text-faint">
                          <span className="truncate">{h.title}</span>
                          <span className="tabular-nums">{clock(h.start)}</span>
                          {h.speaker && <span className="text-accent">{h.speaker}</span>}
                        </div>
                        <p className="mt-0.5 text-[12.5px] leading-relaxed text-soft">{h.text}</p>
                        </button>
                      </motion.li>
                    ))}
                  </ul>
                </div>
              )}
            </motion.div>
          )}
        </AnimatePresence>
        </div>
      </div>
    </div>
  )
}
