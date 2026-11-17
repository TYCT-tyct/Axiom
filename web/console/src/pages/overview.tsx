import type { Schemas } from "@axiom/api";
import DesktopOverview from "@axiom/boards/AxApp/regions/Overview";
import DesktopEN from "@axiom/boards/AxApp/regions/Overview.en";
import DesktopZH from "@axiom/boards/AxApp/regions/Overview.zh";
import PhoneOverview from "@axiom/boards/AxAppPhone/regions/Overview";
import PhoneEN from "@axiom/boards/AxAppPhone/regions/Overview.en";
import PhoneZH from "@axiom/boards/AxAppPhone/regions/Overview.zh";
import { DataState, useFormat, usePageTitle } from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { Announcements } from "../announcements";
import { balanceQuery, callAggregateQuery, keysQuery, recentErrorsQuery, siteQuery } from "../queries";

// The console's overview is the board's Overview slice (#747). The Shell slice that carries the board's frame and its
// page padding lands in its own change; until then the page keeps the frame classes the slice's CSS scopes itself with.
// When the shell replaces layout.tsx, the wrapper and the padding go with it.
export default function Overview() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const wide = useWide();
  const { balance, apiBase, text, rows } = useBoardData(t);
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  usePageTitle(t("nav.overview"));
  const labels = boardLabels(t);
  const desktopT = (key: keyof typeof DesktopZH) => labels(key) ?? (english ? DesktopEN[key] : DesktopZH[key]);
  const phoneT = (key: keyof typeof PhoneZH) => labels(key) ?? (english ? PhoneEN[key] : PhoneZH[key]);
  const values = {
    copyBase: () => copyBaseUrl(apiBase),
    nkx: () => void navigate({ to: "/keys" }),
    ko0_0: () => void navigate({ to: "/keys" }),
    go_usage: () => void navigate({ to: "/calls" }),
    go_logs: () => void navigate({ to: "/calls", search: { status: "error" } }),
  };
  return (
    <div className="board ax ov @container/ov">
      <div className="v8 pg-ov px-4 py-3.5 sm:px-10 sm:py-[30px]">
        {balance.isError ? (
          <DataState query={balance} compact>
            {() => null}
          </DataState>
        ) : null}
        {wide ? (
          <DesktopOverview t={desktopT} values={values} text={text} rows={rows} />
        ) : (
          <PhoneOverview t={phoneT} values={values} text={text} rows={rows} />
        )}
        <Announcements />
      </div>
    </div>
  );
}

// The board's labels name the panels of its demo account. Two metrics it draws are not built here (a median first token
// and a workspace budget, #816 G1/O23), so the page names the figures it does have in those slots, and the recent list
// is of the account's errors, which is what docs/frontend.md asks the overview for.
function boardLabels(t: TFunction) {
  return (key: string): string | undefined => {
    switch (key) {
      case "mfd198d22ce":
        return t("balance.available");
      case "mbd8dbd5724":
        return t("nav.keys");
      case "m5de8778457":
        return t("overview.recentErrors");
      case "m3b09c4395d":
        return t("overview.manageKeys");
      default:
        return undefined;
    }
  };
}

function copyBaseUrl(url: string | undefined) {
  if (url && navigator.clipboard) {
    void navigator.clipboard.writeText(url).catch(() => undefined);
  }
}

// The page meets the board's container queries and card layout from 1024px up; below, the phone slice is the design.
function useWide() {
  return useSyncExternalStore(
    (notify) => {
      const query = window.matchMedia("(min-width: 1024px)");
      query.addEventListener("change", notify);
      return () => query.removeEventListener("change", notify);
    },
    () => window.matchMedia("(min-width: 1024px)").matches,
    () => true,
  );
}

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

type Aggregate = Schemas["CallAggregate"];
type ApiKey = Schemas["Key"];
type Call = Schemas["Call"];
type Money = Schemas["Money"];
type Format = ReturnType<typeof useFormat>;
type Totals = { calls: bigint; amount: bigint };

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

export function dailyTotals(
  rows: Aggregate[] | undefined,
  currency: string | undefined,
): Map<number, bigint> | undefined {
  if (!rows || !currency) {
    return undefined;
  }
  const daily = new Map<number, bigint>();
  for (const row of rows) {
    if (row.price?.currency !== currency) {
      continue;
    }
    const day = Number((row.bucket ?? "").slice(8, 10));
    daily.set(day, (daily.get(day) ?? 0n) + BigInt(row.price.amount ?? "0"));
  }
  return daily;
}

// splitMoney puts the currency and integer digits beside the fraction, which the board sets smaller; the page's amount
// is the account's whole formatted sum, so the currency the account uses never becomes the board's .
export function splitMoney(text: string): [string, string] {
  const point = text.lastIndexOf(".");
  return point < 0 ? [text, ""] : [text.slice(0, point), text.slice(point)];
}

