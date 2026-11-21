import { ApiError, consoleApi, unwrap } from "@axiom/api";
import { type Eggs, startEggs } from "@axiom/fx/eggs";
import type { Sea } from "@axiom/fx/sea";
import {
  type CSSProperties,
  type KeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import styles from "./easter-eggs.module.css";
import { type Answer, subscribe } from "./public-models";
import { useSite } from "./site-context";
import { type Context, type Reply, reply } from "./terminal";

// The sea's easter eggs on the home page (the design's eggs.js and its MomentsLive board, docs/frontend.md「海的彩蛋」):
// the sea-floor terminal "/" opens, the depth gauge typing axiom shows, the way back up offered on the sea floor, and
// the greeting in the browser's console with window.axiom. The rules and the moments on the sea are @axiom/fx/eggs'.
// This code is loaded after the first content, once the sea is up (eggs.ts).

declare global {
  interface Window {
    axiom?: { dive: () => void; feed: () => void; help: () => void; whale: () => void };
  }
}

const prompt = "axiom@sea:~$";
const echoMark = "$";
// for the developers who open the console: a small shark
const shark = [
  "            ,|",
  "      _____/ |__",
  "  ___/ o        `--.__",
  " <___    ____________>",
  "     \\__/  \\|",
].join("\n");
let greeted = false;

type Models = Awaited<ReturnType<Context["models"]>>;

// the site's published models, once the page's one answer has come (a failure is asked again)
function models(): Promise<Models> {
  return new Promise((done) => {
    let over = false;
    const stop = subscribe((answer: Answer) => {
      if (over || answer.status === "pending") return;
      over = true;
      done(answer.status === "ready" ? answer.models : null);
      queueMicrotask(() => stop());
    });
  });
}

// one round trip to the site's own public settings: any answer is one, a call with no response is none
async function trip(): Promise<number | null> {
  const start = performance.now();
  try {
    await unwrap(consoleApi.GET("/api/console/v1/site"));
  } catch (error) {
    if (!(error instanceof ApiError) || error.status === 0) return null;
  }
  return Math.round(performance.now() - start);
}

type Line = { id: number; text: string; echo: boolean };
let lineId = 0;

// usePrinter is the terminal's screen: its last ten lines, a reply's lines added one step apart.
function usePrinter() {
  const [lines, setLines] = useState<Line[]>([]);
  const timers = useRef(new Set<number>());
  const print = useCallback((texts: string[], step = 0, echo = false) => {
    texts.forEach((text, i) => {
      const add = () => setLines((shown) => [...shown, { id: lineId++, text, echo }].slice(-10));
      if (!step) {
        add();
        return;
      }
      const id = window.setTimeout(() => {
        timers.current.delete(id);
        add();
      }, i * step);
      timers.current.add(id);
    });
  }, []);
  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const id of pending) window.clearTimeout(id);
    };
  }, []);
  return { lines, setLines, print };
}

type Typed = { open: boolean; back: Element | null; history: string[]; at: number };

// keys answers the terminal's input: Enter runs the line, ↑ and ↓ go through the lines typed before, Escape closes.
// The keys are the terminal's own: not the whale's word, the page's shortcuts or the menu's Escape.
function keys(typed: Typed, hide: () => void, echo: (line: string) => void, run: (line: string) => Promise<void>) {
  return (e: KeyboardEvent<HTMLInputElement>) => {
    e.stopPropagation();
    const field = e.currentTarget;
    if (e.key === "Escape") return hide();
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      typed.at = Math.min(typed.history.length, Math.max(0, typed.at + (e.key === "ArrowUp" ? -1 : 1)));
      field.value = typed.history[typed.at] ?? "";
      return;
    }
    if (e.key !== "Enter") return;
    const line = field.value.trim();
    field.value = "";
    if (!line) return;
    typed.history.push(line);
    typed.at = typed.history.length;
    echo(line);
    void run(line);
  };
}

