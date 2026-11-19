import { onMotion, reducedMotion, timeline } from "@axiom/fx";
import figureUrl from "./connect-figure.svg?url";
import { along, at, figure, Machine, type Point, points, trailAt } from "./connect-machine";
import {
  addedLength,
  addressLine,
  cardHoles,
  copyText,
  glyphAt,
  keyLine,
  type Texts,
  type Tool,
  textOf,
  tools,
} from "./connect-tools";
import { Wire } from "./connect-wire";
import { firstSeen, type Script, turns } from "./script";

// The connect screen's script (Site.dc.html M1): the tool's configuration is decoded in; the machine plugs into the one
// input, a call runs down the cable, the shaft climbs to its vendor's level, the wiper turns to the model's contact, and
// the vendor's module lights. The machine is an SVG fetched as the screen nears the view and put in place; its
// story rests while the screen is out of view or the tab hidden, and under reduced motion it stands in its wired
// state, with the key in and the lamp on.
//
// What an interaction sets going is that interaction's: a copy's answer and its word, a tool's decode, card height and
// replug. A newer one of the same kind, a tool picked (which also ends the copy's word, about the tool before), the
// screen ending, and reduced motion being asked for (which settles everything in its final state) each end it. The
// setting is read when each thing starts, never kept from the screen's start.

const CALLS: [number, number, string][] = [
  [0, 0, "claude-opus-5.5"],
  [1, 0, "gpt-6.1-sol"],
  [2, 0, "gemini-4-argon"],
  [3, 0, "deepseek-v4-pro"],
  [0, 1, "claude-sonnet-5.5"],
  [4, 0, "kimi-k3"],
  [1, 1, "gpt-6-astra"],
  [5, 0, "glm-5.2"],
  [6, 0, "qwen-max"],
  [0, 2, "claude-haiku-5.5"],
];
const VENDORS = ["Anthropic", "OpenAI", "Google", "DeepSeek", "Moonshot", "Zhipu", "Qwen"];
const rows = 9;
const ease = (p: number) => (p < 0.5 ? 4 * p * p * p : 1 - (-2 * p + 2) ** 3 / 2);
const easeOutBack = (p: number) => 1 + 2.2 * (p - 1) ** 3 + 1.2 * (p - 1) ** 2;
const easeIn = (p: number) => p * p * p;
const scramble = (text: string, from: number, count: number, tick: number) =>
  [...text.slice(from, from + count)].map((_, m) => glyphAt(from + m, tick)).join("");

// The display holds about as many characters as the longest model name it shows.
const displayed = (name: string) => {
  const upper = name.toUpperCase();
  return upper.length > 17 ? `${upper.slice(0, 16)}…` : upper;
};

type State = {
  tool: number;
  ct: number | null;
  copied: "" | "done" | "failed";
  pulse: boolean;
  call: [number, number, string] | null;
  rct: number | null;
};

// which of the copy button's words fits: it cannot copy without an address, it says how a copy ended, and otherwise
// what it copies for the tool shown
const copyState = (known: boolean, copied: "" | "done" | "failed", copies: Tool["copy"]) => {
  if (!known) return "copyUnavailable";
  return copied === "done" ? "copied" : copied === "failed" ? "copyFailed" : copies;
};

// The figure once it has come: a screen built again (the layout changed) puts it in place without asking again. One still
// on its way is the screen's, and stops with it.
let fetched: string | undefined;

class ConnectRun implements Script<Texts> {
  // the clock of the motion setting in force: a new one when the reader changes it
  private clock;
  private readonly seen;
  private readonly state: State = { tool: 0, ct: null, copied: "", pulse: false, call: null, rct: null };
  private machine: Machine | undefined;
  private wire: Wire | undefined;
  private texts: Texts | undefined;
  private current = 0;
  private name = "Axiom";
  private dead = false;
  // the story has started (once): it goes on under a new clock when motion is allowed again
  private told = false;
  private copyTimer = 0;
  private readonly copying = turns();
  private readonly picking = turns();
  private readonly decoding = turns();
  // the card's height easing to a newly picked tool's
  private resizing: Animation | undefined;
  // the figure's fetch, stopped with the screen
  private readonly fetching = new AbortController();
  private readonly resize: ResizeObserver;
  private readonly stopMotion: () => void;
  private readonly picks = tools.map((_, i) => () => this.pick(i));
  private readonly hovers = Array.from({ length: rows }, (_, i) => () => this.hover(i));

