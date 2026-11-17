import DesktopOverview from "@axiom/boards/AxApp/regions/Overview";
import DesktopEN from "@axiom/boards/AxApp/regions/Overview.en";
import DesktopZH from "@axiom/boards/AxApp/regions/Overview.zh";
import PhoneOverview from "@axiom/boards/AxAppPhone/regions/Overview";
import PhoneEN from "@axiom/boards/AxAppPhone/regions/Overview.en";
import PhoneZH from "@axiom/boards/AxAppPhone/regions/Overview.zh";
import { DataState, useFormat, usePageTitle } from "@axiom/ui";
import { Link, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { Announcements } from "../announcements";
import { useBoardData } from "./overview-data";

// The console's overview is the board's Overview slice (#747). The Shell slice that carries the board's frame and its
// page padding lands in its own change; until then the page keeps the frame classes the slice's CSS scopes itself with.
// When the shell replaces layout.tsx, the wrapper and the padding go with it.
export default function Overview() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const wide = useWide();
  const format = useFormat();
  const data = useBoardData(t);
  const { copy, copyBase } = useCopy(data.apiBase);
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  usePageTitle(t("nav.overview"));
  const labels = boardLabels(t, copy, data.apiBase, data.siteError);
  const desktopT = (key: keyof typeof DesktopZH) => labels(key) ?? (english ? DesktopEN[key] : DesktopZH[key]);
  const phoneT = (key: keyof typeof PhoneZH) => labels(key) ?? (english ? PhoneEN[key] : PhoneZH[key]);
  const values = {
    copyBase,
    nkx: () => void navigate({ to: "/keys" }),
    ko0_0: () => void navigate({ to: "/keys" }),
    go_usage: () => void navigate({ to: "/calls" }),
    go_logs: () => void navigate({ to: "/calls", search: { status: "error" } }),
  };
  return (
    <div className="board ax ov @container/ov">
      <div className="v8 pg-ov px-4 py-3.5 sm:px-10 sm:py-[30px]">
        <Failures data={data} />
        {copy === "failed" ? <CopyFailure apiBase={data.apiBase} t={t} /> : null}
        {wide ? (
          <DesktopOverview t={desktopT} values={values} text={data.text} rows={data.rows} />
        ) : (
          <PhoneOverview t={phoneT} values={values} text={data.text} rows={data.rows} />
        )}
        <DailySpend rows={data.dayTable} month={format.monthLabel(data.period.month)} t={t} />
        <ErrorLinks list={data.errorList} t={t} />
        <Announcements />
      </div>
    </div>
  );
}

type BoardData = ReturnType<typeof useBoardData>;
type CopyState = "idle" | "copied" | "failed";

// Failures give a failed query the shared state, with its reason, request ID, permission branch and retry, while the
// cards it feeds keep their own placeholders.
function Failures({ data }: { data: BoardData }) {
  return (
    <>
      {data.balance.isError ? (
        <DataState query={data.balance} compact>
          {() => null}
        </DataState>
      ) : null}
      {data.keys.isError ? (
        <DataState query={data.keys} compact>
          {() => null}
        </DataState>
      ) : null}
      {data.errors.isError ? (
        <DataState query={data.errors} compact>
          {() => null}
        </DataState>
      ) : null}
      {data.aggregate ? (
        <DataState query={data.aggregate} compact>
          {() => null}
        </DataState>
      ) : null}
    </>
  );
}

// CopyFailure leaves the address on the page, because the clipboard may be refused and the only way to the address is
// then to select it by hand.
function CopyFailure({ apiBase, t }: { apiBase: string | undefined; t: TFunction }) {
  return (
    <p role="alert">
      {apiBase ? t("overview.copyFailedBody") : t("overview.copyUnavailableBody")}
      {apiBase ? <code>{apiBase}</code> : null}
    </p>
  );
}

// DailySpend is the accessible form of the daily chart: the slice draws bars without an axis or a tooltip, so a reader
// gets each day's calls and amount here, in the account's currency.
function DailySpend({ rows, month, t }: { rows: BoardData["dayTable"]; month: string; t: TFunction }) {
  if (rows.length === 0) {
    return null;
  }
  return (
    <details>
      <summary className="min-h-6 mb-8">{t("overview.dailySpend")}</summary>
      <p>{month}</p>
      <dl>
        {rows.map((row) => (
          <div key={row.label}>
            <dt>{row.label}</dt>
            <dd>{`${row.calls} · ${row.amount}`}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

// ErrorLinks gives the exact time, model, reason and cost of each recent error and a link to the call itself: the slice's
// rows are not links, and the status column is hidden when the page is narrow.
function ErrorLinks({ list, t }: { list: BoardData["errorList"]; t: TFunction }) {
  if (list.length === 0) {
    return null;
  }
  return (
    <details>
      <summary className="min-h-6 mb-8">{t("overview.errorDetails")}</summary>
      <ul>
        {list.map((error) => (
          <li key={error.id}>
            <Link to="/calls/$callId" params={{ callId: error.id }}>
              {error.label}
            </Link>
          </li>
        ))}
      </ul>
    </details>
  );
}

function useCopy(apiBase: string | undefined) {
  const [copy, setCopy] = useState<CopyState>("idle");
  // A success is worth showing for a moment; a failure stays until the next attempt, so the address it leaves on the
  // page is still there to select by hand.
  useEffect(() => {
    if (copy !== "copied") {
      return;
    }
    const timer = setTimeout(() => setCopy("idle"), 3000);
    return () => clearTimeout(timer);
  }, [copy]);
  const copyBase = () => {
    if (!apiBase || !navigator.clipboard) {
      setCopy("failed");
      return;
    }
    navigator.clipboard.writeText(apiBase).then(
      () => setCopy("copied"),
      () => setCopy("failed"),
    );
  };
  return { copy, copyBase };
}

// The board's labels name the panels of its demo account. Two metrics it draws are not built here (a median first token
// and a workspace budget, #816 G1/O23), so the page names the figures it does have in those slots; the recent list is of
// the account's errors, which is what docs/frontend.md asks the overview for; and the copy button says how its last copy
// went, since the slice has no place for a message of its own.
function boardLabels(t: TFunction, copy: CopyState, apiBase: string | undefined, siteError: boolean) {
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
      case "m569c43d6cc":
        if (copy === "copied") {
          return t("overview.copied");
        }
        if (copy === "failed") {
          return t("overview.copyFailed");
        }
        return apiBase === undefined && siteError ? t("overview.copyUnavailable") : undefined;
      default:
        return undefined;
    }
  };
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
