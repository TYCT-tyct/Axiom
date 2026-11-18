// hash maps two integers to a number in [0, 1), the same for the same pair: the boards' scripts pick their tumbling
// glyphs and sample figures with it (trans.js, Site.dc.html), so a frame is drawn the same on every run.
export const hash = (a: number, b: number) => {
  let h = Math.imul(a | 0, 374761393) ^ Math.imul(b | 0, 668265263);
  h = Math.imul(h ^ (h >>> 13), 1274126177);
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296;
};
