import type { Schemas } from "@axiom/api";
import { hash, reducedMotion, timeline } from "@axiom/fx";
import { parseMoney } from "@axiom/ui";
import type zh from "../locales/zh";
import { bridge, leadOf, measurer } from "./models-leads";
import { publicModels } from "./public-models";
import { firstSeen, type Script } from "./script";

// The models-and-routes screen's script (Site.dc.html M2) on the site's published models (listPublicModels): the list
// and the panel of the selected model's routes, the pick the gateway would make (the cheapest route), every few seconds
// the next model, and every third turn the picked route dropping out so the pick moves on. The API publishes no
// latency, uptime or usage: the first-token and uptime columns and the week's tokens show the board's kind of figures
// under a 示例数据 label, and the list leaves out its usage trend (port design R14).
// a plural is asked for by its base, routes for routes_one and routes_other
type Word = keyof typeof zh.script.models extends infer K ? (K extends `${infer B}_${"one" | "other"}` ? B : K) : never;
type Price = { amount: string; value: number; currency: string };
type Route = { name: string; input?: Price; output?: Price };
// priced: the site publishes this model's prices (listPublicModels leaves every pricing out when it keeps them to
// members); a published price may still lack an item, which is then unavailable rather than hidden
type Model = {
  name: string;
  author: string;
  context?: number;
  output?: number;
  routes: Route[];
  from?: Price;
  priced: boolean;
  // the currency the published prices are in, whatever they price (tokens, images...)
  currency?: string;
};

const NR = 6;
const NROUTE = 4;
const NT = 24;
const GLYPHS = ":;t%SX8#@/<>=+*";
// The filters' authors, in the board's order: all, Anthropic, OpenAI, Google, and the models made in China.
const DOMESTIC = ["deepseek", "moonshotai", "qwen", "z-ai", "zhipu", "minimax", "baidu", "bytedance", "tencent"];
const FILTERS: ((author: string) => boolean)[] = [
  () => true,
  (a) => a === "anthropic",
  (a) => a === "openai",
  (a) => a === "google",
  (a) => DOMESTIC.includes(a),
];
const glyph = (i: number, tick: number) => GLYPHS[Math.floor(hash(i, tick) * GLYPHS.length)] as string;
// writes txt in like the entrance: settled characters, a few tumbling ones, then blanks
const decode = (txt: string, t: number | null) => {
  if (t == null) return txt;
  const tick = Math.floor(t / 50);
  const n = Math.floor((t / 620) * (txt.length + 3));
  return [...txt].map((c, i) => (i < n ? c : i < n + 3 && c !== " " ? glyph(i, tick) : " ")).join("");
};
// a route's sample first token (0.4–1.7 s) and uptime (98.7–99.99 %), fixed per model and route
const sampleTtft = (i: number, j: number) => `${(0.4 + hash(i, j) * 1.3).toFixed(2)}s`;
const sampleUp = (i: number, j: number) => `${(99.99 - hash(j, i) ** 3 * 1.29).toFixed(2)}%`;
const size = (n?: number) =>
  n === undefined ? "—" : n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : `${Math.round(n / 1e3)}K`;