  constructor(
    private readonly stage: HTMLElement,
    private readonly changed: () => void,
    private readonly narrow: boolean,
    private readonly onWhale: () => void,
  ) {
    this.clock = timeline(stage);
    this.seen = firstSeen(stage, 0.3);
    this.resize = new ResizeObserver(() => this.place());
    this.resize.observe(stage);
    this.stopMotion = onMotion((allowed) => this.motion(allowed));
    document.addEventListener("visibilitychange", this.hidden);
    window.addEventListener("pagehide", this.hidden);
    this.fetchMachine();
  }

  // A copy belongs to the page in view: leaving it (another tab, or the page put away) drops a copy still waiting on the
  // clipboard and the word of the last one, which the reader would otherwise come back to.
  private readonly hidden = () => {
    if (document.visibilityState === "visible" && !this.dead) return;
    this.copying.end();
    window.clearTimeout(this.copyTimer);
    if (this.state.copied) this.set({ copied: "" });
  };

  // ---------------------------------------------------------------- the machine arrives
  private fetchMachine() {
    const fig = this.stage.querySelector<HTMLElement>(".fig");
    if (!fig) return;
    const asked = fetched
      ? Promise.resolve(fetched)
      : fetch(figureUrl, { signal: this.fetching.signal }).then((response) =>
          response.ok ? response.text() : Promise.reject(new Error(String(response.status))),
        );
    asked
      .then((asset) => {
        fetched = asset;
        if (this.dead) return;
        this.machine = new Machine(fig, asset);
        this.wire = new Wire(this.stage, fig, this.narrow);
        this.place();
        if (reducedMotion()) this.wired();
        else this.seen.promise.then(() => this.intro());
      })
      // the card works without the picture: the machine is only the illustration
      .catch(() => {});
  }
  // the wired state: the key in the lock, the lamp on, the display reading the site's name and that it is ready
  private wired() {
    this.machine?.swap("key", 1);
    this.machine?.lamp?.classList.add("on");
    this.machine?.text(displayed(this.name), "READY");
  }
  // the site's name, which the display reads while the machine waits for a call
  rename(name: string) {
    const was = displayed(this.name);
    this.name = name;
    if (this.machine?.display.m?.textContent === was) this.machine.text(displayed(name), "READY");
  }
  private place() {
    this.wire?.draw(this.state.tool);
    this.wire?.note0(this.call()[0], false);
  }
  private call() {
    return CALLS[this.current % CALLS.length] as [number, number, string];
  }

  // The reader changed the motion setting: what was moving stops in its final state (the machine wired and at rest, the
  // card decoded at its height, the plug in), on a clock of the new setting, which the loop runs only while motion is
  // allowed; with motion allowed again the story goes on from there.
  private motion(allowed: boolean) {
    this.clock.end();
    this.clock = timeline(this.stage);
    this.picking.end();
    this.resizing?.finish();
    this.stage.querySelector<SVGElement>(".cpulse")?.style.setProperty("opacity", "0");
    if (this.machine) {
      this.machine.rest();
      this.wired();
    }
    if (this.wire) this.wire.plug = 0;
    this.place();
    this.set({ ct: null, pulse: false, rct: null });
    if (!allowed || !this.machine) return;
    if (this.told) this.loop();
    else this.seen.promise.then(() => this.intro());
  }

