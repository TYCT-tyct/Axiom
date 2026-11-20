import type { Schemas } from "@axiom/api";
import type DesktopOverview from "@axiom/boards/AxApp/regions/Overview";
import { useFormat } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useCallback, useEffect, useState } from "react";
import {
  balanceQuery,
  callAggregateQuery,
  keysQuery,
  recentErrorsQuery,
  siteQuery,
  usageStatisticsQuery,
} from "../queries";
import { barRows, barStacks, spendChange, statisticsWindows } from "./overview-chart";
import { seriesColor } from "./usage-model";

// Everything the overview shows that is not a piece of markup: the month the figures cover, the board's rows and texts
// from the APIs, and the small sentences a card uses when its own query gave nothing.

export type MonthWindow = { since: string; until: string; month: string; monthNumber: number; days: number };

// monthWindow is the UTC calendar month the figures cover, as docs/frontend.md defines a month (the balance's month is
// UTC too), and the window the aggregation accepts is at most the month's own days.
export function monthWindow(now = new Date()): MonthWindow {
  const year = now.getUTCFullYear();
  const monthNumber = now.getUTCMonth() + 1;
  return {
    since: new Date(Date.UTC(year, monthNumber - 1, 1)).toISOString(),
    until: now.toISOString(),
    month: `${year}-${String(monthNumber).padStart(2, "0")}`,
    monthNumber,
    days: new Date(Date.UTC(year, monthNumber, 0)).getUTCDate(),
  };
}

// useMonthWindow keeps the window current: every focus or return to the tab moves it to that moment, and across a UTC
// month boundary it moves the month, its days and its start too. It always moves, not once a minute: a refetch at 10:00:45
// must cover a call at 10:00:20, and the query's own freshness window is 30 seconds.
function useMonthWindow() {
  const [period, setPeriod] = useState(monthWindow);
  const advance = useCallback(() => setPeriod(monthWindow()), []);
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== "hidden") {
        advance();
      }
    };
    window.addEventListener("focus", onVisible);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      window.removeEventListener("focus", onVisible);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [advance]);
  return { period, advance };
}

// useLocalMidnight renders the page again at each local midnight, so a time shown as today's becomes its date when the
// day ends while the page stays open. Nothing is fetched: the rows are the same, only the day they are read against
// moves. The timer re-arms from the moment it fired, so one that fires early waits out the rest of the day.
function useLocalMidnight() {
  const [tick, setTick] = useState(() => new Date());
  useEffect(() => {
    const midnight = new Date(tick);
    midnight.setHours(24, 0, 0, 0);
    const timer = setTimeout(() => setTick(new Date()), midnight.getTime() - Date.now());
    return () => clearTimeout(timer);
  }, [tick]);
}

type DesktopProps = Parameters<typeof DesktopOverview>[0];
type ModelRow = DesktopProps["rows"]["models0"][number];
type RequestRow = DesktopProps["rows"]["requests0"][number];
type Aggregate = Schemas["CallAggregate"];
type ApiKey = Schemas["Key"];
type Call = Schemas["Call"];
type Format = Pick<ReturnType<typeof useFormat>, "money" | "number" | "hour" | "monthLabel" | "monthName" | "dateTime">;
type Totals = { calls: bigint; amount: bigint };
type KeysState = "pending" | "error" | "ok";

export function sumTotals(rows: Aggregate[] | undefined, currency: string | undefined): Totals | undefined {
  if (!rows || !currency) {
    return undefined;
  }
  return rows.reduce<Totals>(
    (total, row) =>
      row.price?.currency === currency
        ? {
            calls: total.calls + BigInt(row.calls ?? "0"),
            amount: total.amount + BigInt(row.price.amount ?? "0"),
          }
        : total,
    { calls: 0n, amount: 0n },
  );
}

export type DayRow = { day: number; calls: bigint; amount: bigint };

// dailyRows keeps a day whose calls cost nothing: the calls happened, so the day is part of the shape of the month even
// when its price is zero.
export function dailyRows(rows: Aggregate[] | undefined, currency: string | undefined): DayRow[] | undefined {
  if (!rows || !currency) {
    return undefined;
  }
  const daily = new Map<number, DayRow>();
  for (const row of rows) {
    if (row.price?.currency !== currency) {
      continue;
    }
    const day = Number((row.bucket ?? "").slice(8, 10));
    const entry = daily.get(day) ?? { day, calls: 0n, amount: 0n };
    entry.calls += BigInt(row.calls ?? "0");
    entry.amount += BigInt(row.price.amount ?? "0");
    daily.set(day, entry);
  }
  return [...daily.values()].sort((a, b) => a.day - b.day);
}

