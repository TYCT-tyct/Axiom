import { reducedMotion, timeline } from "@axiom/fx";
import type zh from "../locales/zh";
import { firstSeen, type Script } from "./script";

// The three steps' script (Site.dc.html M3): a key is made, the two lines change, the first request comes back, and
// round again; a click on a step starts from it. The figures are the board's illustration, not an account's.
type Word = keyof typeof zh.script.steps;
type State = {
  step: number;
  tl: number;
  kBtn: string;
  kRow: string;
  kKey: string;
  kCp: string;
  kCpT: Word;
  kDot: boolean;
  kStat: Word;
  cStat: Word | "";
  cU0: string;
  cU1: string;
  // how much of the site's API address is typed in: null for all of it
  cUrl: number | null;
  cK0: string;
  cK1: string;
  cKey: string;
  rMs: string;
  rCode: string;
  rCls: string;
  rTxt: Word;
  rKv: string;
};

const KEY = "sk-ax-7f3c9a1e5b02";
const KEY_MASK = "sk-ax-7f3c••••••••";
const GL = "@%#*+=-:SXt8";
const glyph = () => GL[Math.floor(Math.random() * GL.length)] as string;
// the finished picture: what the screen shows at rest, before it is seen and under reduced motion
const DONE: State = {
  step: 4,
  tl: 1,
  kBtn: "",
  kRow: "",
  kKey: KEY_MASK,
  kCp: "on",
  kCpT: "copied",
  kDot: true,
  kStat: "created",
  cStat: "saved",
  cU0: "del",
  cU1: "",
  cUrl: null,
  cK0: "del",
  cK1: "",
  cKey: KEY_MASK,
  rMs: "412ms",
  rCode: "200",
  rCls: "ok",
  rTxt: "ok",
  rKv: "",
};
const START: State = {
  step: 1,
  tl: 0,
  kBtn: "",
  kRow: "off",
  kKey: "",
  kCp: "",
  kCpT: "copy",
  kDot: false,
  kStat: "noKey",
  cStat: "",
  cU0: "",
  cU1: "off",
  cUrl: 0,
  cK0: "",
  cK1: "off",
  cKey: "",
  rMs: "",
  rCode: "---",
  rCls: "",
  rTxt: "waitSend",
  rKv: "off",
};
const waiting = { rMs: "", rCode: "---", rCls: "", rTxt: "waitSend", rKv: "off" } as const;
// the state each step begins from: everything before it finished, everything after it untouched
const AT: State[] = [
  START,
  { ...START, step: 2, tl: 0.5, kRow: "", kKey: KEY_MASK, kCp: "on", kCpT: "copied", kDot: true, kStat: "created" },
  { ...DONE, step: 3, ...waiting },
];

export type Input = { t: (word: Word) => string };

const on = (yes: boolean) => (yes ? "on" : "");
// a deleted line of the diff carries its minus sign
const minus = (cls: string) => (cls === "del" ? "−" : "");
// the step playing is on, those before it done
const stepClass = (playing: number, step: number) => (playing === step ? "on" : playing > step ? "done" : "");

// The screen's run. Every play carries a number; clicking a step starts a new play, and the old one stops at its next
// wait, which reads the number (live).
class StepsRun implements Script<Input> {
  private readonly clock;
  private readonly still = reducedMotion();
  private readonly seen;
  private readonly state: State;
  private run = 0;
  private dead = false;
  private readonly go = [0, 1, 2].map((i) => () => {
    if (!this.still) this.play(i);
  });
  private readonly steps = [this.makeKey, this.editConfig, this.firstRequest];

  // url is where the reader's base_url goes: the site's API address
  constructor(
    stage: HTMLElement,
    private readonly changed: () => void,
    private readonly url: () => string,
  ) {
    this.state = { ...DONE };
    this.clock = timeline(stage);
    this.seen = firstSeen(stage, 0.35);
    this.seen.promise.then(() => {
      if (!this.still && this.run === 0) this.play(0);
    });
  }

  private set(next: Partial<State>) {
    Object.assign(this.state, next);
    this.changed();
  }
  private live(own: number) {
    return own === this.run && !this.dead;
  }
  private async wait(own: number, ms: number) {
    return (await this.clock.sleep(ms)) && this.live(own);
  }
  // the rail runs from one node towards the next while a step plays
  private rail(own: number, a: number, b: number, ms: number) {
    const t0 = this.clock.now();
    this.clock.each((now) => {
      if (!this.live(own)) return false;
      const p = Math.min(1, (now - t0) / ms);
      this.set({ tl: a + (b - a) * p });
      return p < 1;
    });
  }
  // characters settle from left to right, each one tumbling through the sea's glyphs first
  private async decode(own: number, text: string, ms: number) {
    const t0 = this.clock.now();
    for (;;) {
      const p = Math.min(1, (this.clock.now() - t0) / ms);
      this.set({ kKey: [...text].map((c, j) => (p >= (j + 1) / text.length ? c : glyph())).join("") });
      if (p >= 1) return true;
      if (!(await this.wait(own, 40))) return false;
    }
  }
  // types n characters in, one each ms, writing how many are in with to
  private async type(own: number, n: number, ms: number, to: (j: number) => Partial<State>) {
    for (let j = 1; j <= n; j++) {
      this.set(to(j));
      if (!(await this.wait(own, ms))) return false;
    }
    return true;
  }

