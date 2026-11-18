import { ApiError, type Schemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { filtersUnsupported, withoutFellBack } from "../calls";
import {
  callMoneyText,
  callStatusKind,
  cleanSearchFilters,
  computeTimeline,
  dateRangeForPreset,
  detectRangePreset,
  formatClock,
  formatDayHeading,
  formatSeconds,
  groupCallsByDay,
  hasActiveCallFilters,
  isExportLimitError,
  summarizeAggregates,
  usageNumber,
} from "./calls-data";
import { buildChargeCells, buildDetailSteps } from "./calls-detail-rows";
import { buildCallListRows, buildSessionRows } from "./calls-rows";
import { getLastCallsSearch, saveCallsScroll, saveLastCallsSearch, takeCallsScroll } from "./calls-scroll";

const fmtMoney = (m: { amount?: string | number; currency?: string }) =>
  `${m.currency ?? "CNY"} ${Number(m.amount ?? 0) / 1e9}`;

const baseCall: Schemas["Call"] = {
  id: "00000000-0000-4000-8000-000000000001",
  at: "2026-10-14T10:24:28Z",
  workspace_id: "00000000-0000-4000-8000-000000000010",
  key_id: "00000000-0000-4000-8000-000000000020",
  ip: "127.0.0.1",
  client_app: "claude-code",
  session_id: "sess_1",
  subagent_id: "",
  request_class: "chat",
  model: "openai/gpt-5",
  usage: { input: 600, output: 200 },
  service_tier: "standard",
  price: { amount: "21900000", currency: "CNY" },
  free: false,
  latency_ms: 2410,
  first_token_ms: 550,
  finish_reason: "stop",
  error_kind: "",
};

describe("calls filters and helpers", () => {
  it("strips fell_back when preparing aggregate or export queries", () => {
    expect(withoutFellBack({ model: "openai/gpt-5", fell_back: true, status: "error" })).toEqual({
      model: "openai/gpt-5",
      status: "error",
    });
  });

  it("flags fell_back unsupported when the server omits fell_back_filter or returns single-attempt rows", () => {
    expect(
      filtersUnsupported({ fell_back: true }, [{ data: [{ ...baseCall, attempt_count: 1 }], next_cursor: null }]),
    ).toBe(true);
    expect(
      filtersUnsupported({ fell_back: true }, [
        { fell_back_filter: true, data: [{ ...baseCall, attempt_count: 2 }], next_cursor: null },
      ]),
    ).toBe(false);
  });

  it("maps range presets and round-trips detectRangePreset", () => {
    const now = new Date(2026, 9, 24, 15, 30, 0);
    for (const preset of ["today", "d7", "d30", "month"] as const) {
      const range = dateRangeForPreset(preset, now);
      expect(detectRangePreset(range.since, range.until, now)).toBe(preset);
    }
    expect(detectRangePreset(undefined, undefined, now)).toBe("all");
    expect(detectRangePreset("2026-10-01T00:00:00Z", "2026-10-10T00:00:00Z", now)).toBe("custom");
  });

  it("classifies status kinds including #756 fallbacks and failures", () => {
    expect(callStatusKind(baseCall)).toBe("ok");
    expect(callStatusKind({ ...baseCall, attempt_count: 2 })).toBe("retry");
    expect(callStatusKind({ ...baseCall, finish_reason: "cancelled" })).toBe("cancelled");
    expect(callStatusKind({ ...baseCall, error_kind: "upstream_error", attempt_count: 2 })).toBe("fail");
  });

  it("detects export 400 limit errors", () => {
    expect(isExportLimitError(new ApiError(400, "invalid_request"))).toBe(true);
    expect(isExportLimitError(new ApiError(500, "internal_error"))).toBe(false);
    expect(isExportLimitError(new Error("network"))).toBe(false);
  });

  it("computes attempt timeline percentages and TPS", () => {
    const tl = computeTimeline({
      ...baseCall,
      latency_ms: 4000,
      first_token_ms: 1500,
      attempt_count: 2,
      attempts: [
        {
          sequence: 1,
          endpoint_code: "ep-a",
          started_at: "2026-10-14T10:24:24.000Z",
          finished_at: "2026-10-14T10:24:25.000Z",
          result: "failed",
        },
        {
          sequence: 2,
          endpoint_code: "ep-b",
          started_at: "2026-10-14T10:24:25.000Z",
          finished_at: "2026-10-14T10:24:28.000Z",
          result: "committed",
        },
      ],
    });
    expect(tl.scaleSec).toBe(10);
    expect(tl.failPct).toBeCloseTo(10, 4);
    expect(tl.waitPct).toBeCloseTo(5, 4);
    expect(tl.genPct).toBeCloseTo(25, 4);
    expect(tl.tps).toBe(80);
    expect(tl.attempts.map((a) => a.sequence)).toEqual([1, 2]);
  });

  it("groups calls by day, formats values, and summarizes aggregates", () => {
    const day = formatDayHeading(
      "2026-10-24",
      { today: "今天", yesterday: "昨天" },
      "zh-CN",
      new Date(2026, 9, 24, 12),
    );
    expect(day.primary).toBe("今天");
    expect(formatSeconds(550, "s", "—")).toBe("0.55s");
    expect(formatSeconds(14_600, "s", "—")).toBe("14.6s");
    expect(formatSeconds(undefined, "s", "—")).toBe("—");
    expect(usageNumber(baseCall.usage, "input", "input_tokens")).toBe(600);
    expect(callMoneyText({ ...baseCall, free: true }, "免单", fmtMoney)).toContain("免单");
    expect(formatClock(baseCall.at)).toMatch(/^\d{2}:\d{2}:\d{2}$/);
    expect(groupCallsByDay([baseCall, { ...baseCall, id: "2" }])).toHaveLength(1);
    const agg = summarizeAggregates(
      {
        data: [
          { bucket: "2026-10-14", calls: "3", price: { amount: "1000000000", currency: "CNY" } },
          { bucket: "2026-10-15", calls: "2", price: { amount: "500000000", currency: "CNY" } },
        ],
      },
      fmtMoney,
    );
    expect(agg.totalCalls).toBe(5n);
    expect(agg.costText).toBe("CNY 1.5");
    expect(cleanSearchFilters({ model: " gpt-5 ", group_by: "day" })).toEqual({
      since: undefined,
      until: undefined,
      model: "gpt-5",
      client_app: undefined,
      key_id: undefined,
      workspace_id: undefined,
      status: undefined,
      fell_back: undefined,
      request_id: undefined,
      group_by: undefined,
      tab: undefined,
      call: undefined,
    });
    expect(hasActiveCallFilters({ fell_back: true })).toBe(true);
    expect(hasActiveCallFilters({})).toBe(false);
    saveCallsScroll("?model=gpt-5", 420);
    expect(takeCallsScroll("?model=other")).toBeNull();
    expect(takeCallsScroll("?model=gpt-5")).toBe(420);
    expect(takeCallsScroll("?model=gpt-5")).toBeNull();
    saveLastCallsSearch({ model: "gpt-5", fell_back: true });
    expect(getLastCallsSearch()).toEqual({ model: "gpt-5", fell_back: true });
  });

  it("builds list, attempt, charge, and session board rows", () => {
    const t = ((k: string) => k) as unknown as Parameters<typeof buildCallListRows>[5];
    const fmt = {
      money: (m: { amount: string; currency: string }) => `$${m.amount}`,
      number: (n: number | bigint) => String(n),
      dateTime: (d: Date) => d.toISOString(),
    };
    const sample: Schemas["Call"] = {
      id: "00000000-0000-4000-8000-000000000001",
      at: "2026-06-01T08:00:00Z",
      key_id: "key_1",
      workspace_id: "ws_1",
      model: "openai/gpt-5",
      request_class: "standard",
      service_tier: "default",
      finish_reason: "stop",
      error_kind: "",
      latency_ms: 800,
      first_token_ms: 200,
      usage: { input_tokens: 120, output_tokens: 30 },
      price: { amount: "1000000", currency: "USD" },
      free: false,
      cache_hit: true,
      attempt_count: 2,
      ip: "127.0.0.1",
      session_id: "sess_1",
      subagent_id: "",
      client_app: "claude-code",
      route: {
        preset: { id: "preset_1", version: 2 },
        selected_endpoint: "ep-1",
        fastest_unmeasured: true,
        excluded_reasons: { rate_limit: 1 },
      },
      attempts: [
        {
          sequence: 1,
          endpoint_code: "ep-0",
          source_class: "relay",
          started_at: "2026-06-01T08:00:00Z",
          finished_at: "2026-06-01T08:00:00.150Z",
          first_token_ms: 0,
          result: "failed",
          error_kind: "rate_limit",
          charged: false,
        },
        {
          sequence: 2,
          endpoint_code: "ep-1",
          source_class: "official",
          started_at: "2026-06-01T08:00:00.150Z",
          finished_at: "2026-06-01T08:00:00.800Z",
          first_token_ms: 200,
          result: "committed",
          charged: true,
        },
      ],
      charge_details: {
        currency: "USD",
        subtotal: "0.001",
        rounding: "0",
        charged: "0.001",
        items: [{ item: "input", quantity: 120, unit: "token", unit_price: "0.00001", amount: "0.001" }],
      },
    };
    const rows = buildCallListRows([sample], sample.id, new Map([["key_1", "Prod"]]), fmt, "zh-CN", t);
    expect(rows.some((r) => r.kind === "1" && r.f2 === sample.id && r.f0 === "lgr sel")).toBe(true);
    expect(buildDetailSteps(sample, fmt, t).length).toBeGreaterThanOrEqual(2);
    expect(buildChargeCells(sample, fmt, t).length).toBeGreaterThan(2);
    expect(
      buildSessionRows(
        [
          {
            session_id: "sess_1",
            client_app: "claude-code",
            main_model: "openai/gpt-5",
            first_at: "2026-06-01T07:00:00Z",
            last_at: "2026-06-01T08:00:00Z",
            calls: "2",
            price: { amount: "2000000", currency: "USD" },
          },
        ],
        fmt,
        t,
      ).rows0,
    ).toHaveLength(1);
  });
});
