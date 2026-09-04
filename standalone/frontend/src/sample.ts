// Sample data for working on the interface without the models.
//
// Only reachable when the Wails bridge is absent AND the build is a development
// one: `import.meta.env.DEV` is a compile-time constant, so Vite removes this
// file entirely from a production bundle. Nothing here ever ships.
import type { Answer, Meeting, Recording, Settings } from "./api"

const summary = {
  title: "Скрипти, безпека і Vodafone",
  overview:
    "Коротко пройшлися по поточних задачах команди: Таня працює над скриптами для витягування даних, Діма — над архітектурними й безпековими питаннями, а Олена — над пакетом документів для Vodafone.",
  chapters: [
    { start: 282, title: "Скрипти та дані", summary: "" },
    { start: 441, title: "Безпека та доступ", summary: "" },
    { start: 733, title: "Матеріали для Vodafone", summary: "" },
  ],
  topics: ["скрипти", "безпека", "Vodafone"],
  decisions: [
    "Продукт не закривають повністю, а обмежують для зовнішнього світу; доступ лишиться через VPN.",
    "Питання з безпекою планують закрити до 3-го числа.",
  ],
  action_items: [
    { task: "Продовжити розбиратися зі скриптами для витягування даних", owner: "Tanya", due: "", done: false },
    { task: "Додати матеріали відповіді на запитання Vodafone", owner: "Olena", due: "сьогодні", done: false },
  ],
  open_questions: ["Які саме IP-діапазони треба вказати для обмежень доступу?"],
}

const recordings: Recording[] = [
  {
    id: 1, kind: "meeting", audio: "sample-1.wav", title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
    duration: 1826, language: "uk", status: "done", progress: 1, turns: 153,
    speakers: ["Olena", "Tanya", "Dmytro Mykolenko"], summary,
  },
  {
    id: 2, kind: "meeting", audio: "sample-2.wav", title: "Рефайнмент рольової моделі", started: new Date(Date.now() - 9e7).toISOString(),
    duration: 2387, language: "uk", status: "summarising", progress: 0.9, turns: 210,
    speakers: ["Olena", "Bogdan"],
  },
  {
    id: 3, kind: "note", audio: "sample-3.wav", title: "note 2026-09-01 08:12.wav", started: new Date(Date.now() - 1.8e8).toISOString(),
    duration: 214, language: "uk", status: "transcribing", progress: 0.35, turns: 0,
  },
  {
    id: 4, kind: "meeting", audio: "sample-4.wav", title: "meeting 2026-08-30 22:39.wav", started: new Date(Date.now() - 2.6e8).toISOString(),
    duration: 0, language: "", status: "failed", progress: 0, turns: 0,
    problem: "could not read the audio: no data chunk",
  },
]

const transcript = [
  { start: 42, end: 44, speaker: "Olena", text: "Привіт, привіт." },
  { start: 55, end: 59, speaker: "Olena", text: "Таня, з першого вересня ти вже вийшла на роботу?" },
  { start: 72, end: 80, speaker: "Tanya", text: "Прикольно. А я тут хвастаюсь, що у нас в школі вже вчора сказали, що ніхто нікуди не йде." },
  { start: 282, end: 291, speaker: "Tanya", text: "Вчора розбирався з простором, сьогодні вже починав робити скрипти." },
  { start: 441, end: 452, speaker: "Dmytro Mykolenko", text: "Продукт повністю не закриваємо — обмежуємо для зовнішнього світу, доступ лишається через VPN." },
]

