// @vitest-environment jsdom
import "@axiom/boards/engines/login-ui.js";
import { afterEach, describe, expect, it, vi } from "vitest";

type Tile = { x: number; y: number; tone: number; lam: number; a: number; mat: number; o: number };
type Geometry = { cx: number; cy: number; S: number; t: number; W?: number; H?: number };
type ShapeName = "start" | "code" | "key" | "enter" | "wechat" | "connect";

type MorphInstance = {
  A: ShapeName;
  B: ShapeName;
  mAt: number;
  th: number;
  ph: number;
  size: number;
  bad: number;
  pulse: number;
  kick: number;
  glint: number;
  mix(now: number): number;
  solid(g: Geometry, now: number): Tile[];
  solidRef(g: Geometry, now: number): Tile[];
  destroy(): void;
};

type MorphCtor = {
  new (root: HTMLElement, lg: HTMLElement, eng: { renderer: object }): MorphInstance;
  PAR: { open: number; mail: number; n: number };
};

type Case = {
  name: string;
  A: ShapeName;
  B: ShapeName;
  m: number;
  mail: number;
  open: number;
  n: number;
  th: number;
  ph: number;
  g: Geometry;
  now: number;
  bad?: number;
  glint?: number;
  pulse?: number;
  kick?: number;
  size?: number;
  expectEdge?: boolean;
  expectBoil?: boolean;
};

const desktop: Geometry = { cx: 403, cy: 378, S: 225, t: 5 };
const phone: Geometry = { cx: 295, cy: 124, S: 62.6, t: 3 };
// io3(u) = 0.25 at u = cbrt(1/16) and 0.75 at u = 1 - cbrt(1/16).
const uQuarter = Math.cbrt(1 / 16);
const mixOffset = (m: number) => {
  if (m <= 0) return 0;
  if (m >= 1) return 1000;
  if (m === 0.25) return uQuarter * 1000;
  if (m === 0.75) return (1 - uQuarter) * 1000;
  return 500;
};

const ctxStub = () =>
  ({
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    translate: vi.fn(),
    scale: vi.fn(),
    fillStyle: "#000000",
    globalAlpha: 1,
  }) as unknown as CanvasRenderingContext2D;

function buildMorph(): MorphInstance {
  const root = document.createElement("div");
  const lg = document.createElement("div");
  const sea = document.createElement("div");
  sea.className = "lg-sea";
  lg.append(sea);
  root.append(lg);
  document.body.append(root);
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(
    ctxStub as unknown as typeof HTMLCanvasElement.prototype.getContext,
  );
  const Ctor = window.AxiomLoginMorph as unknown as MorphCtor;
  return new Ctor(root, lg, { renderer: {} });
}

const base = (overrides: Partial<Case> & Pick<Case, "name" | "A">): Case => ({
  B: overrides.A,
  m: 1,
  mail: 0,
  open: 0,
  n: 0,
  th: 0,
  ph: 0.3,
  g: desktop,
  now: 5000,
  ...overrides,
});

const singleShapes: Case[] = [
  base({ name: "start idle (mail=0, desktop, th=0)", A: "start", expectEdge: true }),
  base({ name: "start mail=0.5 rotated", A: "start", mail: 0.5, th: 0.65, ph: 0.45 }),
  base({ name: "start mail=1 phone", A: "start", mail: 1, th: -0.9, ph: -0.2, g: phone, expectEdge: true }),
  base({ name: "code closed (open=0, n=3)", A: "code", open: 0, n: 3, th: 0.4, ph: 0.3 }),
  base({ name: "code open (open=1, n=6, phone)", A: "code", open: 1, n: 6, th: -0.7, ph: 0.55, g: phone }),
  base({ name: "key rotated (axis-aligned kx=0, ky=0)", A: "key", th: 0, ph: 0 }),
  base({ name: "key side-on (kz=0)", A: "key", th: Math.PI / 2, ph: 0, g: phone }),
  base({ name: "enter desktop", A: "enter", th: -0.5, ph: 0.25 }),
  base({ name: "wechat desktop", A: "wechat", th: 0.8, ph: 0.35 }),
  base({ name: "connect desktop (back-facing kz<0)", A: "connect", th: Math.PI, ph: 0.3 }),
];

