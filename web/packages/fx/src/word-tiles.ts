// Sea-floor wordmark tiles for the tiles departure (trans.js.txt lines 1009–1025, 1100–1108): when leaving from the
// sea floor, the Clawd engine exposes the buried wordmark's cells through frame.__axCast.wordTiles(). Each tile lifts
// out of the sand and flies into a panel cell arriving on the same beat; destroy() calls wm.show() to put them back if
// the departure reverses or is stopped.
import { hash } from "./hash";

export const GLYPHS = ":;t%SX8#@/\\<>=+*";
export const clamp = (v: number, a: number, b: number) => Math.min(Math.max(v, a), b);
export const lerp = (a: number, b: number, t: number) => a + (b - a) * t;

export type RawWordTile = { x: number; y: number; w: number; h: number; m: number; li: number };
export type WordCast = {
  tiles: RawWordTile[];
  tile: string;
  ink: string;
  gf: number;
  hide: () => void;
  show: () => void;
};
type CastRoot = HTMLElement & { __axCast?: { wordTiles?: () => WordCast | null } };

export type WordCell = { x: number; y: number; w: number; h: number; at: number; word?: boolean };
export type FlyingWord = {
  x: number;
  y: number;
  w: number;
  h: number;
  m: number;
  tx: number;
  ty: number;
  tw: number;
  th: number;
  ld: number;
  fl: number;
  arc: number;
  g: string;
};

// A wide screen has large seabed letters: group their tiles into mk x mk blocks so at most ~900 travel.
function coalesce(tiles: RawWordTile[]): RawWordTile[] {
  const mk = Math.max(1, Math.ceil(Math.sqrt(tiles.length / 900)));
  if (mk <= 1) return tiles;
  const merged = new Map<number, RawWordTile>();
  for (const w of tiles) {
    const kx = Math.floor(w.x / (w.w * mk));
    const ky = Math.floor(w.y / (w.h * mk));
    const key = kx * 100_000 + ky;
    const prev = merged.get(key);
    if (prev) {
      prev.m = Math.max(prev.m, w.m);
      prev.x = Math.min(prev.x, w.x);
      prev.y = Math.min(prev.y, w.y);
    } else {
      merged.set(key, { ...w, w: w.w * mk, h: w.h * mk });
    }
  }
  return [...merged.values()];
}

// planWord claims the seabed wordmark tiles (calling wm.hide()) and pairs each with an incoming panel cell.
export function planWord(frame: HTMLElement, cells: WordCell[], ex: number, ey: number) {
  const cast = (frame as CastRoot).__axCast;
  const wm = cast?.wordTiles ? cast.wordTiles() : null;
  if (!wm?.tiles.length || !cells.length) return { word: [] as FlyingWord[], wm: null, landed: 0 };
  wm.hide();
  const sorted = coalesce(wm.tiles)
    .map((w) => ({ ...w, k: Math.hypot(w.x - ex, w.y - ey) }))
    .sort((p, q) => p.k - q.k);
  const n = sorted.length;
  const kmax = sorted[n - 1]?.k || 1;
  const order = cells.map((_, i) => i).sort((p, q) => (cells[p]?.at ?? 0) - (cells[q]?.at ?? 0));
  const used = new Uint8Array(cells.length);
  const word: FlyingWord[] = [];
  let j = 0;
  let landed = 0;
  sorted.forEach((w, i) => {
    const fl = 380 + (w.k / kmax) * 160;
    const ld = 20 + (i / n) * 260 + hash(i, 17) * 50;
    const arr = ld + fl;
    while (j < order.length - 1 && (cells[order[j] as number]?.at ?? 0) < arr - 30) j++;
    let pick = j + Math.floor(hash(i, 19) * Math.min(24, order.length - j));
    while (pick < order.length - 1 && used[order[pick] as number]) pick++;
    const cell = cells[order[pick] as number];
    if (!cell) return;
    used[order[pick] as number] = 1;
    cell.at = Math.min(cell.at, arr - 70);
    cell.word = true;
    landed = Math.max(landed, arr);
    word.push({
      x: w.x,
      y: w.y,
      w: w.w,
      h: w.h,
      m: w.m,
      tx: cell.x,
      ty: cell.y,
      tw: cell.w,
      th: cell.h,
      ld,
      fl,
      arc: 40 + hash(i, 23) * 120,
      g: GLYPHS[Math.floor(hash(i, 29) * GLYPHS.length)] ?? "",
    });
  });
  return { word, wm, landed };
}

// drawWord draws every wordmark tile at clock t: sitting in the letters until ld, then arcing into its panel cell.
export function drawWord(
  x: CanvasRenderingContext2D,
  word: FlyingWord[],
  wm: WordCast | null,
  t: number,
  tileInk: string,
  ease: (u: number) => number,
) {
  if (!word.length || !wm) return;
  x.font = `${wm.gf}px "Axiom Mono", ui-monospace, monospace`;
  x.textAlign = "center";
  x.textBaseline = "middle";
  for (const w of word) {
    const lt = (t - w.ld) / w.fl;
    if (lt >= 1) continue;
    const e = lt <= 0 ? 0 : ease(lt);
    const px = lerp(w.x, w.tx, e);
    const py = lerp(w.y, w.ty, e) - Math.sin(Math.PI * e) * w.arc;
    const ww = lerp(w.w, w.tw, e);
    const hh = lerp(w.h, w.th, e);
    x.globalAlpha = (0.26 + 0.52 * w.m) * (1 - clamp((lt - 0.85) / 0.15, 0, 1));
    x.fillStyle = e > 0.5 ? tileInk : wm.tile;
    x.fillRect(px, py, Math.max(1, ww - 1), Math.max(1, hh - 1));
    if (ww >= 5) {
      x.globalAlpha = (0.5 + 0.5 * w.m) * (1 - clamp(lt / 0.6, 0, 1));
      x.fillStyle = wm.ink;
      x.fillText(w.g, px + ww / 2, py + hh / 2 + 0.5);
    }
  }
}
