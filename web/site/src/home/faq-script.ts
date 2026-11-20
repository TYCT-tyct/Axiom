import { timeline } from "@axiom/fx";
import { type Script, tabKey, tabStop, turns } from "./script";

// The questions' script (Site.dc.html M5): one question is open, and in the wide layout a cable patches from its port to
// the card, with a pulse down it; the first answer's diagram sends its request to one vendor after another. In the
// narrow layout the questions are an accordion, where a second press closes the open one. A patch is the choice's that
// started it: a newer choice or the screen ending ends it.
const questions = 8;
const ROW = 48;
// the cable's route: from a port at PX, through the bend at XM, to the card's socket at (SX, SY)
const PX = 502;
const XM = 557;
const SX = 612;
const SY = 145;
const vendors = 4;

// an orthogonal trace with rounded corners, from a question's port to the card's socket
const trace = (y: number) => {
  const dy = SY - y;
  if (Math.abs(dy) < 1) return `M${PX + 4} ${y} H${SX - 4}`;
  const s = Math.sign(dy);
  const r = Math.min(10, Math.abs(dy) / 2);
  return `M${PX + 4} ${y} H${XM - r} Q${XM} ${y} ${XM} ${y + s * r} V${SY - s * r} Q${XM} ${SY} ${XM + r} ${SY} H${SX - 4}`;
};
const easeOut = (p: number) => 1 - (1 - p) ** 3;
const easeInOut = (p: number) => (p < 0.5 ? 2 * p * p : 1 - (-2 * p + 2) ** 2 / 2);

// What the cable shows before the script has started: patched to the first question
export const resting = { m5_pillY: 0, m5_trD: trace(ROW / 2), m5_ringX: PX, m5_ringY: ROW / 2 };

const indices = Array.from({ length: questions }, (_, i) => i);

class FaqRun implements Script<object> {
  private readonly clock;
  private open = 0;
  private vendor = 0;
  // where the cable is: the row it is patched to, in pixels
  private cy = ROW / 2;
  private readonly patching = turns();
  private readonly pick = indices.map((i) => () => this.choose(i));
  private readonly tabKey = tabKey(
    questions,
    () => Math.max(0, this.open),
    (i) => this.choose(i),
  );

  constructor(
    private readonly stage: HTMLElement,
    private readonly changed: () => void,
    private readonly narrow: boolean,
  ) {
    this.clock = timeline(stage);
    this.cycle();
  }

  // the first answer's diagram: the request goes out to one vendor after another while that answer is open
  private async cycle() {
    while (await this.clock.sleep(1400)) {
      if (this.open !== 0) continue;
      this.vendor = (this.vendor + 1) % vendors;
      this.changed();
    }
  }

  // the wide layout always keeps one answer up; in the accordion a second press closes it
  private choose(i: number) {
    const open = this.narrow && this.open === i ? -1 : i;
    if (open === this.open) return;
    this.open = open;
    this.changed();
    if (!this.narrow) this.repatch(open);
  }

  // the cable slides to the new port, then a pulse runs down it to the card
  private repatch(i: number) {
    const to = i * ROW + ROW / 2;
    const from = this.cy;
    const mine = this.patching.start();
    const t0 = this.clock.now();
    this.clock.each((now) => {
      if (!mine()) return false;
      const p = Math.min(1, (now - t0) / 250);
      this.moveTo(from + (to - from) * easeOut(p));
      return p < 1;
    });
    const pulse = this.stage.querySelector(".patch .pu");
    this.clock.each((now) => {
      if (!mine()) return false;
      const p = Math.max(0, Math.min(1, (now - t0 - 120) / 480));
      pulse?.setAttribute("stroke-dashoffset", String(16 - 116 * easeInOut(p)));
      return p < 1;
    });
  }

  private moveTo(y: number) {
    this.cy = y;
    this.changed();
  }

  values() {
    const open = this.open;
    return {
      ...Object.fromEntries(indices.map((i) => [`m5_t${i}`, this.pick[i]])),
      ...Object.fromEntries(indices.map((i) => [`m5_o${i}`, open === i ? "on" : ""])),
      ...Object.fromEntries(indices.map((i) => [`m5_x${i}`, open === i])),
      ...Object.fromEntries(indices.map((i) => [`m5_ti${i}`, tabStop(i, Math.max(0, open))])),
      m5_tabKey: this.tabKey,
      // a closed answer of the accordion is out of what is read, by the same state as the button's aria-expanded
      ...Object.fromEntries(indices.map((i) => [`m5_h${i}`, open !== i])),
      ...Object.fromEntries(Array.from({ length: vendors }, (_, i) => [`m5_v${i}`, this.vendor === i ? "on" : ""])),
      m5_pillY: Math.max(0, open) * ROW,
      m5_trD: trace(this.cy),
      m5_ringX: PX,
      m5_ringY: this.cy,
    };
  }

  destroy() {
    this.patching.end();
    this.clock.end();
  }
}

export const start = (stage: HTMLElement, changed: () => void, narrow: boolean): Script<object> =>
  new FaqRun(stage, changed, narrow);