const morphPairs: [ShapeName, ShapeName][] = [
  ["start", "code"],
  ["code", "key"],
  ["key", "enter"],
  ["enter", "start"],
];

const morphCases: Case[] = [
  ...morphPairs.flatMap(([A, B], idx) =>
    [0, 0.25, 0.5, 0.75, 1].map((m) =>
      base({
        name: `morph ${A}->${B} m=${m}`,
        A,
        B,
        m,
        mail: idx % 2 === 0 ? 0.5 : 0,
        open: idx % 2,
        n: 4,
        th: -0.4 + idx * 0.35,
        ph: 0.15 + idx * 0.12,
        g: m === 0.25 || m === 0.75 ? phone : desktop,
        expectBoil: m > 0 && m < 1,
      }),
    ),
  ),
  base({ name: "morph wechat->connect m=0.5", A: "wechat", B: "connect", m: 0.5, th: 0.5, ph: 0.2, expectBoil: true }),
];

const effectCases: Case[] = [
  base({ name: "shake (bad recent)", A: "start", bad: 4880, th: 0.2, ph: 0.3 }),
  base({ name: "glint sweep", A: "code", open: 1, n: 6, glint: 4700, th: -0.3, ph: 0.25 }),
  base({ name: "pulse growth", A: "key", pulse: 4950, th: 0.5, ph: 0.4 }),
  base({ name: "copy kick spin", A: "enter", kick: 4700, th: 0.1, ph: 0.3 }),
  base({
    name: "combined shake + glint + pulse + kick during morph (phone)",
    A: "start",
    B: "code",
    m: 0.5,
    mail: 1,
    open: 1,
    n: 5,
    bad: 4900,
    glint: 4650,
    pulse: 4960,
    kick: 4720,
    size: 0.85,
    g: phone,
    expectEdge: true,
    expectBoil: true,
  }),
];

const cases: Case[] = [...singleShapes, ...morphCases, ...effectCases];

function applyCase(morph: MorphInstance, c: Case) {
  const Ctor = window.AxiomLoginMorph as unknown as MorphCtor;
  Ctor.PAR.mail = c.mail;
  Ctor.PAR.open = c.open;
  Ctor.PAR.n = c.n;
  morph.A = c.A;
  morph.B = c.B;
  morph.mAt = c.now - mixOffset(c.m);
  morph.th = c.th;
  morph.ph = c.ph;
  morph.size = c.size ?? 1;
  morph.bad = c.bad ?? -1e9;
  morph.glint = c.glint ?? -1e9;
  morph.pulse = c.pulse ?? -1e9;
  morph.kick = c.kick ?? -1e9;
}

describe("Morph.solid matches solidRef", () => {
  let morph: MorphInstance | undefined;

  afterEach(() => {
    morph?.destroy();
    morph = undefined;
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it.each(cases)("$name", (c) => {
    morph = buildMorph();
    expect(morph.solid).not.toBe(morph.solidRef);
    applyCase(morph, c);
    expect(morph.mix(c.now)).toBeCloseTo(c.m, 12);
    const actual = morph.solid(c.g, c.now);
    const expected = morph.solidRef(c.g, c.now);
    expect(actual).toEqual(expected);
    expect(actual.length).toBeGreaterThan(0);
    // Edge-AA and morph-boil tiles rely on OBB padding; removing the pad from the cull fails these cases.
    if (c.expectEdge) expect(actual.some((tile) => tile.a < 1 && tile.o > 0)).toBe(true);
    if (c.expectBoil) expect(actual.some((tile) => tile.a === 0.7 && tile.o === 0)).toBe(true);
  });
});
