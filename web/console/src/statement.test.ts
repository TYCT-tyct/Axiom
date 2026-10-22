import { afterEach, describe, expect, it, vi } from "vitest";
import { raised, statementCsv, statementFile, statementName } from "./statement";

afterEach(() => vi.unstubAllGlobals());

describe("the statement's file", () => {
  it("reads a successful CSV response as text", async () => {
    const csv = "at,kind,amount\n2026-10-21T00:00:00Z,freeze,0.000000000\n";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(csv, { headers: { "Content-Type": "text/csv" } })));
    expect(await statementCsv()).toBe(csv);
  });
  it("is named for the day it was taken, in UTC", () => {
    expect(statementName(new Date("2026-10-20T23:30:00Z"))).toBe("statement-2026-10-20.csv");
  });

  it("carries the server's rows unchanged, as a CSV the browser saves", async () => {
    const csv =
      "at,kind,amount,balance_after,currency,reference\n2026-10-20T09:00:00Z,spend,-0.250000000,1.750000000,USD,x\n";
    const file = statementFile(csv, new Date("2026-10-21T01:00:00Z"));
    expect(file.name).toBe("statement-2026-10-21.csv");
    expect(file.type).toBe("text/csv");
    expect(await file.text()).toBe(csv);
  });
});

describe("raised", () => {
  it.each([
    { amount: "1500000000", want: true },
    { amount: "0", want: false },
    { amount: "-1000", want: false },
  ])("reads $amount as a balance that rose: $want", ({ amount, want }) => {
    expect(raised(amount)).toBe(want);
  });
});