  // ---------------------------------------------------------------- motion helpers
  // tween runs step over ms of the clock in force, and says whether it got to the end: false once that clock has ended
  // (the screen has ended, or the motion setting changed), when whatever awaits it stops too, so nothing it would do next
  // lands on a later clock.
  private tween(ms: number, step: (p: number) => void) {
    const clock = this.clock;
    return new Promise<boolean>((done) => {
      const t0 = clock.now();
      clock.each(
        (now) => {
          const p = Math.min(1, (now - t0) / ms);
          step(p);
          if (p >= 1) done(true);
          return p < 1;
        },
        () => done(false),
      );
    });
  }
  private set(next: Partial<State>) {
    Object.assign(this.state, next);
    this.changed();
  }
  // a point of light runs along a path, its head and a short trail, fading in and out
  private async ride(pulse: Element | undefined, path: readonly Point[], ms: number) {
    if (!pulse || path.length < 2) return true;
    const { total, position } = along(path);
    const group = pulse as SVGElement;
    pulse.removeAttribute("transform");
    group.style.opacity = "1";
    const ran = await this.tween(ms, (p) => {
      trailAt(pulse, position, ease(p) * total, 2.6);
      group.style.opacity = String(p > 0.9 ? (1 - p) / 0.1 : Math.min(1, p / 0.06));
    });
    group.style.opacity = "0";
    return ran;
  }
  private async flash(pulse: Element | undefined, point: Point, ms: number) {
    if (!pulse) return true;
    pulse.removeAttribute("transform");
    trailAt(pulse, () => point, 0, 0);
    return this.tween(ms, (p) => ((pulse as SVGElement).style.opacity = String(Math.sin(Math.PI * p))));
  }

  // ---------------------------------------------------------------- the story
  private async intro() {
    // the key goes in and turns; the pilot lamp comes on; then the lines you add are written
    if (this.told) return;
    this.told = true;
    this.place();
    if (!(await this.clock.sleep(500))) return;
    this.wired();
    if (!(await this.clock.sleep(250))) return;
    this.decode();
    if (!(await this.clock.sleep(900))) return;
    this.loop();
  }
  private async loop() {
    for (;;) {
      this.current++;
      if (!(await this.callA(this.call())) || !(await this.clock.sleep(520))) return;
    }
  }
  // a call, start to end: every wait says whether the clock it started on is still the one in force, and the call stops
  // at the first that is not (the motion setting changed, or the screen ended)
  private async callA([vendor, contact, name]: [number, number, string]) {
    const m = this.machine;
    const w = this.wire;
    if (!m || !w) return false;
    const lift = figure.lift[vendor + 1] as number;
    const home = figure.geo.HOME_A;
    const to = figure.geo.angles[contact] as number;
    const pin = m.pin(`${vendor}-${contact}`);
    const steps: (() => Promise<boolean> | boolean)[] = [
      // the call starts at the address line in your config and runs down the cable into the one input
      () => {
        this.set({ pulse: true });
        return this.clock.sleep(240);
      },
      () => {
        this.set({ pulse: false });
        w.draw(this.state.tool);
        return this.rideCable(720);
      },
      () => this.flash(m.pulses[1], at(figure.contact[0] as number[][], 0), 260),
      () => this.climb(lift, vendor),
      // the wiper turns along the vendor's level to the model's contact
      () => this.tween(200 + contact * 55, (p) => m.setWiper(home + (to - home) * ease(p))),
      () => {
        m.setWiper(to);
        pin?.classList.add("on");
        return this.flash(m.pulses[1], at(figure.contact[vendor] as number[][], contact), 420);
      },
      () => {
        m.text(name, VENDORS[vendor]?.toUpperCase() ?? "");
        this.showCard(vendor, contact, name);
        return this.ride(m.pulses[0], [...points(figure.harness), ...points(figure.rack[vendor])], 820);
      },
      () => {
        m.mods[vendor]?.classList.add("on");
        w.note0(vendor, true);
        return this.clock.sleep(1000);
      },
      // release: the wiper turns home, then the shaft drops and settles
      () => {
        w.note0(vendor, false);
        for (const lit of [pin, m.mods[vendor], m.dnames[vendor]]) lit?.classList.remove("on");
        return this.tween(240, (p) => m.setWiper(to + (home - to) * ease(p)));
      },
      () => {
        m.setWiper(home);
        return this.tween(280, (p) => m.setLift(lift * (1 - easeIn(p))));
      },
      () => this.tween(140, (p) => m.setLift(1.4 * Math.sin(Math.PI * p))),
    ];
    for (const step of steps) if (!(await step())) return false;
    m.setLift(0);
    return true;
  }
  // the shaft climbs in one calm movement; the armature ticks once for every level it passes
  private async climb(lift: number, vendor: number) {
    const m = this.machine as Machine;
    let passed = -1;
    const ran = await this.tween(240 + vendor * 70, (p) => {
      const z = lift * easeOutBack(p);
      m.setLift(z);
      const level = figure.lift.findIndex((l, i) => i > 0 && l > z + 0.01) - 2;
      if (level > passed) {
        passed = level;
        m.move("arm", [0, (figure.zv[1] as number) * -1.2]);
        this.clock.sleep(50).then(() => m.move("arm", [0, 0]));
      }
    });
    if (!ran) return false;
    m.setLift(lift);
    m.dnames[vendor]?.classList.add("on");
    return true;
  }
  private async rideCable(ms: number) {
    const path = this.wire?.path;
    const group = this.stage.querySelector<SVGElement>(".cpulse");
    if (!path || !group) return true;
    const total = path.getTotalLength();
    group.style.opacity = "1";
    const position = (s: number): Point => {
      const q = path.getPointAtLength(s);
      return [q.x, q.y];
    };
    const ran = await this.tween(ms, (p) => {
      trailAt(group, position, ease(p) * total, 3.2);
      group.style.opacity = String(p > 0.94 ? (1 - p) / 0.06 : Math.min(1, p / 0.05));
    });
    group.style.opacity = "0";
    return ran;
  }
  // the card names the call: the model's name decodes in, and a DeepSeek one calls the whale
  private showCard(vendor: number, contact: number, name: string) {
    this.stage.querySelector(".rcard")?.classList.add("on");
    this.wire?.note0(vendor, false);
    const t0 = this.clock.now();
    this.clock.each((now) => {
      const t = now - t0;
      this.set({ call: [vendor, contact, name], rct: t < 420 ? t : null });
      if (t < 420) return true;
      if (/^deepseek/i.test(name)) this.onWhale();
      return false;
    });
  }