type BarRow = { key: string; kind: "0"; f0: string; f1: string; f2: string; f3: string; f4: string; f5: string };
type ModelRow = {
  key: string;
  kind: "0";
  f0: string;
  f1: string;
  f2: string;
  f3: string;
  f4: string;
  f5: string;
  f6: string;
  f7: string;
  f8: string;
};
type RequestRow = {
  key: string;
  kind: "0";
  f0: string;
  f1: string;
  f2: string;
  f3: string;
  f4: string;
  f5: string;
  f6: string;
  f7: string;
  f8: string;
  f9: string;
  f10: string;
  f11: string;
  f12: string;
  f13: string;
};

export function barRows(daily: Map<number, bigint> | undefined, days: number): BarRow[] {
  if (!daily?.size) {
    return [];
  }
  const max = [...daily.values()].reduce((a, b) => (b > a ? b : a), 0n);
  if (max === 0n) {
    return [];
  }
  const slot = 1000 / days;
  const width = slot * 0.6;
  return [...daily.entries()]
    .sort(([a], [b]) => a - b)
    .map(([day, amount]) => {
      const height = (Number(amount) / Number(max)) * 190;
      return {
        key: String(day),
        kind: "0" as const,
        f0: (slot * (day - 1) + (slot - width) / 2).toFixed(1),
        f1: (190 - height).toFixed(1),
        f2: width.toFixed(1),
        f3: height.toFixed(1),
        f4: "2",
        f5: "fill: var(--c0);",
      };
    });
}

export function chartLabels(days: number, month: number, note: string): { text: string; left: string }[] {
  const marks = [1, 8, 15, 22, days];
  if (note) {
    return [{ text: note, left: "left: 1.61%;" }, ...marks.slice(1).map(() => ({ text: "", left: "" }))];
  }
  return marks.map((day) => ({ text: `${month}/${day}`, left: `left: ${(((day - 0.5) / days) * 100).toFixed(2)}%;` }));
}

export type Quota = { key: ApiKey; percent: number; limit: Money; remaining: Money };

// pickQuota finds the enabled key nearest its spend limit: the one a warning is about.
export function pickQuota(keys: ApiKey[]): Quota | undefined {
  let best: Quota | undefined;
  for (const key of keys) {
    const limit = key.spend_limit;
    const remaining = key.limit_remaining;
    const expired = key.expires_at !== undefined && Date.parse(key.expires_at) <= Date.now();
    if (!limit || !remaining || key.disabled || expired || BigInt(limit.amount) <= 0n) {
      continue;
    }
    const used = BigInt(limit.amount) - BigInt(remaining.amount);
    const percent = used <= 0n ? 0 : Number((used * 100n) / BigInt(limit.amount));
    if (!best || percent > best.percent) {
      best = { key, percent, limit, remaining };
    }
  }
  return best;
}

function noticeOf(keys: ApiKey[], state: KeysState, format: Format, t: TFunction) {
  if (state === "pending") {
    return { noticeTitle: t("overview.loadingKeys"), noticeBody: "" };
  }
  if (state === "error") {
    return { noticeTitle: t("overview.keysFailed"), noticeBody: "" };
  }
  const quota = pickQuota(keys);
  if (quota) {
    return {
      noticeTitle: t("overview.quotaTitle", { name: quota.key.name, percent: quota.percent }),
      noticeBody: t("overview.quotaBody", {
        remaining: format.money(quota.remaining),
        limit: format.money(quota.limit),
      }),
    };
  }
  return keys.length === 0
    ? { noticeTitle: t("overview.noKeys"), noticeBody: t("overview.createFirstBody") }
    : { noticeTitle: t("overview.noLimitTitle", { count: keys.length }), noticeBody: t("overview.noLimitBody") };
}

type KeysState = "pending" | "error" | "ok";