// useTerminal is the sea-floor terminal: it fades in and out (250 ms), takes the focus when opened and gives it back
// to where it was when closed; the first time it opens it says hello.
function useTerminal(answer: (line: string) => Promise<Reply>, hello: string) {
  const [open, setOpen] = useState(false);
  const [fading, setFading] = useState(false);
  const { lines, setLines, print } = usePrinter();
  const box = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const typed = useRef<Typed>({ open: false, back: null, history: [], at: 0 });
  const greeting = useRef(hello);
  greeting.current = hello;
  const show = useCallback(() => {
    if (typed.current.open) return;
    typed.current.open = true;
    typed.current.back = document.activeElement;
    for (const fade of box.current?.getAnimations() ?? []) fade.cancel();
    setFading(false);
    setOpen(true);
    setLines((shown) => (shown.length ? shown : [{ id: lineId++, text: greeting.current, echo: false }]));
  }, [setLines]);
  const hide = useCallback(() => {
    if (!typed.current.open) return;
    typed.current.open = false;
    setOpen(false);
    setFading(true);
    const fade = box.current?.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 250, easing: "ease-out" });
    if (fade) fade.onfinish = () => setFading(false);
    // the focus leaves the fading terminal for where it was (the page itself when that was nothing in particular)
    input.current?.blur();
    const back = typed.current.back;
    if (back instanceof HTMLElement && back.isConnected) back.focus({ preventScroll: true });
  }, []);
  useLayoutEffect(() => {
    if (!open) return;
    box.current?.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 250, easing: "ease-out" });
    input.current?.focus({ preventScroll: true });
  }, [open]);
  const run = useCallback(
    async (line: string) => {
      show();
      const got = await answer(line);
      if (got === "clear") setLines([]);
      else if (got === "close") hide();
      else print(got.print, got.step);
    },
    [answer, show, hide, print, setLines],
  );
  const isOpen = useCallback(() => typed.current.open, []);
  const onKey = keys(typed.current, hide, (line) => print([line], 0, true), run);
  return { hidden: !open && !fading, lines, box, input, show, run, onKey, isOpen };
}

type Terminal = ReturnType<typeof useTerminal>;

function TerminalBox({ terminal }: { terminal: Terminal }) {
  const { t } = useTranslation();
  return (
    <div
      ref={terminal.box}
      className={styles.term}
      role="dialog"
      aria-label={t("eggs.terminal")}
      hidden={terminal.hidden}
    >
      <div className={styles.out} aria-live="polite">
        {terminal.lines.map((line) => (
          <div key={line.id} className={styles.line}>
            {line.echo ? <span className={styles.prompt}>{echoMark}</span> : null}
            {line.text}
          </div>
        ))}
      </div>
      <label className={styles.in}>
        <span className={styles.prompt}>{prompt}</span>
        <input
          ref={terminal.input}
          type="text"
          spellCheck={false}
          autoComplete="off"
          aria-label={t("eggs.input")}
          onKeyDown={terminal.onKey}
        />
      </label>
    </div>
  );
}

// the gauge's scale: a tick every 30 m down to 1200 m, a longer one and its depth every 300 m
const ticks = Array.from({ length: 41 }, (_, i) => i);

// The depth gauge (typing axiom shows it for 12 s): where the page is between the surface and the floor.
function Gauge({ gauge, scroller }: { gauge: RefObject<HTMLDivElement | null>; scroller: HTMLElement | null }) {
  const mark = useRef<HTMLDivElement>(null);
  const depth = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!scroller) return;
    const follow = () => {
      const max = scroller.scrollHeight - scroller.clientHeight;
      const d = max > 0 ? Math.min(1, scroller.scrollTop / max) : 0;
      mark.current?.style.setProperty("--d", d.toFixed(4));
      if (depth.current) depth.current.textContent = `−${Math.round(d * 1200)}m`;
    };
    follow();
    scroller.addEventListener("scroll", follow, { passive: true });
    return () => scroller.removeEventListener("scroll", follow);
  }, [scroller]);
  return (
    <div ref={gauge} className={styles.gauge} aria-hidden="true">
      {ticks.map((i) => (
        <span
          key={i}
          className={i % 10 ? styles.tick : styles.major}
          style={{ "--y": `${i * 15}px` } as CSSProperties}
        />
      ))}
      {ticks
        .filter((i) => i % 10 === 0)
        .map((i) => (
          <span key={i} className={styles.depth} style={{ "--y": `${i * 15}px` } as CSSProperties}>
            {`−${i * 30}m`}
          </span>
        ))}
      <div ref={mark} className={styles.mark}>
        <span ref={depth} />
        <i />
      </div>
    </div>
  );
}