  // ---------------------------------------------------------------- the tool's card
  // the lines the tool adds are written in; a newer decode (another tool picked) takes over from an older one
  private decode() {
    const mine = this.decoding.start();
    if (reducedMotion()) {
      this.set({ ct: null });
      return;
    }
    const t0 = this.clock.now();
    this.set({ ct: 0 });
    this.clock.each((now) => {
      if (!mine()) return false;
      const t = now - t0;
      this.set({ ct: t < 700 ? t : null });
      return t < 700;
    });
  }
  private async replug(mine: () => boolean) {
    const m = this.machine;
    const w = this.wire;
    if (!m || !w) return;
    const out = figure.plugOut;
    const yv = at([figure.yv], 0);
    const draw = (to: number) => {
      if (!mine()) return;
      w.plug = to;
      m.move("plug", [yv[0] * to, yv[1] * to]);
      w.draw(this.state.tool);
    };
    if (!(await this.tween(200, (p) => draw(out * ease(p))))) return;
    if (!(await this.clock.sleep(80))) return;
    if (!(await this.tween(240, (p) => draw(out * (1 - ease(p)))))) return;
    draw(0);
  }
  private pick(i: number) {
    if (i === this.state.tool) return;
    const mine = this.picking.start();
    // the copy's word was about the tool before
    this.copying.end();
    window.clearTimeout(this.copyTimer);
    const still = reducedMotion();
    const card = this.stage.querySelector<HTMLElement>(".code");
    // the height the card shows now, mid-way through an earlier pick's easing, which a new one starts from
    const before = card?.offsetHeight ?? 0;
    this.resizing?.cancel();
    this.set({ tool: i, ct: still ? null : 0, copied: "" });
    if (!still) this.replug(mine);
    // once the card is drawn for the new tool, its height eases to the new one and the cable follows
    const after = () => {
      if (!mine()) return;
      const now = card?.offsetHeight ?? 0;
      if (card && !still && Math.abs(now - before) > 1) {
        this.resizing = card.animate([{ height: `${before}px` }, { height: `${now}px` }], {
          duration: 250,
          easing: "ease-out",
        });
        this.tween(260, () => mine() && this.wire?.draw(this.state.tool));
      }
      this.decode();
      this.wire?.draw(this.state.tool);
    };
    if (still) setTimeout(after, 0);
    else
      this.clock.each(() => {
        after();
        return false;
      });
  }
  // pointing at the address line lights the one input; pointing at the key line lights the key
  private hover(i: number) {
    const tool = this.state.tool;
    this.machine?.hover("plug", i === addressLine[tool]);
    this.stage.querySelector(".wire")?.classList.toggle("hot", i === addressLine[tool]);
    this.machine?.hover("key", i === keyLine[tool]);
  }
  private readonly leave = () => this.hover(-1);
  // The copy is reported once the clipboard has taken it, if it is still the latest copy of the tool shown and the page
  // has stayed in view: a refusal, or no clipboard, says to select it by hand, on the card where its file's name is. A
  // new copy takes the last one's word away first, so only its own outcome shows.
  private readonly copy = async () => {
    const texts = this.texts;
    if (!texts?.known) return;
    const mine = this.copying.start();
    window.clearTimeout(this.copyTimer);
    if (this.state.copied) this.set({ copied: "" });
    let outcome: "done" | "failed" = "failed";
    try {
      await navigator.clipboard.writeText(copyText(this.state.tool, texts));
      outcome = "done";
    } catch {
      // the reader copies it by hand
    }
    if (!mine()) return;
    this.set({ copied: outcome });
    this.copyTimer = window.setTimeout(() => mine() && this.set({ copied: "" }), outcome === "done" ? 1600 : 4000);
  };

