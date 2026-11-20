import { describe, expect, it, vi } from "vitest";
import { control, director, entry } from "./director";

// The director's rules on a clock the test moves: no wall-clock time passes.
function setup() {
  const clock = { at: 0, hidden: false };
  const states = vi.fn();
  const d = director({ now: () => clock.at, hidden: () => clock.hidden }, states);
  return { clock, states, d };
}

describe("director", () => {
  it.each([
    { name: "before the first 10 s are over", at: entry - 1, hidden: false, touched: Number.NEGATIVE_INFINITY },
    { name: "while the tab is hidden", at: entry + 1, hidden: true, touched: Number.NEGATIVE_INFINITY },
    { name: "within 3 s of a control", at: entry + 1, hidden: false, touched: entry + 1 - control + 1 },
  ])("plays no auto moment $name", ({ at, hidden, touched }) => {
    const { clock, d } = setup();
    const tide = vi.fn();
    d.auto("tide", tide);
    if (touched > 0) {
      clock.at = touched;
      d.touched();
    }
    clock.at = at;
    clock.hidden = hidden;
    d.tick();
    expect(tide).not.toHaveBeenCalled();
    expect(d.state().queued).toEqual(["tide"]);
  });

  it("plays one auto moment per visit, the highest priority first, and drops the rest", () => {
    const { clock, d } = setup();
    const tide = vi.fn();
    const reroute = vi.fn();
    d.auto("tide", tide, 1);
    d.auto("reroute", reroute, 3);
    d.auto("tide", tide, 1);
    clock.at = entry;
    d.tick();
    d.tick();
    clock.at += 3_600_000;
    d.auto("tide", tide, 1);
    d.tick();
    expect(reroute).toHaveBeenCalledOnce();
    expect(tide).not.toHaveBeenCalled();
    expect(d.state()).toEqual({ ran: ["reroute"], earned: [], queued: [] });
  });

  it("plays a moment 3 s after the last touch of a control", () => {
    const { clock, d } = setup();
    const tide = vi.fn();
    clock.at = entry;
    d.touched();
    d.auto("tide", tide);
    clock.at = entry + control - 1;
    d.tick();
    expect(tide).not.toHaveBeenCalled();
    clock.at = entry + control;
    d.tick();
    expect(tide).toHaveBeenCalledOnce();
  });

  it("colours the visit once, when the first 10 s are over, without spending the budget", () => {
    const { clock, states, d } = setup();
    clock.at = entry - 1;
    d.tick();
    expect(states).not.toHaveBeenCalled();
    clock.at = entry;
    d.tick();
    d.tick();
    expect(states).toHaveBeenCalledOnce();
    const tide = vi.fn();
    d.auto("tide", tide);
    d.tick();
    expect(tide).toHaveBeenCalledOnce();
  });

  it("plays each earned moment once per visit, at once and whatever the guards, until it is allowed again", () => {
    const { d } = setup();
    const feed = vi.fn();
    expect(d.earn("feed", feed)).toBe(true);
    expect(d.earn("feed", feed)).toBe(false);
    expect(d.earn("whirl", () => {})).toBe(true);
    expect(feed).toHaveBeenCalledOnce();
    d.again("feed");
    expect(d.earn("feed", feed)).toBe(true);
    expect(d.state()).toMatchObject({ ran: [], earned: ["whirl", "feed"] });
  });
});