  private async makeKey(own: number) {
    this.rail(own, 0, 0.5, 2600);
    this.set({ kBtn: "press" });
    if (!(await this.wait(own, 250))) return false;
    this.set({ kBtn: "", kRow: "", kStat: "creating" });
    if (!(await this.decode(own, KEY, 700))) return false;
    this.set({ kStat: "created", kDot: true });
    if (!(await this.wait(own, 600))) return false;
    this.set({ kKey: KEY_MASK, kCp: "on", kCpT: "copied" });
    return this.wait(own, 900);
  }
  private async editConfig(own: number) {
    this.rail(own, 0.5, 1, 3200);
    this.set({ cStat: "editing", cU0: "del", cU1: "" });
    if (
      !(await this.wait(own, 250)) ||
      !(await this.type(own, this.url().length, 28, (j) => ({ cUrl: j }))) ||
      !(await this.wait(own, 250))
    )
      return false;
    this.set({ cK0: "del", cK1: "" });
    if (
      !(await this.wait(own, 250)) ||
      !(await this.type(own, KEY_MASK.length, 34, (j) => ({ cKey: KEY_MASK.slice(0, j) })))
    )
      return false;
    this.set({ cStat: "saved" });
    return this.wait(own, 800);
  }
  private async firstRequest(own: number) {
    this.set({ rTxt: "waitReply", rCode: "" });
    const t0 = this.clock.now();
    while (this.clock.now() - t0 < 900) {
      this.set({ rMs: `${Math.round((this.clock.now() - t0) * 0.46)}ms`, rCode: glyph() + glyph() + glyph() });
      if (!(await this.wait(own, 50))) return false;
    }
    this.set({ rMs: "412ms", rCode: "200", rCls: "ok", rTxt: "ok" });
    if (!(await this.wait(own, 350))) return false;
    this.set({ rKv: "" });
    return this.wait(own, 600);
  }

  // from a step to the last, a rest on the finished picture, and round again from the first
  private async play(from: number) {
    const own = ++this.run;
    for (let i = from; this.live(own); i = (i + 1) % 3) {
      this.set({ ...AT[i] });
      if (!(await this.wait(own, 450)) || !(await this.steps[i]?.call(this, own))) return;
      if (i < 2) continue;
      this.set({ step: 4 });
      if (!(await this.wait(own, 3600))) return;
    }
  }

  values({ t }: Input) {
    const { state, go } = this;
    return {
      m3_go0: go[0],
      m3_go1: go[1],
      m3_go2: go[2],
      m3_kBtn: state.kBtn,
      m3_kRow: state.kRow,
      m3_kKey: state.kKey,
      m3_kCp: state.kCp,
      m3_kCpT: t(state.kCpT),
      m3_kDot: on(state.kDot),
      m3_kStat: t(state.kStat),
      m3_kTime: state.kRow === "off" ? "" : t("justNow"),
      m3_cStat: state.cStat ? t(state.cStat) : "",
      m3_cU0: state.cU0,
      m3_cU0m: minus(state.cU0),
      m3_cU1: state.cU1,
      m3_cUrl: this.url().slice(0, state.cUrl ?? undefined),
      m3_cK0: state.cK0,
      m3_cK0m: minus(state.cK0),
      m3_cK1: state.cK1,
      m3_cKey: state.cKey,
      m3_rMs: state.rMs,
      m3_rCode: state.rCode,
      m3_rCls: state.rCls,
      m3_rTxt: t(state.rTxt),
      m3_rKv: state.rKv,
      m3_tlW: `calc(${(state.tl * 100).toFixed(2)}%)`,
      m3_s0: stepClass(state.step, 1),
      m3_s1: stepClass(state.step, 2),
      m3_s2: stepClass(state.step, 3),
    };
  }

  destroy() {
    this.dead = true;
    this.seen.cancel();
    this.clock.end();
  }
}

export const start = (stage: HTMLElement, changed: () => void, url: () => string): Script<Input> =>
  new StepsRun(stage, changed, url);