// The 12 s the gauge shows for: in fast, held, out fast (eggs.js gauge).
const gaugeShow: Keyframe[] = [
  { opacity: 0 },
  { opacity: 1, offset: 0.02 },
  { opacity: 1, offset: 0.98 },
  { opacity: 0 },
];

// useEggs starts the eggs (@axiom/fx/eggs) on the frame and puts window.axiom up, both until the eggs go.
function useEggs(
  frame: HTMLElement,
  sea: Sea,
  { terminal, whale, eggs }: { terminal: Terminal; whale: RefObject<() => void>; eggs: RefObject<Eggs | null> },
) {
  const gauge = useRef<HTMLDivElement>(null);
  const [floor, setFloor] = useState(false);
  const { show, run, isOpen } = terminal;
  useEffect(() => {
    const running = startEggs(frame, sea, {
      glow: styles.glow ?? "",
      terminal: { open: isOpen, show },
      gauge: () => gauge.current?.animate(gaugeShow, { duration: 12_000, easing: "linear" }),
      floor: setFloor,
    });
    eggs.current = running;
    window.axiom = {
      dive: () => void run("dive"),
      feed: () => void run("feed"),
      help: show,
      whale: () => whale.current(),
    };
    return () => {
      running.destroy();
      eggs.current = null;
      window.axiom = undefined;
    };
  }, [frame, sea, show, run, isOpen, whale, eggs]);
  return { gauge, floor };
}

// The console's greeting, once per page: a shark and the commands that do real things to the sea.
function useGreeting() {
  const { t } = useTranslation();
  useEffect(() => {
    if (greeted) return;
    greeted = true;
    console.log(
      `%c${shark}\n\n%c${t("script.bed.tagline")}%c\n${t("eggs.console")}`,
      "font-family:monospace;color:#4ccce6",
      "font:600 16px sans-serif",
      "font:12px monospace;color:#b0b4ba",
    );
  }, [t]);
}

// SeaEggs puts the eggs on the frame (.tr) over its running sea; whale calls the whale on purpose.
export function SeaEggs({ frame, sea, whale }: { frame: HTMLElement; sea: Sea; whale: () => void }) {
  const { t } = useTranslation();
  const { join } = useSite();
  const eggs = useRef<Eggs | null>(null);
  const latest = useRef({ t, join, whale });
  latest.current = { t, join, whale };
  const summon = useRef(() => latest.current.whale());
  const answer = useCallback(
    (line: string) =>
      reply(line, {
        t: latest.current.t,
        models,
        trip,
        host: window.location.host,
        open: latest.current.join === "open",
        acts: {
          feed: () => eggs.current?.feed(),
          dive: () => eggs.current?.dive(),
          surface: () => eggs.current?.surface(),
          milestone: () => eggs.current?.milestone(),
          whale: summon.current,
        },
      }),
    [],
  );
  const terminal = useTerminal(answer, t("eggs.hello"));
  const { gauge, floor } = useEggs(frame, sea, { terminal, whale: summon, eggs });
  useGreeting();
  const ho = frame.querySelector(".ho");
  if (!ho) return null;
  return createPortal(
    <>
      <Gauge gauge={gauge} scroller={frame.querySelector(".hs")} />
      <button
        type="button"
        className={styles.floor}
        data-on={floor || undefined}
        onClick={() => eggs.current?.surface()}
      >
        {t("eggs.floor")}
      </button>
      <TerminalBox terminal={terminal} />
    </>,
    ho,
  );
}
