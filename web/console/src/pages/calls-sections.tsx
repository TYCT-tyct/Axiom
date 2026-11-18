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
import { CallsAlertBanner, CallsCardNotice, CallsQueryState, segClickIndex } from "./calls-states";

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

export function SummarySection({
  t,
  report,
  loadedCalls,
  groupBy,
  fellBackActive,
  keyNames,
  onGroupBy,
}: {
  t: TFunction;
  report: UseQueryResult<Schemas["CallAggregates"], Error>;
  loadedCalls: Schemas["Call"][];
  groupBy: CallGroupBy;
  fellBackActive: boolean;
  keyNames: Map<string, string>;
  onGroupBy: (next: CallGroupBy) => void;
}) {
  const format = useFormat();
  const unavailable = report.error instanceof ApiError && report.error.status === 404;
  if (unavailable) {
    return (
      <CallsAlertBanner lead={t("calls.summary.unavailable")} body={t("calls.summary.unavailableBody")} role="status" />
    );
  }
  if (report.isPending || report.isError) {
    return (
      <CallsQueryState t={t} error={report.error} isPending={report.isPending} onRetry={() => void report.refetch()} />
    );
  }
  const aggTable = buildAggregateRows(report.data, keyNames, groupBy, format, t);
  return (
    <section aria-label={t("callsBoard.summary.title")}>
      {fellBackActive ? <CallsCardNotice message={t("callsBoard.summary.fallbackNote")} /> : null}
      <SummaryGroupBar t={t} groupBy={groupBy} onGroupBy={onGroupBy} />
      <Logs
        t={(k) => logsZh[k]}
        values={{
          hideHeader: true,
          hideFilters: true,
          hideList: true,
          unbuilt: true,
          summaryStyle: "margin-bottom: 8px;",
        }}
        rows={{ summaryItems0: buildSummaryItems(report.data, loadedCalls, format, t), headers0: [], rows0: [] }}
        text={{ heading: "", moreLabel: "" }}
      />
      {aggTable.rows0.length === 0 ? (
        <CallsCardNotice message={t("callsBoard.summary.empty")} />
      ) : (
        <Logs
          t={(k) => logsZh[k]}
          values={{
            hideHeader: true,
            hideFilters: true,
            hideSummary: true,
            hideMore: true,
            unbuilt: true,
            tableLabel: t("callsBoard.summary.tableLabel"),
          }}
          rows={{ summaryItems0: [], ...aggTable }}
          text={{ heading: "", moreLabel: "" }}
        />
      )}
    </section>
  );
}

export function SessionsSection({
  t,
  sessions,
  filtered,
  onClear,
  onPickClient,
}: {
  t: TFunction;
  sessions: UseInfiniteQueryResult<InfiniteData<Schemas["CallSessionList"]>, Error>;
  filtered: boolean;
  onClear: () => void;
  onPickClient: (clientApp: string) => void;
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
  const table = buildSessionRows(items, format, t);
  return (
    <Logs
      t={(k) => logsZh[k]}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        unbuilt: true,
        tableLabel: t("callsBoard.sessions.tableLabel"),
        hideMore: !sessions.hasNextPage,
        moreDisabled: sessions.isFetchingNextPage,
        onMore: () => void sessions.fetchNextPage(),
        onListClick: (e) => {
          const row = (e.target as Element).closest('[data-a="session"]');
          const client = row?.getAttribute("data-id") ?? "";
          if ((e.target as Element).closest("a.lgt")) e.preventDefault();
          if (row) onPickClient(client);
        },
      }}
      rows={{ summaryItems0: [], ...table }}
      text={{
        heading: "",
        moreLabel: sessions.isFetchingNextPage ? t("callsBoard.list.loadingMore") : t("callsBoard.list.loadMore"),
      }}
    />
  );
}
