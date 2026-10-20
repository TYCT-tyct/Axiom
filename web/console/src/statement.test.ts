import { describe, expect, it } from "vitest";
import { raised, statementFile, statementName } from "./statement";

describe("the statement's file", () => {
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
    { amount: "0", want: true },
    { amount: "-1000", want: false },
  ])("reads $amount as a balance that rose: $want", ({ amount, want }) => {
    expect(raised(amount)).toBe(want);
  });
});