export function dailyTotals(
  rows: Aggregate[] | undefined,
  currency: string | undefined,
): Map<number, bigint> | undefined {
  const daily = dailyRows(rows, currency);
  return daily && new Map(daily.map((row) => [row.day, row.amount]));
}

// splitMoney puts the currency and integer digits beside the fraction, which the board sets smaller; the page's amount
// is the account's whole formatted sum, so the currency the account uses never becomes the board's .
export function splitMoney(text: string): [string, string] {
  const point = text.lastIndexOf(".");
  return point < 0 ? [text, ""] : [text.slice(0, point), text.slice(point)];
}

export function chartLabels(days: number, month: number, note: string): { text: string; left: string }[] {
  const marks = [1, 8, 15, 22, days];
  if (note) {
    return [{ text: note, left: "left: 1.61%;" }, ...marks.slice(1).map(() => ({ text: "", left: "" }))];
  }
  return marks.map((day) => ({ text: `${month}/${day}`, left: `left: ${(((day - 0.5) / days) * 100).toFixed(2)}%;` }));
}

// spendOf writes the month's spend, and under it its change from the same span of last month, or the month's name when
// there is no change to give.
function spendOf(
  balance: Schemas["Balance"] | undefined,
  format: Format,
  t: TFunction,
  month: string,
  change: { value: string | undefined; lastMonth: number },
) {
  if (!balance) {
    return { spendWhole: "—", spendFraction: "", spendNote: "" };
  }
  const [whole, fraction] = splitMoney(format.money(balance.spent_this_month));
  const note = change.value
    ? t("overview.spendChange", {
        change: change.value,
        month: change.lastMonth,
        monthName: format.monthName(change.lastMonth),
      })
    : format.monthLabel(month);
  return { spendWhole: whole, spendFraction: fraction, spendNote: note };
}

function requestsOf(totals: Totals | undefined, currency: string | undefined, format: Format, t: TFunction) {
  if (!totals) {
    return { requestTotal: "—", requestAverage: "" };
  }
  const average =
    totals.calls > 0n && currency ? format.money({ amount: (totals.amount / totals.calls).toString(), currency }) : "";
  return {
    requestTotal: format.number(totals.calls),
    requestAverage: average ? t("overview.averagePerCall", { amount: average }) : "—",
  };
}

function balanceOf(balance: Schemas["Balance"] | undefined, format: Format, t: TFunction) {
  return {
    balance: balance ? format.money(balance.available) : "—",
    balanceNote: t("overview.balanceNote"),
  };
}

function keysOf(keys: ApiKey[], state: KeysState, more: boolean, format: Format, t: TFunction) {
  if (state !== "ok") {
    return { keyCount: "—", keyNote: t(state === "pending" ? "overview.loadingKeys" : "overview.keysFailed") };
  }
  const active = keys.filter(
    (key) => !key.disabled && !(key.expires_at && Date.parse(key.expires_at) <= Date.now()),
  ).length;
  return {
    keyCount: `${format.number(keys.length)}${more ? "+" : ""}`,
    keyNote: t("overview.keysNote", { enabled: active, disabled: keys.length - active }),
  };
}

function chartOf(daily: Map<number, bigint> | undefined, note: string, window: MonthWindow) {
  const hasSpend = daily !== undefined && [...daily.values()].some((amount) => amount > 0n);
  const labels = chartLabels(window.days, window.monthNumber, hasSpend ? "" : note);
  const at = (index: number) => labels[index] ?? { text: "", left: "" };
  return {
    chartDate0: at(0).text,
    chartDateLeft0: at(0).left,
    chartDate1: at(1).text,
    chartDateLeft1: at(1).left,
    chartDate2: at(2).text,
    chartDateLeft2: at(2).left,
    chartDate3: at(3).text,
    chartDateLeft3: at(3).left,
    chartDate4: at(4).text,
    chartDateLeft4: at(4).left,
  };
}

// rankedModels orders the month's models with calls by spend, then by calls: the model card's order, and the colours
// the chart gives the models.
function rankedModels(rows: Aggregate[] | undefined, currency: string | undefined): Aggregate[] {
  return (rows ?? [])
    .filter((row) => row.price?.currency === currency && BigInt(row.calls ?? "0") > 0n)
    .sort((a, b) => {
      const amountA = BigInt(a.price?.amount ?? "0");
      const amountB = BigInt(b.price?.amount ?? "0");
      if (amountA !== amountB) {
        return amountB > amountA ? 1 : -1;
      }
      const callsA = BigInt(a.calls ?? "0");
      const callsB = BigInt(b.calls ?? "0");
      return callsB > callsA ? 1 : callsB < callsA ? -1 : 0;
    });
}

