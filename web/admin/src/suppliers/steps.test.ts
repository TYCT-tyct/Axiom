import { describe, expect, it } from "vitest";
import { type StepInput, steps } from "./steps";

function input(overrides: Partial<StepInput> = {}): StepInput {
  return {
    status: "active",
    groups: [{ id: "g1", present: true, selected: true, key_state: "ready" }],
    credentials: [],
    hasUnitValue: true,
    ...overrides,
  };
}

const done = (all: ReturnType<typeof steps>, key: string) => all.find((step) => step.key === key)?.done;

describe("steps", () => {
  it("is complete when every listed group has a key or needs none and the unit is valued", () => {
    const all = steps(
      input({
        groups: [
          { id: "g1", present: true, selected: true, key_state: "ready" },
          { id: "g2", present: true, selected: false, key_state: "unavailable" },
          { id: "g3", present: false, selected: true, key_state: "pending" },
        ],
      }),
    );
    expect(all.every((step) => step.done)).toBe(true);
  });

  it("waits for a key that is still being made", () => {
    const all = steps(
      input({
        groups: [
          { id: "g1", present: true, selected: true, key_state: "ready" },
          { id: "g2", present: true, selected: true, key_state: "pending" },
        ],
      }),
    );
    expect(done(all, "keys")).toBe(false);
    expect(all.find((step) => step.key === "keys")?.failed).toBe(0);
  });

  it("counts failed keys and keeps the key step open", () => {
    const all = steps(
      input({
        groups: [
          { id: "g1", present: true, selected: true, key_state: "failed" },
          { id: "g2", present: true, selected: true, key_state: "failed" },
        ],
      }),
    );
    expect(done(all, "keys")).toBe(false);
    expect(all.find((step) => step.key === "keys")?.failed).toBe(2);
  });

  it("reads an older server whose groups all have bound keys", () => {
    const all = steps(
      input({
        groups: [
          { id: "g1", present: true, selected: true },
          { id: "g2", present: true, selected: true },
        ],
        credentials: [
          { group_id: "g1", status: "active" },
          { group_id: "g2", status: "disabled" },
        ],
      }),
    );
    expect(done(all, "keys")).toBe(true);
  });

  it("reads an older server by the bound keys it has", () => {
    const all = steps(
      input({
        groups: [
          { id: "g1", present: true, selected: true },
          { id: "g2", present: true, selected: true },
          { id: "g3", present: true, selected: false },
        ],
        credentials: [{ group_id: "g1", status: "active" }],
      }),
    );
    expect(done(all, "keys")).toBe(false);
    expect(all.find((step) => step.key === "keys")?.failed).toBe(0);
  });

  it("does not count groups the supplier no longer lists", () => {
    const all = steps(input({ groups: [{ id: "g1", present: false, selected: true, key_state: "unavailable" }] }));
    expect(done(all, "groups")).toBe(false);
  });

  it("waits for groups and the unit value", () => {
    const all = steps(input({ groups: [], hasUnitValue: false }));
    expect(done(all, "groups")).toBe(false);
    expect(done(all, "keys")).toBe(false);
    expect(done(all, "unit")).toBe(false);
  });
});
