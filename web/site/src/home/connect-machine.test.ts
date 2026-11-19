// @vitest-environment jsdom
import { expect, it } from "vitest";
import asset from "./connect-figure.svg?raw";
import { Machine } from "./connect-machine";

// Moving the shaft or turning the wiper redraws them every frame: the nodes stay, and only their geometry changes.
it.each([
  ["mid", (m: Machine, i: number) => m.setLift(i * 4)],
  ["wiper", (m: Machine, i: number) => m.setWiper(i * 10)],
])("redraws the %s in place", (part, move) => {
  const fig = document.createElement("div");
  const machine = new Machine(fig, asset);
  move(machine, 1);
  const group = fig.querySelector(`[data-v="${part}"]`) as Element;
  const before = [...group.querySelectorAll("*")];
  const shapes = before.map((element) => element.outerHTML);
  move(machine, 2);
  const after = [...group.querySelectorAll("*")];
  expect(after.length).toBeGreaterThan(0);
  expect(after.every((element, i) => element === before[i])).toBe(true);
  expect(after.map((element) => element.outerHTML)).not.toEqual(shapes);
});