// modelRowsOf keeps the models with calls even when their calls cost nothing: a free or failed call is still a call, and
// the card shows its zero spend rather than saying the month is empty.
export function modelRowsOf(
  rows: Aggregate[] | undefined,
  currency: string | undefined,
  note: string,
  format: Format,
  t: TFunction,
): ModelRow[] {
  if (note) {
    return [placeholderModel(note)];
  }
  if (!rows || !currency) {
    return [];
  }
  const called = rankedModels(rows, currency);
  if (called.length === 0) {
    return [placeholderModel(t("overview.chartEmpty"))];
  }
  const total = called.reduce((sum, row) => sum + BigInt(row.price?.amount ?? "0"), 0n);
  return called.slice(0, 5).map((row, index) => {
    const amount = row.price?.amount ?? "0";
    const share = total === 0n ? 0 : Number((BigInt(amount) * 10000n) / total) / 100;
    return {
      key: row.bucket ?? "",
      kind: "0" as const,
      f0: "ovmr",
      f1: `background: ${seriesColor(index)};`,
      f2: "ovmn",
      f3: row.bucket ?? "",
      f4: "pc num",
      f5: `${format.number(Math.round(share))}%`,
      f6: "num",
      f7: format.money({ amount, currency }),
      f8: `--p: ${share.toFixed(1)}%; --c: ${seriesColor(index)};`,
    };
  });
}

function placeholderModel(note: string): ModelRow {
  return {
    key: "empty",
    kind: "0",
    f0: "ovmr",
    f1: "background: var(--line);",
    f2: "ovmn",
    f3: note,
    f4: "pc num",
    f5: "",
    f6: "num",
    f7: "",
    f8: "--p: 0%; --c: var(--line);",
  };
}

// requestRowsOf writes the reason into the model cell: the slice hides the status column below 1100px, and the reason
// is the one thing a narrow reader must still see. The time and status columns are the board's narrow ones (64px and
// 76px), so they hold the time of an error today or the date of an older one, and the word for a failed call; the full
// time and reason are in the error list below the slice.
export function requestRowsOf(
  errors: Call[] | undefined,
  note: string,
  keys: Map<string, string>,
  format: Format,
  t: TFunction,
  now = new Date(),
): RequestRow[] {
  if (note) {
    return [placeholderRequest(note)];
  }
  return (errors ?? []).map((call) => {
    const at = new Date(call.at);
    const reason = call.error_kind || t("calls.failed");
    const today = at.toDateString() === now.toDateString();
    return {
      key: call.id,
      kind: "0" as const,
      f0: "qrq",
      f1: "num mut",
      f2: today ? format.hour(at) : `${at.getMonth() + 1}/${at.getDate()}`,
      f3: "qmn",
      f4: "qmd",
      f5: "background: var(--bad);",
      f6: "mono",
      f7: `${reason} · ${call.model}`,
      f8: "mono mut",
      f9: keys.get(call.key_id) ?? "—",
      f10: "",
      f11: t("calls.failed"),
      f12: "num r",
      f13: format.money(call.price),
    };
  });
}

function placeholderRequest(note: string): RequestRow {
  return {
    key: "empty",
    kind: "0",
    f0: "qrq",
    f1: "num mut",
    f2: "",
    f3: "qmn",
    f4: "qmd",
    f5: "",
    f6: "mono",
    f7: note,
    f8: "mono mut",
    f9: "",
    f10: "",
    f11: "",
    f12: "num r",
    f13: "",
  };
}

// notesOf names what a card says when its own query gave nothing: a failure says so, an account without calls says the
// month is empty, and an account whose calls cost nothing says there is no spending, without dropping the calls.
function notesOf(
  t: TFunction,
  days: { isError: boolean; isSuccess: boolean },
  models: { isError: boolean; isSuccess: boolean },
  errors: { isError: boolean; isSuccess: boolean; data?: { status_filter?: string; data: Call[] } },
  totals: Totals | undefined,
  modelCalls: bigint,
) {
  const dayNote = days.isError
    ? t("overview.chartFailed")
    : days.isSuccess
      ? totals?.calls
        ? totals.amount > 0n
          ? ""
          : t("overview.chartNoSpend")
        : t("overview.chartEmpty")
      : "";
  const modelNote = models.isError
    ? t("overview.modelsFailed")
    : models.isSuccess && modelCalls === 0n
      ? t("overview.chartEmpty")
      : "";
  return { dayNote, modelNote, errorNote: errorNoteOf(t, errors) };
}