export const sample = {
  Recent: async () => recordings,
  Open: async (id: number): Promise<Meeting> => ({
    ...(recordings.find((r) => r.id === id) ?? recordings[0]),
    transcript,
  }),
  Search: async (q: string) => [
    { recording: 1, title: summary.title, start: 441, speaker: "Dmytro Mykolenko", text: transcript[4].text },
    { recording: 1, title: summary.title, start: 282, speaker: "Tanya", text: transcript[3].text },
  ].filter((h) => h.text.toLowerCase().includes(String(q).toLowerCase().split(/\s+/).pop() ?? "")),
  Ask: async (): Promise<Answer> => ({
    text: "Домовилися не закривати продукт повністю, а обмежити доступ для зовнішнього світу через VPN. Питання з безпекою планують закрити до 3-го числа.",
    sources: [
      { recording: 1, title: summary.title, start: 441, speaker: "Dmytro Mykolenko", text: transcript[4].text },
      { recording: 1, title: summary.title, start: 282, speaker: "Tanya", text: transcript[3].text },
    ],
  }),
  Rename: async () => {},
  SaveNote: async () => {},
  Delete: async () => {},
  Import: async () => recordings[0],
  RevealFolder: async () => {},
  Settings: async (): Promise<Settings> => ({
    language: "uk", transcriber: "whisper" as const, openaiKey: "sk-demo", openaiModel: "gpt-5.4-mini",
    summarise: true, density: "compact", listening: true,
    startSpeech: 20, quietEnds: 180, preroll: 300, keepAudioDays: 30,
    folder: "/Users/you/MeetingTranscriber",
  }),
  Actions: async () => [
    { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
      index: 0, task: "Продовжити розбиратися зі скриптами для витягування даних", owner: "Tanya", due: "", done: false },
    { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
      index: 1, task: "Додати матеріали відповіді на запитання Vodafone", owner: "Olena", due: "сьогодні", done: false },
  ],
  Tick: async () => {},
  Markdown: async () => "# " + summary.title + "\n\n" + summary.overview,
  SaveSettings: async () => {},
  State: async () => ({ stage: "ready" as const, what: "", fraction: 1, done: 0, total: 0 }),

  Listening: () => ({
    phase: "listening",
    kind: "meeting",
    elapsed: 0,
    quiet: 0,
    system: true,
    problem: "",
  }),
  Summaries: () => true,
  Again: async () => {},
  ThisIsMe: async (name: string) => `Learnt your voice from 6 recordings. Your turns are now named ${name}.`,
  Reindex: async () => "Indexed 12 recordings. 486 passages searchable by meaning, 0 by keyword only.",
  Tidy: async () => "Deleted 3 recordings, 412 MB. The transcripts are untouched.",

  Analytics: async (id: number) => id === 3 ? ({
    speech: 190, silence: 24, overlap: 0, words: 402, pace: 127.0, balance: 0,
    speakers: [{ speaker: "You", seconds: 190, share: 1, turns: 31, longest: 41, words: 402, pace: 127, questions: 2 }],
    busiest: Array.from({ length: 48 }, (_, i) => ({ at: i * 4.5, words: Math.round(20 + 14 * Math.sin(i / 3)) })),
  }) : ({
    speech: 1490, silence: 310, overlap: 46, words: 3120, pace: 125.6, balance: 0.87,
    speakers: [
      { speaker: "Olena", seconds: 700, share: 0.45, turns: 61, longest: 92, words: 1520, pace: 130.3, questions: 14 },
      { speaker: "Tanya", seconds: 540, share: 0.35, turns: 48, longest: 71, words: 1080, pace: 120.0, questions: 5 },
      { speaker: "Dmytro Mykolenko", seconds: 310, share: 0.2, turns: 44, longest: 38, words: 520, pace: 100.6, questions: 9 },
    ],
    busiest: Array.from({ length: 48 }, (_, i) => ({ at: i * 37.5, words: Math.round(40 + 55 * Math.sin(i / 4) + (i % 5) * 9) })),
  }),

  People: async () => [
    { id: 1, name: "Olena", samples: 6, meetings: 12 },
    { id: 2, name: "Tanya", samples: 4, meetings: 9 },
    { id: 3, name: "Dmytro Mykolenko", samples: 8, meetings: 21 },
  ],
  Forget: async () => {},
  Summarise: async () => {},

  Live: () => [
    { at: 12, who: "them", text: "Таня, з першого вересня ти вже вийшла на роботу?" },
    { at: 21, who: "you", text: "Так, з першого. Я вже дивлюся на скрипти." },
    { at: 34, who: "them", text: "Добре, тоді давай о четвертій зберемося і подивимося баги." },
  ],

  Brief: async (days: number) => ({
    since: new Date(Date.now() - days * 864e5).toISOString(),
    minutes: 148,
    meetings: [],
    voices: ["Dmytro Mykolenko", "Olena", "Tanya"],
    decided: [
      { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
        text: "Продукт не закривають повністю, а обмежують для зовнішнього світу; доступ лишиться через VPN." },
      { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
        text: "Питання з безпекою планують закрити до 3-го числа." },
    ],
    mine: [
      { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
        index: 0, task: "Продовжити розбиратися зі скриптами для витягування даних", owner: "Tanya", due: "", done: false },
      { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(),
        index: 1, task: "Додати матеріали відповіді на запитання Vodafone", owner: "Olena", due: "сьогодні", done: false },
    ],
    overdue: [
      { recording: 2, title: "Рефайнмент рольової моделі", started: new Date(Date.now() - 5 * 864e5).toISOString(),
        index: 0, task: "Узгодити перелік ролей із безпекою", owner: "Dmytro Mykolenko", due: "цього тижня", done: false },
    ],
    nagging: [
      { text: "Які саме IP-діапазони треба вказати для обмежень доступу?", times: 3,
        said: [
          { recording: 1, title: summary.title, started: new Date(Date.now() - 3e6).toISOString(), text: "" },
          { recording: 2, title: "Рефайнмент рольової моделі", started: new Date(Date.now() - 5 * 864e5).toISOString(), text: "" },
          { recording: 3, title: "Планування спринту", started: new Date(Date.now() - 12 * 864e5).toISOString(), text: "" },
        ] }],
  }),
  Record: () => undefined,
}