const hhmm = (d: Date) => `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;

// priceOf reads the price a list shows for item, per million tokens: the server's own display amount, exact.
function priceOf(pricing: Schemas["ModelPricing"] | undefined, item: "input" | "output"): Price | undefined {
  const rate = pricing?.rates.find((r) => r.item === item && r.headline);
  const amount = rate?.display_per === 1_000_000 && rate.display_amount ? parseMoney(rate.display_amount) : null;
  return pricing && rate?.display_amount && amount !== null
    ? { amount, value: Number(rate.display_amount), currency: pricing.currency }
    : undefined;
}

const modelOf = (m: Schemas["PublicModel"]): Model => ({
  name: m.name,
  author: m.author,
  context: m.context_length,
  output: m.max_output,
  routes: (m.endpoints ?? []).map((e) => ({
    name: e.name,
    input: priceOf(e.pricing, "input"),
    output: priceOf(e.pricing, "output"),
  })),
  from: priceOf(m.pricing, "input"),
  priced: m.pricing !== undefined || (m.endpoints ?? []).some((e) => e.pricing !== undefined),
  currency: (m.pricing ?? m.endpoints?.find((e) => e.pricing)?.pricing)?.currency,
});

// the route shown as the gateway's pick: the cheapest input price among those up; without comparable prices (hidden or
// missing) none is named
function pickOf(model: Model, down: number) {
  let best = -1;
  model.routes.forEach((route, j) => {
    const low = model.routes[best]?.input?.value ?? Number.POSITIVE_INFINITY;
    if (j !== down && route.input && route.input.value < low) best = j;
  });
  return best;
}

// The routes the panel's rows show, by index into the model's: the first ones, and the pick in the last row when it
// lies beyond them, so the card's lead always has its row.
function visibleRoutes(model: Model | undefined, pick: number) {
  const first = Array.from({ length: Math.min(NROUTE, model?.routes.length ?? 0) }, (_, j) => j);
  if (pick >= NROUTE) first[NROUTE - 1] = pick;
  return first;
}

type Input = {
  t: (word: Word, options?: Record<string, string | number>) => string;
  money: (price: Price) => string;
  // the name a currency's prices are said in, by its code
  currency: (code: string) => string;
};

// The call for the models, in the shape DataState reads: the page shows the list and the panel once it has succeeded
// with at least one model, and the state of the call in their place until then.
type Query = {
  status: "pending" | "error" | "success";
  data: unknown[] | undefined;
  error: unknown;
  errorUpdatedAt: number;
  refetch: () => void;
};

export type ModelsScript = Script<Input> & { layout: () => void; query: () => Query };

// the list's rows: each model's name, its lowest price and its routes; the rows beyond the list are hidden
function listValues(list: { m: Model; i: number }[], sel: number, input: Input) {
  const { t } = input;
  const v: Record<string, unknown> = {};
  for (let k = 0; k < NR; k++) {
    const x = list[k];
    const p = `m2_r${k}`;
    if (!x) {
      Object.assign(v, {
        [`${p}c`]: "off",
        [`${p}s`]: false,
        [`${p}n`]: "",
        [`${p}p`]: "",
        [`${p}d`]: "",
        [`${p}r`]: "",
      });
      continue;
    }
    const cheapest = x.m.routes.reduce<Price | undefined>(
      (low, r) => (r.input && (!low || r.input.value < low.value) ? r.input : low),
      x.m.from,
    );
    v[`${p}c`] = x.i === sel ? "on" : "";
    v[`${p}s`] = x.i === sel;
    v[`${p}n`] = x.m.name;
    v[`${p}p`] = priceText(x.m, cheapest, input);
    v[`${p}d`] = x.m.author;
    v[`${p}r`] = t("routes", { count: x.m.routes.length });
  }
  return v;
}

// a price as the list writes it: hidden prices are a dash, a published price without this item is unavailable
const priceText = (model: Model, price: Price | undefined, { t, money }: Input) =>
  price ? money(price) : model.priced ? t("unavailable") : "—";

const noRoute = (p: string) => ({
  [`${p}c`]: "off",
  [`${p}n`]: "",
  [`${p}a`]: "",
  [`${p}b`]: "",
  [`${p}w`]: "0%",
  [`${p}t`]: "",
  [`${p}u`]: "",
});

// a route's cells: its prices, a bar against the dearest (top), and the sample first token and uptime; the one down
// shows its timeout, and as the last tick of its strip
function routeCells(
  model: Model,
  r: Route,
  p: string,
  sample: [number, number],
  state: { chosen: boolean; down: boolean; top: number },
  input: Input,
) {
  const share = r.input && state.top > 0 ? Math.round((r.input.value / state.top) * 100) : 0;
  const { t } = input;
  return {
    [`${p}c`]: [state.chosen && "chosen", state.down && "down"].filter(Boolean).join(" "),
    [`${p}n`]: r.name,
    [`${p}a`]: priceText(model, r.input, input),
    [`${p}b`]: priceText(model, r.output, input),
    [`${p}w`]: `${share}%`,
    [`${p}t`]: state.down ? t("timeout") : sampleTtft(...sample),
    [`${p}u`]: sampleUp(...sample),
  };
}

// the panel's rows (visibleRoutes), the rows beyond the model's own hidden
function routeValues(model: Model | undefined, sel: number, down: number, input: Input) {
  const v: Record<string, unknown> = {};
  const pick = model ? pickOf(model, down) : -1;
  const top = Math.max(0, ...(model?.routes ?? []).map((r) => r.input?.value ?? 0));
  const rows = visibleRoutes(model, pick);
  for (let row = 0; row < NROUTE; row++) {
    const j = rows[row] ?? -1;
    const r = model?.routes[j];
    const p = `m2_u${row}`;
    for (let q = 0; q < NT; q++) v[`m2_q${row}_${q}`] = r && j === down && q === NT - 1 ? "x" : "";
    const state = { chosen: j === pick, down: j === down, top };
    Object.assign(v, model && r ? routeCells(model, r, p, [sel, j], state, input) : noRoute(p));
  }
  return v;
}

// the selected model's name, written in like the entrance: settled, tumbling, then still to come
function nameValues(name: string, mt: number | null) {
  const kk = mt == null ? Number.POSITIVE_INFINITY : Math.floor((mt / 380) * (name.length + 3));
  const a = Math.min(name.length, kk);
  const g = Math.max(0, Math.min(name.length - a, kk + 3 - a));
  return {
    m2_pmA: name.slice(0, a),
    m2_pmG: [...name.slice(a, a + g)].map((_, i) => glyph(a + i, Math.floor((mt ?? 0) / 50))).join(""),
    m2_pmC: name.slice(a + g),
  };
}

// the panel's model and the card naming the gateway's pick, or, in the demonstrated failover, the route it would move to
function pickValues(model: Model | undefined, down: number, fail: number, t: Input["t"]) {
  const chosen = model?.routes[model ? pickOf(model, down) : -1];
  const failed = model?.routes[fail];
  const card = !chosen
    ? { m2_pkA: "", m2_pkB: "" }
    : failed
      ? { m2_pkA: t("switched"), m2_pkB: t("failed", { route: failed.name }) }
      : { m2_pkA: t("current"), m2_pkB: t("cheapest") };
  return {
    ...card,
    m2_pDev: model?.author ?? "",
    m2_pCtx: size(model?.context),
    m2_pMax: size(model?.output),
    m2_pickCls: failed ? "fail" : "",
    m2_pkR: chosen?.name ?? "",
    m2_say: model && chosen ? t("say", { model: model.name, route: chosen.name }) : "",
  };
}

// the prices' unit: the site's currency, which the list's prices carry; a site that keeps its prices to members
// publishes none (listPublicModels), and the line says so
function unitOf(models: Model[], { t, currency }: Input) {
  const code = models.find((m) => m.currency)?.currency;
  return code ? t("unit", { unit: currency(code) }) : t("noPrice");
}

type State = {
  models: Model[] | undefined;
  error: { error: unknown; at: number } | undefined;
  at: Date;
  filter: number;
  sel: number;
  down: number;
  fail: number;
  // the stats' and the selected model's entrances, in ms since each began; null once over
  st: number | null;
  mt: number | null;
};
type Part = "list" | "panel" | "routes" | "leads" | "pick";

// The screen's run: the call for the models, the turn to the next model and the failover, and the leads it draws after
// each render. Its handlers are made once, so the region's props keep their identity from one frame to the next.
class ModelsRun implements ModelsScript {
  private readonly clock;
  private readonly still = reducedMotion();
  private readonly seen;
  private readonly resized = new ResizeObserver(() => this.place(false));
  private state: State = {
    models: undefined,
    error: undefined,
    at: new Date(),
    filter: 0,
    sel: 0,
    down: -1,
    fail: -1,
    st: null,
    mt: null,
  };
  private readonly parts: Record<Part, Element | null> = {
    list: null,
    panel: null,
    routes: null,
    leads: null,
    pick: null,
  };
  private dead = false;
  private hold = false;
  private glide = false;
  private leadY: number | undefined;
  private flash = 0;
  private arrive = () => {};
  private readonly refs = Object.fromEntries(
    (["list", "panel", "routes", "leads", "pick"] as const).map((name) => [
      `m2_set${name[0]?.toUpperCase()}${name.slice(1)}`,
      (element: Element | null) => {
        this.parts[name] = element;
      },
    ]),
  );
  private readonly filters = FILTERS.map((_, i) => () => this.pickFilter(i));
  private readonly hovers = Array.from({ length: NR }, (_, k) => () => this.hover(k));
  private readonly leave = () => {
    this.hold = false;
  };

  constructor(
    private readonly stage: HTMLElement,
    private readonly changed: () => void,
    private readonly narrow: boolean,
    private readonly onWhale: () => void,
  ) {
    this.clock = timeline(stage);
    this.resized.observe(stage);
    // the list starts moving once it is seen with models in it, whether they came on the first call or on a retry
    const arrived = new Promise<void>((done) => {
      this.arrive = done;
    });
    this.fetchModels();
    this.seen = firstSeen(stage, 0.3);
    Promise.all([this.seen.promise, arrived]).then(() => this.begin());
  }

  private set(next: Partial<State>) {
    Object.assign(this.state, next);
    this.changed();
  }
  private all() {
    return this.state.models ?? [];
  }
  private shown() {
    return this.all()
      .map((m, i) => ({ m, i }))
      .filter(({ m }) => FILTERS[this.state.filter]?.(m.author))
      .slice(0, NR);
  }

  // the page's one call for the models (public-models.ts), which the footer reads too
  private fetchModels() {
    publicModels().then(
      (data) => {
        if (this.dead) return;
        this.set({ models: data.data.map(modelOf), at: new Date(), error: undefined });
        this.arrive();
      },
      (error: unknown) => {
        if (!this.dead) this.set({ error: { error, at: Date.now() } });
      },
    );
  }
  private readonly refetch = () => {
    this.set({ error: undefined });
    this.fetchModels();
  };

  private begin() {
    if (this.dead || this.still || this.all().length === 0) return;
    this.tween("st", 700);
    this.tween("mt", 420);
    this.glide = true;
    this.changed();
    this.rotate();
  }
  private tween(key: "st" | "mt", ms: number) {
    if (this.still) return;
    const t0 = this.clock.now();
    this.clock.each((now) => {
      const t = now - t0;
      this.set({ [key]: t < ms ? t : null });
      return t < ms && !this.dead;
    });
  }
  private select(i: number) {
    if (i === this.state.sel) return;
    this.glide = true;
    this.set({ sel: i, down: -1, fail: -1 });
    this.tween("mt", 420);
  }
  // the panel follows the filter: its first model, or none when the filter has no models
  private pickFilter(i: number) {
    this.hold = false;
    this.set({ filter: i });
    this.select(this.all().findIndex((m) => FILTERS[i]?.(m.author)));
  }
  // the selected model, only while the filter shows it
  private selected() {
    return this.shown().find((x) => x.i === this.state.sel)?.m;
  }
  private hover(k: number) {
    const x = this.shown()[k];
    if (!x) return;
    this.hold = true;
    if (x.m.author === "deepseek") this.onWhale();
    this.select(x.i);
  }

  private async failover() {
    const model = this.selected();
    const was = model ? pickOf(model, -1) : -1;
    if (!model || was < 0) return;
    this.glide = true;
    this.set({ down: was, fail: was });
    if (!(await this.clock.sleep(2600))) return;
    this.glide = true;
    this.set({ down: -1, fail: -1 });
  }
  private async rotate() {
    for (let n = 1; await this.clock.sleep(5200); ) {
      if (this.hold) continue;
      // every third turn, the chosen route drops out and the gateway moves to the next one
      if (n++ % 3 === 0 && this.canFail()) {
        await this.failover();
        continue;
      }
      const list = this.shown();
      const at = list.findIndex((x) => x.i === this.state.sel);
      const next = list[(at + 1) % list.length];
      if (next) this.select(next.i);
    }
  }

  // a failover can be shown when another priced route would take over from the pick
  private canFail() {
    const model = this.selected();
    return model !== undefined && pickOf(model, pickOf(model, -1)) >= 0;
  }
  // the chosen route's status dot, by the panel row that shows that route
  private chosenDot() {
    const model = this.selected();
    const pick = model ? pickOf(model, this.state.down) : -1;
    const row = visibleRoutes(model, pick).indexOf(pick);
    return pick < 0 ? undefined : this.parts.routes?.children[row]?.querySelector(".dot");
  }
  private selectedRow() {
    return this.narrow ? undefined : [...(this.parts.list?.children ?? [])].find((el) => el.classList.contains("on"));
  }
  // the chosen route's dot, the leads shown only while there is one: none is left pointing at an earlier route
  private showLeads() {
    const dot = this.chosenDot();
    const { leads } = this.parts;
    if (leads instanceof SVGElement) leads.style.visibility = dot ? "visible" : "hidden";
    return dot;
  }
  // the lead from the card to the chosen route, and a hairline bridge from the selected model to the panel
  private place(move: boolean) {
    const { panel, leads, pick: card } = this.parts;
    const dotEl = this.showLeads();
    if (!panel || !leads || !(card instanceof HTMLElement) || !dotEl) return;
    const box = measurer(this.stage);
    const dot = box(dotEl);
    const pnl = box(panel);
    // the card sits under the panel, its lead rises to the chosen route's status dot
    const cardY = pnl.y + pnl.h + (this.narrow ? 24 : 28);
    card.style.top = `${cardY}px`;
    const c = box(card);
    const mid = this.narrow ? pnl.x + pnl.w - 4 : pnl.x + pnl.w + 20;
    const lead = leadOf(leads, [c.x + c.w - 24, cardY], [dot.x + dot.w / 2, dot.y + dot.h / 2], mid);
    if (!lead) return;
    const from = this.leadY ?? lead.to;
    this.leadY = lead.to;
    if (move && !this.still) {
      this.light(lead.lit);
      this.slide(lead.draw, from, lead.to);
    } else lead.draw(lead.to);
    const selected = this.selectedRow();
    bridge(leads, selected && box(selected), pnl);
  }
  // the lead's end slides to the new route's row
  private slide(draw: (y: number) => void, from: number, to: number) {
    const t0 = this.clock.now();
    this.clock.each((now) => {
      const p = Math.min(1, (now - t0) / 250);
      draw(from + (to - from) * (1 - (1 - p) ** 3));
      return p < 1;
    });
  }
  // the lead glows for a moment after it moves
  private light(lit: Element[]) {
    const own = ++this.flash;
    for (const el of lit) el.classList.add("on");
    this.clock.sleep(900).then(() => {
      if (own === this.flash) for (const el of lit) el.classList.remove("on");
    });
  }

  query(): Query {
    const { models, error } = this.state;
    return {
      status: error ? "error" : models ? "success" : "pending",
      data: models,
      error: error?.error,
      errorUpdatedAt: error?.at ?? 0,
      refetch: this.refetch,
    };
  }

  values(input: Input) {
    const { state } = this;
    const all = this.all();
    const known = !state.error && all.length > 0;
    const model = this.selected();
    const v: Record<string, unknown> = {
      ...this.refs,
      m2_rl: this.leave,
      m2_noPick: !model || pickOf(model, state.down) < 0,
      m2_s0: decode(known ? String(all.length) : "—", state.st),
      m2_s1: decode(known ? all.reduce((sum, m) => sum + m.routes.length, 0).toLocaleString("en-US") : "—", state.st),
      m2_s2: decode(input.t("sampleTokens"), state.st),
      m2_upd: known ? hhmm(state.at) : "—",
      ...listValues(this.shown(), state.sel, input),
      ...routeValues(model, state.sel, state.down, input),
      ...nameValues(model?.name ?? (known ? input.t("noneInFilter") : ""), state.mt),
      ...pickValues(model, state.down, state.fail, input.t),
      unit: unitOf(all, input),
    };
    this.filters.forEach((pick, i) => {
      Object.assign(v, {
        [`m2_f${i}`]: i === state.filter ? "on" : "",
        [`m2_fs${i}`]: i === state.filter,
        [`m2_pf${i}`]: pick,
      });
    });
    this.hovers.forEach((hover, k) => {
      v[`m2_rh${k}`] = hover;
    });
    return v;
  }

  // after the screen is drawn: the card and the leads follow the panel's rows
  layout() {
    this.place(this.glide);
    this.glide = false;
  }

  destroy() {
    this.dead = true;
    this.seen.cancel();
    this.resized.disconnect();
    this.clock.end();
  }
}

// onWhale is called when a DeepSeek model is pointed at: the whale's easter egg (Site.logic.html, AxiomWhaleEgg).
export const start = (stage: HTMLElement, changed: () => void, narrow: boolean, onWhale: () => void): ModelsScript =>
  new ModelsRun(stage, changed, narrow, onWhale);
