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

  it("shows a key failure before the group is selected", () => {
    const all = steps(
      input({
        groups: [{ id: "g1", present: true, selected: false, key_state: "failed" }],
      }),
    );
    expect(done(all, "keys")).toBe(false);
    expect(all.find((step) => step.key === "keys")?.failed).toBe(1);
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

  it("names a relay's steps by what it needs: logged in, groups, keys and the unit", () => {
    const all = steps(input({ status: "needs_attention" }));
    expect(all.map((step) => step.key)).toEqual(["connected", "login", "groups", "keys", "unit"]);
    expect(done(all, "connected")).toBe(true);
    expect(done(all, "login")).toBe(false);
  });

  it.each([
    ["direct", false, ["connected", "pasted", "line", "models", "cost"]],
    ["cloud", false, ["credential", "regions", "line", "models", "cost"]],
  ] as const)("names a %s supplier's steps by its way in", (mode, regionless, keys) => {
    const all = steps(input({ mode, regionless, endpoints: [] }));
    expect(all.map((step) => step.key)).toEqual(keys);
  });

  it("counts a direct API's refused keys and waits for a cost before it is done", () => {
    const all = steps(
      input({
        mode: "direct",
        credentials: [
          { group_id: "g1", status: "active" },
          { group_id: "g1", status: "rejected" },
        ],
        endpoints: [{ cost_state: "unknown" }],
      }),
    );
    expect(all.find((step) => step.key === "pasted")).toEqual({ key: "pasted", done: true, failed: 1 });
    expect(done(all, "models")).toBe(true);
    expect(done(all, "cost")).toBe(false);
  });

  it("takes a cloud credential's regions, or its one credential where the platform has no regions", () => {
    const live = [{ group_id: "g1", status: "active" as const }];
    expect(done(steps(input({ mode: "cloud", credentials: live })), "regions")).toBe(false);
    expect(done(steps(input({ mode: "cloud", credentials: live, regionless: true })), "regions")).toBe(true);
    expect(
      done(steps(input({ mode: "cloud", credentials: [{ ...live[0], region: "us-east5" } as never] })), "regions"),
    ).toBe(true);
  });
});
