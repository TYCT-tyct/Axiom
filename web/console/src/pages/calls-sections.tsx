import { ApiError, type Schemas } from "@axiom/api";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import LogsSeg from "@axiom/boards/AxApp/regions/LogsSeg";
import segZh from "@axiom/boards/AxApp/regions/LogsSeg.zh";
import { useFormat } from "@axiom/ui";
import type { InfiniteData, UseInfiniteQueryResult, UseQueryResult } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import type { CallGroupBy } from "../queries";
import { buildAggregateRows, buildSessionRows, buildSummaryItems } from "./calls-rows";
import { CallsAlertBanner, CallsCardNotice, CallsQueryState, PlainBoardCell, segClickIndex } from "./calls-states";

const groupOptions: readonly CallGroupBy[] = ["day", "model", "key"];

function SummaryGroupBar({
  t,
  groupBy,
  onGroupBy,
}: {
  t: TFunction;
  groupBy: CallGroupBy;
  onGroupBy: (next: CallGroupBy) => void;
}) {
  const groupIdx = Math.max(0, groupOptions.indexOf(groupBy));
  return (
    <LogsSeg
      t={(k) => segZh[k]}
      values={{
        ariaLabel: t("callsBoard.summary.groupBy"),
        segStyle: `--n: 3; --i: ${groupIdx}; margin-bottom: 8px;`,
        onClick: (e) => {
          const picked = groupOptions[segClickIndex(e)];
          if (picked) onGroupBy(picked);
        },
      }}
      rows={{
        items0: groupOptions.map((g) => ({
          key: g,
          kind: "0",
          f0: "button",
          f1: g === groupBy ? "on" : "",
          f2: t(`callsBoard.summary.groups.${g}`),
        })),
      }}
    />
  );
}

function summaryWindowItems(
  data: Schemas["CallAggregates"],
  loadedCalls: Schemas["Call"][],
  loadedReady: boolean,
  format: ReturnType<typeof useFormat>,
  t: TFunction,
) {
  const base = buildSummaryItems(data, loadedCalls, loadedReady, format, t);
  if (!data.since || !data.until) return base;
  const label = t("callsBoard.summary.window", {
    since: format.dateTime(new Date(data.since)),
    until: format.dateTime(new Date(data.until)),
  });
  return [...base, { key: "sum-win", kind: "1" as const, f0: label, f1: "" }];
}

function SummaryAggTable({ t, aggTable }: { t: TFunction; aggTable: ReturnType<typeof buildAggregateRows> }) {
  if (aggTable.rows0.length === 0) return <CallsCardNotice message={t("callsBoard.summary.empty")} />;
  return (
    <Logs
      t={(k) => logsZh[k]}
      link={PlainBoardCell}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        hideMore: true,
        hideStatusDot: true,
        rowStyle: "cursor: default;",
        unbuilt: true,
        tableLabel: t("callsBoard.summary.tableLabel"),
      }}
      rows={{ summaryItems0: [], ...aggTable }}
      text={{ heading: "", moreLabel: "" }}
    />
  );
}

export function SummarySection(p: {
  t: TFunction;
  narrow: boolean;
  report: UseQueryResult<Schemas["CallAggregates"], Error>;
  loadedCalls: Schemas["Call"][];
  loadedReady: boolean;
  groupBy: CallGroupBy;
  fellBackActive: boolean;
  keyNames: Map<string, string>;
  onGroupBy: (next: CallGroupBy) => void;
}) {
  const format = useFormat();
  const unavailable = p.report.error instanceof ApiError && p.report.error.status === 404;
  if (unavailable) {
    return (
      <CallsAlertBanner
        lead={p.t("calls.summary.unavailable")}
        body={p.t("calls.summary.unavailableBody")}
        role="status"
      />
    );
  }
  if (p.report.isPending || p.report.isError) {
    return (
      <CallsQueryState
        t={p.t}
        error={p.report.error}
        isPending={p.report.isPending}
        onRetry={() => void p.report.refetch()}
      />
    );
  }
  const aggTable = buildAggregateRows(p.report.data, p.keyNames, p.groupBy, format, p.t, p.narrow);
  return (
    <section aria-label={p.t("callsBoard.summary.title")}>
      {p.fellBackActive ? <CallsCardNotice message={p.t("callsBoard.summary.fallbackNote")} /> : null}
      <SummaryGroupBar t={p.t} groupBy={p.groupBy} onGroupBy={p.onGroupBy} />
      <Logs
        t={(k) => logsZh[k]}
        values={{
          hideHeader: true,
          hideFilters: true,
          hideList: true,
          unbuilt: true,
          summaryStyle: "margin-bottom: 8px;",
        }}
        rows={{
          summaryItems0: summaryWindowItems(p.report.data, p.loadedCalls, p.loadedReady, format, p.t),
          headers0: [],
          rows0: [],
        }}
        text={{ heading: "", moreLabel: "" }}
      />
      <SummaryAggTable t={p.t} aggTable={aggTable} />
    </section>
  );
}

export function SessionsSection({
  t,
  narrow,
  sessions,
  filtered,
  onClear,
}: {
  t: TFunction;
  narrow: boolean;
  sessions: UseInfiniteQueryResult<InfiniteData<Schemas["CallSessionList"]>, Error>;
  filtered: boolean;
  onClear: () => void;
}) {
  const format = useFormat();
  const unavailable = sessions.error instanceof ApiError && sessions.error.status === 404;
  if (unavailable) {
    return (
      <CallsAlertBanner
        lead={t("callsBoard.sessions.unsupportedTitle")}
        body={t("callsBoard.sessions.unsupportedBody")}
        role="status"
      />
    );
  }
  if (sessions.isPending || sessions.isError) {
    return (
      <CallsQueryState
        t={t}
        error={sessions.error}
        isPending={sessions.isPending}
        onRetry={() => void sessions.refetch()}
      />
    );
  }
  const items = sessions.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  if (items.length === 0) {
    return (
      <CallsCardNotice
        message={filtered ? t("callsBoard.sessions.emptyFiltered") : t("callsBoard.sessions.emptyTitle")}
        actionLabel={filtered ? t("callsBoard.filter.clear") : undefined}
        onAction={filtered ? onClear : undefined}
      />
    );
  }
  const table = buildSessionRows(items, format, t, narrow);
  return (
    <Logs
      t={(k) => logsZh[k]}
      link={PlainBoardCell}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        hideStatusDot: true,
        rowStyle: "cursor: default;",
        unbuilt: true,
        tableLabel: t("callsBoard.sessions.tableLabel"),
        hideMore: !sessions.hasNextPage,
        moreDisabled: sessions.isFetchingNextPage,
        onMore: () => void sessions.fetchNextPage(),
      }}
      rows={{ summaryItems0: [], ...table }}
      text={{
        heading: "",
        moreLabel: sessions.isFetchingNextPage ? t("callsBoard.list.loadingMore") : t("callsBoard.list.loadMore"),
      }}
    />
  );
}