  values(texts: Texts) {
    this.texts = texts;
    const word = texts.word;
    const { tool, ct, copied, pulse, call, rct } = this.state;
    const total = addedLength(tools[tool] as Tool, texts);
    const k = ct === null ? null : Math.floor((ct / 620) * (total + 3));
    const holes = cardHoles(
      tool,
      texts,
      { written: k, tick: Math.floor((ct ?? 0) / 50), narrow: this.narrow },
      pulse,
      rows,
    );
    const copyWord = copyState(texts.known, copied, (tools[tool] as Tool).copy);
    // the button is the board's, not a prop of the region: without an address there is nothing to copy
    this.stage.querySelector<HTMLButtonElement>(".cpy")?.toggleAttribute("disabled", !texts.known);
    const name = call?.[2] ?? "";
    const progress = rct === null ? Number.POSITIVE_INFINITY : Math.floor((rct / 380) * (name.length + 3));
    const done = Math.min(name.length, progress);
    const tumbling = Math.max(0, Math.min(name.length - done, progress + 3 - done));
    const current = tools[tool] as Tool;
    return {
      ...holes,
      ...Object.fromEntries(
        tools.flatMap((_, i) => [
          [`m1_tab${i}`, i === tool ? "on" : ""],
          [`m1_sel${i}`, i === tool],
          [`m1_pick${i}`, this.picks[i]],
        ]),
      ),
      ...Object.fromEntries(this.hovers.map((hover, i) => [`m1_lh${i}`, hover])),
      m1_lhoff: this.leave,
      m1_fig: "A",
      m1_noteCls: "n-card",
      m1_file: copied === "failed" ? word("copyFailed") : textOf(current.file, texts),
      m1_copy: this.copy,
      m1_copied: copied === "done" ? "done" : "",
      m1_copyLabel: word(copyWord),
      m1_live: copied ? word(copyWord) : "",
      m1_rcA: name.slice(0, done),
      m1_rcG: scramble(name, done, tumbling, Math.floor((rct ?? 0) / 50)),
      m1_rcC: name.slice(done + tumbling),
      m1_rcT: textOf(current.name, texts),
      m1_rcV: call ? (VENDORS[call[0]] ?? "") : "",
    };
  }

  destroy() {
    this.dead = true;
    this.seen.cancel();
    this.resize.disconnect();
    this.stopMotion();
    this.fetching.abort();
    this.copying.end();
    this.picking.end();
    this.decoding.end();
    this.resizing?.cancel();
    window.clearTimeout(this.copyTimer);
    document.removeEventListener("visibilitychange", this.hidden);
    window.removeEventListener("pagehide", this.hidden);
    this.clock.end();
  }
}

export const start = (stage: HTMLElement, changed: () => void, narrow: boolean, onWhale: () => void): ConnectRun =>
  new ConnectRun(stage, changed, narrow, onWhale);
