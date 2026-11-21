// One-shot sessionStorage markers linking the website and the console's sign-in pages (port design §9):
// - arrival: set when departing to /console/sign-in so its title decodes once (direct visits do not)
// - origin: the departure trigger's viewport rect, so the sign-in page knows where to stream return tiles and that
//   browser Back leads back to the website
// - return: set when returning from a sign-in page to /, so the home page settles cleanly without replaying the tide
const arrivalKey = "axiom:arrival";
const originKey = "axiom:origin";
const returnKey = "axiom:return";
const fresh = 10_000;

export type OriginRect = { x: number; y: number; w: number; h: number };

function setStamp(key: string) {
  try {
    sessionStorage.setItem(key, String(Date.now()));
  } catch {
    // storage refused (private mode, quota): fall back to a direct visit
  }
}

function takeStamp(key: string): boolean {
  try {
    const at = Number(sessionStorage.getItem(key));
    sessionStorage.removeItem(key);
    return at > 0 && Date.now() - at < fresh;
  } catch {
    return false;
  }
}

export const markArrival = () => setStamp(arrivalKey);
export const takeArrival = () => takeStamp(arrivalKey);
export const markReturn = () => setStamp(returnKey);
export const takeReturn = () => takeStamp(returnKey);

export function markOrigin(rect: OriginRect) {
  try {
    sessionStorage.setItem(originKey, JSON.stringify({ at: Date.now(), ...rect }));
  } catch {
    // storage refused: the return transition falls back to the header target
  }
}

export function takeOrigin(): OriginRect | null {
  try {
    const raw = sessionStorage.getItem(originKey);
    sessionStorage.removeItem(originKey);
    if (!raw) return null;
    const data = JSON.parse(raw) as Partial<OriginRect & { at: number }>;
    if (typeof data.at !== "number" || Date.now() - data.at >= fresh) return null;
    const { x, y, w, h } = data;
    return typeof x === "number" && typeof y === "number" && typeof w === "number" && typeof h === "number"
      ? { x, y, w, h }
      : null;
  } catch {
    return null;
  }
}