function spendOf(balance: Schemas["Balance"] | undefined, format: Format, month: string) {
  if (!balance) {
    return { spendWhole: "—", spendFraction: "", spendNote: "" };
  }
  const [whole, fraction] = splitMoney(format.money(balance.spent_this_month));
  return { spendWhole: whole, spendFraction: fraction, spendNote: format.monthLabel(month) };
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
  const labels = chartLabels(window.days, window.monthNumber, daily?.size ? "" : note);
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

function modelRowsOf(
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
  const priced = rows
    .filter((row) => row.price?.currency === currency && BigInt(row.price.amount ?? "0") > 0n)
    .sort((a, b) => (BigInt(b.price?.amount ?? "0") > BigInt(a.price?.amount ?? "0") ? 1 : -1));
  const total = priced.reduce((sum, row) => sum + BigInt(row.price?.amount ?? "0"), 0n);
  if (total === 0n) {
    return [placeholderModel(t("overview.chartEmpty"))];
  }
  return priced.slice(0, 5).map((row, index) => {
    const amount = row.price?.amount ?? "0";
    const share = Number((BigInt(amount) * 10000n) / total) / 100;
    return {
      key: row.bucket ?? "",
      kind: "0" as const,
      f0: "ovmr",
      f1: `background: var(--c${index % 5});`,
      f2: "ovmn",
      f3: row.bucket ?? "",
      f4: "pc num",
      f5: `${format.number(Math.round(share))}%`,
      f6: "num",
      f7: format.money({ amount, currency }),
      f8: `--p: ${share.toFixed(1)}%; --c: var(--c${index % 5});`,
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

function requestRowsOf(
  errors: Call[] | undefined,
  note: string,
  keys: Map<string, string>,
  format: Format,
  t: TFunction,
): RequestRow[] {
  if (note) {
    return [placeholderRequest(note)];
  }
  return (errors ?? []).map((call, index) => ({
    key: call.id,
    kind: "0" as const,
    f0: "qrq",
    f1: "num mut",
    f2: format.hour(new Date(call.at)),
    f3: "qmn",
    f4: "qmd",
    f5: `background: var(--c${index % 5});`,
    f6: "mono",
    f7: call.model,
    f8: "mono mut",
    f9: keys.get(call.key_id) ?? "—",
    f10: "qsd fail",
    f11: call.error_kind || t("calls.failed"),
    f12: "num r",
    f13: format.money(call.price),
  }));
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

// cardNote is what one card says when its query gave nothing: a failure says so, and a successful empty answer says the
// card is empty, while the other cards keep their data.
function cardNote(state: { isError: boolean; isSuccess: boolean }, failed: string, isEmpty: boolean, empty: string) {
  if (state.isError) {
    return failed;
  }
  return state.isSuccess && isEmpty ? empty : "";
}

function notesOf(
  t: TFunction,
  days: { isError: boolean; isSuccess: boolean },
  models: { isError: boolean; isSuccess: boolean },
  errors: { isError: boolean; isSuccess: boolean; data?: { status_filter?: string; data: Call[] } },
  daily: Map<number, bigint> | undefined,
  modelTotal: bigint | undefined,
) {
  const errorNote = errors.isError
    ? t("overview.callsFailed")
    : errors.isSuccess && errors.data?.status_filter !== "error"
      ? t("overview.errorsUnavailable")
      : errors.isSuccess && errors.data?.data.length === 0
        ? t("overview.noErrors")
        : "";
  return {
    dayNote: cardNote(days, t("overview.chartFailed"), !daily?.size, t("overview.chartEmpty")),
    modelNote: cardNote(models, t("overview.modelsFailed"), modelTotal === 0n, t("overview.chartEmpty")),
    errorNote,
  };
}

function useBoardData(t: TFunction) {
  const format = useFormat();
  const keys = useInfiniteQuery(keysQuery());
  const errors = useQuery(recentErrorsQuery);
  const [window] = useState(monthWindow);
  const days = useQuery(callAggregateQuery({ since: window.since, until: window.until }, "day"));
  const models = useQuery(callAggregateQuery({ since: window.since, until: window.until }, "model"));
  const balance = useQuery(balanceQuery);
  const site = useQuery(siteQuery);
  const currency = balance.data?.balance.currency ?? site.data?.currency;
  const keyList = keys.data?.pages[0]?.data ?? [];
  const keyState: KeysState = keys.isPending ? "pending" : keys.isError ? "error" : "ok";
  const keysById = new Map(keyList.map((key) => [key.id, key.name]));
  const totals = sumTotals(days.data?.data, currency);
  const daily = dailyTotals(days.data?.data, currency);
  const { dayNote, modelNote, errorNote } = notesOf(
    t,
    days,
    models,
    errors,
    daily,
    sumTotals(models.data?.data, currency)?.amount,
  );
  const text = {
    ...noticeOf(keyList, keyState, format, t),
    ...spendOf(balance.data, format, window.month),
    ...requestsOf(totals, currency, format, t),
    ...balanceOf(balance.data, format, t),
    ...keysOf(keyList, keyState, keys.hasNextPage, format, t),
    ...chartOf(daily, dayNote, window),
  };
  const rows = {
    models0: modelRowsOf(models.data?.data, currency, modelNote, format, t),
    requests0: requestRowsOf(errors.data?.data, errorNote, keysById, format, t),
    bars0: barRows(daily, window.days),
  };
  return { balance, apiBase: site.data?.api_base_url, text, rows };
}
