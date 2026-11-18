import { ApiError, consoleApi, type Schemas } from "@axiom/api";
import { describe, expect, it, vi } from "vitest";
import { filtersUnsupported, withoutFellBack } from "../calls";
import { callSessionsQuery } from "../queries";
import {
  callMoneyText,
  callStatusKind,
  classifyExportError,
  cleanSearchFilters,
  computeTimeline,
  dateRangeForPreset,
  decimalToNano,
  detectRangePreset,
  formatClock,
  formatDayHeading,
  formatDecimalMoney,
  formatSeconds,
  groupCallsByDay,
  hasActiveCallFilters,
  resolveRangePreset,
  summarizeAggregates,
  usageNumber,
} from "./calls-data";
import { buildChargeCells, buildDetailSteps, buildIdCells } from "./calls-detail-rows";
import { buildAggregateRows, buildCallListRows, buildSessionRows, buildSummaryItems } from "./calls-rows";
import {
  armCallsReturnScroll,
  getCallsReturnState,
  newCallsNavId,
  saveCallsNavigation,
  takeCallsScroll,
} from "./calls-scroll";

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

const fmtBundle = {
  money: fmtMoney,
  number: (n: number | bigint) => String(n),
  dateTime: (d: Date) => d.toISOString(),
};

const identityT = ((k: string, vars?: Record<string, unknown>) =>
  vars ? `${k}:${JSON.stringify(vars)}` : k) as unknown as Parameters<typeof buildCallListRows>[5];

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

  it("maps range presets strictly and disambiguates colliding boundaries via explicit preset", () => {
    const now = new Date(2026, 9, 24, 15, 30, 0);
    for (const preset of ["today", "d7", "d30", "month"] as const) {
      const range = dateRangeForPreset(preset, now);
      expect(detectRangePreset(range.since, range.until, now)).toBe(preset);
    }
    expect(detectRangePreset(undefined, undefined, now)).toBe("all");
    expect(detectRangePreset("2026-10-01T00:00:00Z", "2026-10-10T00:00:00Z", now)).toBe("custom");
    // 00:01 on the same day must not be mistaken for 00:00 midnight preset.
    const oneMinuteLate = new Date(new Date(2026, 9, 24).getTime() + 60_000).toISOString();
    expect(detectRangePreset(oneMinuteLate, undefined, now)).toBe("custom");
    // On Nov 7, d7 and month share the exact Nov 1 00:00 boundary; explicit preset preserves the choice.
    const nov7 = new Date(2026, 10, 7, 12, 0, 0);
    const nov1Iso = dateRangeForPreset("month", nov7).since;
    expect(resolveRangePreset(nov1Iso, undefined, "month", nov7)).toBe("month");
    expect(resolveRangePreset(nov1Iso, undefined, "d7", nov7)).toBe("d7");
  });

  it("carries server since and until window bounds on callSessionsQuery next page", async () => {
    const opts = callSessionsQuery({ model: "openai/gpt-5" });
    const nextParam = opts.getNextPageParam?.(
      {
        data: [],
        next_cursor: "cur-1",
        since: "2026-09-15T00:00:00Z",
        until: "2026-10-15T00:00:00Z",
      },
      [],
      undefined,
      [],
    );
    expect(nextParam).toEqual({
      cursor: "cur-1",
      since: "2026-09-15T00:00:00Z",
      until: "2026-10-15T00:00:00Z",
    });

    const spy = vi.spyOn(consoleApi, "GET").mockResolvedValueOnce({
      data: { data: [], next_cursor: null, since: "2026-09-15T00:00:00Z", until: "2026-10-15T00:00:00Z" },
      error: undefined,
      response: new Response(null, { status: 200 }),
    } as Awaited<ReturnType<typeof consoleApi.GET>>);
    try {
      await opts.queryFn?.({
        pageParam: nextParam,
        signal: new AbortController().signal,
        queryKey: opts.queryKey,
        meta: undefined,
        direction: "forward",
      } as never);
      expect(spy).toHaveBeenCalledWith(
        "/api/console/v1/calls/sessions",
        expect.objectContaining({
          params: {
            query: expect.objectContaining({
              model: "openai/gpt-5",
              cursor: "cur-1",
              since: "2026-09-15T00:00:00Z",
              until: "2026-10-15T00:00:00Z",
            }),
          },
        }),
      );
    } finally {
      spy.mockRestore();
    }
  });

  it("classifies status kinds including #756 fallbacks and failures", () => {
    expect(callStatusKind(baseCall)).toBe("ok");
    expect(callStatusKind({ ...baseCall, attempt_count: 2 })).toBe("retry");
    expect(callStatusKind({ ...baseCall, finish_reason: "cancelled" })).toBe("cancelled");
    expect(callStatusKind({ ...baseCall, error_kind: "upstream_error", attempt_count: 2 })).toBe("fail");
  });

  it("distinguishes export 400 rows limit, window limit, invalid fields, and other errors", () => {
    const now = new Date("2026-10-15T00:00:00Z");
    const badReq = (fields: Schemas["ErrorField"][] = []) =>
      new ApiError(400, null, { body: { error: { code: "invalid_request", message: "invalid_request", fields } } });
    expect(
      classifyExportError(
        badReq([{ pointer: "since", message: "more than 10000 calls; narrow the window or filters" }]),
        { since: "2026-10-10T00:00:00Z" },
        now,
      ),
    ).toBe("rows_limit");
    expect(
      classifyExportError(
        badReq([{ pointer: "since", message: "choose a nonempty window of at most 31 days" }]),
        { since: "2026-08-01T00:00:00Z", until: "2026-10-15T00:00:00Z" },
        now,
      ),
    ).toBe("window_limit");
    expect(
      classifyExportError(
        badReq([{ pointer: "request_id", message: "invalid identifier" }]),
        { request_id: "bad" },
        now,
      ),
    ).toBe("invalid_field");
    expect(classifyExportError(badReq(), { since: "2026-10-16T00:00:00Z", until: "2026-10-10T00:00:00Z" }, now)).toBe(
      "invalid_field",
    );
    expect(classifyExportError(new ApiError(500, null, { body: { error: { code: "internal_error" } } }), {}, now)).toBe(
      "other",
    );
    expect(classifyExportError(new Error("network"), {}, now)).toBe("other");
  });

  it("computes sorted attempt timeline percentages, TPS, and fallback route facts", () => {
    const call: Schemas["Call"] = {
      ...baseCall,
      latency_ms: 4000,
      first_token_ms: 1500,
      attempt_count: 2,
      route: {
        preset: { id: "preset_1", version: 2 },
        selected_endpoint: "ep-b",
        fastest_unmeasured: true,
        excluded_reasons: { rate_limit: 1 },
      },
      attempts: [
        {
          sequence: 2,
          endpoint_code: "ep-b",
          started_at: "2026-10-14T10:24:25.000Z",
          finished_at: "2026-10-14T10:24:28.000Z",
          result: "committed",
          charged: undefined,
        },
        {
          sequence: 1,
          endpoint_code: "",
          started_at: "2026-10-14T10:24:24.000Z",
          finished_at: "2026-10-14T10:24:25.000Z",
          result: "failed",
          charged: false,
        },
      ],
    };
    const tl = computeTimeline(call);
    expect(tl.scaleSec).toBe(10);
    expect(tl.failPct).toBeCloseTo(10, 4);
    expect(tl.waitPct).toBeCloseTo(5, 4);
    expect(tl.genPct).toBeCloseTo(25, 4);
    expect(tl.tps).toBe(80);
    expect(tl.attempts.map((a) => a.sequence)).toEqual([1, 2]);

    const steps = buildDetailSteps(call, fmtBundle, identityT);
    expect(steps[0]?.kind).toBe("0");
    if (steps[0]?.kind === "0") {
      expect(steps[0].f3).toContain("callsBoard.detail.attempt.noEndpoint");
      expect(steps[0].f4).toContain("callsBoard.detail.attempt.uncharged");
    }
    expect(steps[1]?.kind).toBe("0");
    if (steps[1]?.kind === "0") {
      expect(steps[1].f4).toContain("callsBoard.detail.attempt.chargedUnknown");
    }
    const routeStep = steps[2];
    expect(routeStep?.kind).toBe("1");
    if (routeStep?.kind === "1") {
      expect(routeStep.f9).toContain("callsBoard.detail.route.fastestUnmeasuredYes");
    }
  });

  it("formats decimals, prefers corrected charge_details over immutable call.price, and emits 3-column charge grid", () => {
    expect(decimalToNano("0.5")).toBe("500000000");
    expect(decimalToNano("-0.125")).toBe("-125000000");
    expect(decimalToNano("0.1234567890")).toBeNull();
    expect(formatDecimalMoney("0.5", "USD", fmtMoney)).toBe("USD 0.5");
    expect(formatDecimalMoney("0.1234567891", "USD", fmtMoney)).toBe("USD 0.1234567891");

    const corrected: Schemas["Call"] = {
      ...baseCall,
      price: { amount: "1000000000", currency: "CNY" },
      charge_details: {
        currency: "CNY",
        subtotal: "0.6",
        balance_adjustment: "-0.1",
        charged: "0.5",
        items: [{ item: "input", quantity: 600, unit: "token", unit_price: "0.001", amount: "0.6" }],
      },
    };
    expect(callMoneyText(corrected, "免单", fmtMoney)).toBe("CNY 0.5");

    const cells = buildChargeCells(corrected, fmtBundle, identityT);
    expect(cells.length % 3).toBe(0);
    const lastRow = cells.slice(-3);
    expect(lastRow[2]?.kind === "1" ? lastRow[2].f1 : "").toBe("CNY 0.5");
  });

  it("groups calls by day, formats helpers, summarizes aggregates, and isolates navigation state per navId + identity", () => {
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
      preset: undefined,
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

    const navA = newCallsNavId();
    saveCallsNavigation(navA, "user-a", { model: "gpt-5", fell_back: true }, "?model=gpt-5", 420);
    expect(getCallsReturnState(navA, "user-b").search).toBeUndefined();
    expect(getCallsReturnState(navA, "user-a")).toEqual({
      search: { model: "gpt-5", fell_back: true },
      hasScroll: true,
    });
    armCallsReturnScroll(navA, "user-a");
    expect(takeCallsScroll(navA, "user-b", "?model=gpt-5")).toBeNull();
    expect(takeCallsScroll(navA, "user-a", "?model=other")).toBeNull();
    expect(takeCallsScroll(navA, "user-a", "?model=gpt-5")).toBe(420);
    expect(takeCallsScroll(navA, "user-a", "?model=gpt-5")).toBeNull();
  });

  it("builds list, loaded-marked summary, narrow/desktop aggregate, session, and copyable ID rows", () => {
    const cachedCorrected: Schemas["Call"] = {
      ...baseCall,
      cache_hit: true,
      price: { amount: "1000000000", currency: "CNY" },
      charge_details: { currency: "CNY", charged: "0.2" },
    };
    const rows = buildCallListRows(
      [cachedCorrected],
      cachedCorrected.id,
      new Map([[cachedCorrected.key_id, "Prod"]]),
      fmtBundle,
      "zh-CN",
      identityT,
      (id) => `/calls/${id}`,
    );
    expect(rows.find((r) => r.kind === "0")).toMatchObject({
      kind: "0",
      f3: 'callsBoard.list.loadedDayCount:{"n":"1"}',
    });
    const dataRow = rows.find((r) => r.kind === "1");
    expect(dataRow && dataRow.kind === "1" ? [dataRow.f0, dataRow.f4, dataRow.f25] : []).toEqual([
      "lgr sel",
      `/calls/${cachedCorrected.id}`,
      "callsBoard.list.cacheTag · CNY 0.2",
    ]);

    const pendingSummary = buildSummaryItems(
      { data: [{ bucket: "2026-10-14", calls: "51", price: { amount: "1000000000", currency: "CNY" } }] },
      [],
      false,
      fmtBundle,
      identityT,
    );
    expect(pendingSummary[0]?.f1).toBe("51");
    expect(pendingSummary[2]?.f1).toBe("callsBoard.states.dash");

    const readySummary = buildSummaryItems(
      { data: [{ bucket: "2026-10-14", calls: "51", price: { amount: "1000000000", currency: "CNY" } }] },
      [{ ...baseCall, error_kind: "upstream_error", attempt_count: 2 }],
      true,
      fmtBundle,
      identityT,
    );
    expect(readySummary[2]).toMatchObject({ f0: "callsBoard.summary.failedLoaded", f1: "1" });
    expect(readySummary[3]).toMatchObject({ f0: "callsBoard.summary.fallbackLoaded", f1: "1" });

    const aggNarrow = buildAggregateRows(
      { data: [{ bucket: "key_1", calls: "4", price: { amount: "1000", currency: "USD" } }] },
      new Map([["key_1", "Prod"]]),
      "key",
      fmtBundle,
      identityT,
      true,
    );
    expect(aggNarrow.rows0[0]?.kind).toBe("1");
    if (aggNarrow.rows0[0]?.kind === "1") {
      expect(aggNarrow.rows0[0].f4).toBe("");
      expect(aggNarrow.rows0[0].f13).toBe("Prod");
      expect(aggNarrow.rows0[0].f8).toContain("4");
    }

    const sessNarrow = buildSessionRows(
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
      fmtBundle,
      identityT,
      true,
    );
    expect(sessNarrow.rows0[0]?.kind).toBe("1");
    if (sessNarrow.rows0[0]?.kind === "1") {
      expect(sessNarrow.rows0[0].f4).toBe("");
      expect(sessNarrow.rows0[0].f13).toBe("sess_1");
      expect(sessNarrow.rows0[0].f8).toContain("2");
      expect(sessNarrow.rows0[0].f6).toContain("openai/gpt-5");
    }

    const idCells = buildIdCells(baseCall, identityT);
    expect(idCells[0]).toMatchObject({
      kind: "0",
      f0: "1",
      f1: baseCall.id,
      f4: "callsBoard.detail.ids.callId",
    });
  });
});