function errorNoteOf(
  t: TFunction,
  errors: { isError: boolean; isSuccess: boolean; data?: { status_filter?: string; data: Call[] } },
) {
  if (errors.isError) {
    return t("overview.callsFailed");
  }
  if (!errors.isSuccess) {
    return "";
  }
  if (errors.data?.status_filter !== "error") {
    return t("overview.errorsUnavailable");
  }
  return errors.data.data.length === 0 ? t("overview.noErrors") : "";
}

function keyStateOf(keys: { isPending: boolean; isError: boolean }): KeysState {
  if (keys.isPending) {
    return "pending";
  }
  return keys.isError ? "error" : "ok";
}

function errorCallsOf(data: { status_filter?: string; data: Call[] } | undefined): Call[] {
  return data?.status_filter === "error" ? data.data : [];
}

function dayTableOf(rows: DayRow[] | undefined, period: MonthWindow, currency: string | undefined, format: Format) {
  if (!rows || !currency) {
    return [];
  }
  return rows.map((row) => ({
    label: `${period.monthNumber}/${row.day}`,
    calls: format.number(row.calls),
    amount: format.money({ amount: row.amount.toString(), currency }),
  }));
}

function errorListOf(calls: Call[], format: Format, t: TFunction) {
  return calls.map((call) => ({
    id: call.id,
    label: `${format.dateTime(new Date(call.at))} · ${call.model} · ${call.error_kind || t("calls.failed")} · ${format.money(call.price)}`,
  }));
}

// aggregateFailure answers one retry for both aggregates: they come from the same endpoint, and a reader who retries
// wants the chart and the model card back together.
function aggregateFailure(
  days: { isError: boolean; error: unknown; errorUpdatedAt: number },
  models: { isError: boolean; error: unknown; errorUpdatedAt: number },
  advance: () => void,
) {
  if (!days.isError && !models.isError) {
    return undefined;
  }
  return {
    status: "error" as const,
    data: undefined,
    error: days.error ?? models.error,
    errorUpdatedAt: Math.max(days.errorUpdatedAt, models.errorUpdatedAt),
    // The retry moves the window first: the new query keys fetch both aggregates from that moment.
    refetch: advance,
  };
}

export function useBoardData(t: TFunction) {
  const format = useFormat();
  const { period, advance } = useMonthWindow();
  useLocalMidnight();
  const windows = statisticsWindows(period);
  const keys = useInfiniteQuery(keysQuery());
  const errors = useQuery(recentErrorsQuery);
  const days = useQuery(callAggregateQuery({ since: period.since, until: period.until }, "day"));
  const models = useQuery(callAggregateQuery({ since: period.since, until: period.until }, "model"));
  // Only the chart's split by model and the spend's comparison wait on these, in parallel with the rest.
  const byModel = useQuery(usageStatisticsQuery({ ...windows.month, granularity: "day", group_by: "model" }));
  const lastMonth = useQuery(usageStatisticsQuery({ ...windows.lastMonth, granularity: "week" }));
  const balance = useQuery(balanceQuery);
  const site = useQuery(siteQuery);
  const currency = balance.data?.balance.currency ?? site.data?.currency;
  const keyList = keys.data?.pages[0]?.data ?? [];
  const keyState = keyStateOf(keys);
  const totals = sumTotals(days.data?.data, currency);
  const modelTotals = sumTotals(models.data?.data, currency);
  const daily = dailyTotals(days.data?.data, currency);
  const { dayNote, modelNote, errorNote } = notesOf(t, days, models, errors, totals, modelTotals?.calls ?? 0n);
  const errorCalls = errorCallsOf(errors.data);
  return {
    balance,
    apiBase: site.data?.api_base_url,
    siteError: site.isError,
    period,
    keys,
    errors,
    aggregate: aggregateFailure(days, models, advance),
    text: {
      ...spendOf(balance.data, format, t, period.month, {
        value: spendChange(byModel.data, lastMonth.data, currency),
        lastMonth: windows.lastMonthNumber,
      }),
      ...requestsOf(totals, currency, format, t),
      ...balanceOf(balance.data, format, t),
      ...keysOf(keyList, keyState, keys.hasNextPage, format, t),
      ...chartOf(daily, dayNote, period),
    },
    rows: {
      models0: modelRowsOf(models.data?.data, currency, modelNote, format, t),
      requests0: requestRowsOf(errorCalls, errorNote, new Map(keyList.map((key) => [key.id, key.name])), format, t),
      bars0: barRows(
        barStacks(
          byModel.data,
          daily,
          currency,
          rankedModels(models.data?.data, currency).map((row) => row.bucket ?? ""),
        ),
        period.days,
      ),
    },
    dayTable: dayTableOf(dailyRows(days.data?.data, currency), period, currency, format),
    errorList: errorListOf(errorCalls, format, t),
  };
}
