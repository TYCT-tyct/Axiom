import type { Schemas } from "@axiom/api";
import { can, useErrors, useSession, useSessionBound } from "@axiom/app";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import LogsBtn from "@axiom/boards/AxApp/regions/LogsBtn";
import btnZh from "@axiom/boards/AxApp/regions/LogsBtn.zh";
import LogsSeg from "@axiom/boards/AxApp/regions/LogsSeg";
import segZh from "@axiom/boards/AxApp/regions/LogsSeg.zh";
import { useFormat, usePageTitle } from "@axiom/ui";
import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseQueryResult,
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import { useLocation, useNavigate, useRouter, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { callsCsv, callsName, filtersUnsupported } from "../calls";
import {
  type CallFilters,
  type CallGroupBy,
  callAggregateQuery,
  callQuery,
  callSessionsQuery,
  callsQuery,
  keysQuery,
  modelsQuery,
  workspacesQuery,
} from "../queries";
import { save } from "../statement";
import { cleanSearchFilters, dateRangeForPreset, hasActiveCallFilters, isExportLimitError } from "./calls-data";
import { InlineCallSplit } from "./calls-detail";
import { CallsFilterBar } from "./calls-filters";
import { buildCallListRows, callHeaders } from "./calls-rows";
import { saveCallsScroll, saveLastCallsSearch, takeCallsScroll } from "./calls-scroll";
import { SessionsSection, SummarySection } from "./calls-sections";
import {
  CallsAlertBanner,
  CallsCardNotice,
  CallsPageShell,
  CallsQueryState,
  segClickIndex,
  usePhoneWidth,
} from "./calls-states";

type CallsSearch = CallFilters & { group_by?: CallGroupBy; tab?: "sessions"; call?: string };

function useCallsExport(filters: CallFilters) {
  const bound = useSessionBound();
  const [failed, setFailed] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const start = () => {
    setBusy(true);
    setFailed(null);
    void bound((signal) => callsCsv(filters, signal))
      .then((ans) => {
        if (ans) save(new File([ans.value], callsName(), { type: "text/csv" }));
      })
      .catch(setFailed)
      .finally(() => setBusy(false));
  };
  return { failed, busy, start, dismiss: () => setFailed(null) };
}

function ExportNotice({
  t,
  error,
  onUse7d,
  onDismiss,
}: {
  t: TFunction;
  error: unknown;
  onUse7d: () => void;
  onDismiss: () => void;
}) {
  const errors = useErrors();
  if (!error) return null;
  if (isExportLimitError(error)) {
    return (
      <CallsAlertBanner
        lead={t("callsBoard.export.limitTitle")}
        body={t("callsBoard.export.limitBody")}
        actionLabel={t("callsBoard.export.use7d")}
        onAction={onUse7d}
      />
    );
  }
  return (
    <CallsAlertBanner
      lead={t("callsBoard.export.failed", { reason: errors.message(error) })}
      body=""
      actionLabel={t("callsBoard.export.dismiss")}
      onAction={onDismiss}
    />
  );
}

function CallsHeaderActions({
  t,
  sessionsTab,
  exportBusy,
  onTab,
  onExport,
}: {
  t: TFunction;
  sessionsTab: boolean;
  exportBusy: boolean;
  onTab: (tab: "sessions" | undefined) => void;
  onExport: () => void;
}) {
  return (
    <>
      <LogsSeg
        t={(k) => segZh[k]}
        values={{
          ariaLabel: t("callsBoard.title"),
          segStyle: `--n: 2; --i: ${sessionsTab ? 1 : 0};`,
          onClick: (e) => {
            const idx = segClickIndex(e);
            if (idx >= 0) onTab(idx === 1 ? "sessions" : undefined);
          },
        }}
        rows={{
          items0: [
            { key: "calls", kind: "0", f0: "button", f1: sessionsTab ? "" : "on", f2: t("callsBoard.tabs.calls") },
            {
              key: "sessions",
              kind: "0",
              f0: "button",
              f1: sessionsTab ? "on" : "",
              f2: t("callsBoard.tabs.sessions"),
            },
          ],
        }}
      />
      <LogsBtn
        t={(k) => btnZh[k]}
        values={{ disabled: exportBusy, onClick: onExport }}
        text={{ label: exportBusy ? t("callsBoard.export.busy") : t("callsBoard.export.action") }}
      />
    </>
  );
}

function handleCallRowClick(
  e: React.SyntheticEvent,
  selectedId: string | undefined,
  onSelectCall: (id?: string) => void,
  onOpenCall: (id: string) => void,
) {
  const id = (e.target as Element).closest('[data-a="lrow"]')?.getAttribute("data-id");
  if (!id) return;
  if ((e.target as Element).closest("a.lgt")) {
    e.preventDefault();
    onOpenCall(id);
    return;
  }
  onSelectCall(id === selectedId ? undefined : id);
}

type CallsListBodyProps = {
  t: TFunction;
  locale: string;
  phone: boolean;
  calls: UseInfiniteQueryResult<InfiniteData<Schemas["CallList"]>, Error>;
  callRows: Schemas["Call"][];
  selectedId?: string;
  selectedQuery: UseQueryResult<Schemas["Call"], Error>;
  keyNames: Map<string, string>;
  filtered: boolean;
  onClear: () => void;
  onSelectCall: (id?: string) => void;
  onOpenCall: (id: string) => void;
};

function CallsListTable(p: CallsListBodyProps) {
  const format = useFormat();
  const headers = callHeaders(p.t);
  const rows = buildCallListRows(p.callRows, p.selectedId, p.keyNames, format, p.locale, p.t);
  const idx = p.selectedId ? p.callRows.findIndex((c) => c.id === p.selectedId) : -1;
  const moreLabel = p.calls.isFetchingNextPage ? p.t("callsBoard.list.loadingMore") : p.t("callsBoard.list.loadMore");
  const onListClick = (e: React.SyntheticEvent) => handleCallRowClick(e, p.selectedId, p.onSelectCall, p.onOpenCall);
  if (p.selectedId) {
    return (
      <InlineCallSplit
        phone={p.phone}
        headers={headers}
        rows={rows}
        moreLabel={moreLabel}
        hideMore={!p.calls.hasNextPage}
        moreDisabled={p.calls.isFetchingNextPage}
        onMore={() => void p.calls.fetchNextPage()}
        onListClick={onListClick}
        selectedCall={idx >= 0 ? p.callRows[idx] : undefined}
        selectedQuery={p.selectedQuery}
        keyNames={p.keyNames}
        hasPrev={idx > 0}
        hasNext={idx >= 0 && idx < p.callRows.length - 1}
        onPrev={() => idx > 0 && p.onSelectCall(p.callRows[idx - 1]?.id)}
        onNext={() => idx >= 0 && idx < p.callRows.length - 1 && p.onSelectCall(p.callRows[idx + 1]?.id)}
        onClose={() => p.onSelectCall(undefined)}
        t={p.t}
      />
    );
  }
  return (
    <Logs
      t={(k) => logsZh[k]}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        unbuilt: true,
        tableLabel: p.t("callsBoard.list.tableLabel"),
        hideMore: !p.calls.hasNextPage,
        moreDisabled: p.calls.isFetchingNextPage,
        onMore: () => void p.calls.fetchNextPage(),
        onListClick,
      }}
      rows={{ summaryItems0: [], headers0: headers, rows0: rows }}
      text={{ heading: "", moreLabel }}
    />
  );
}

function CallsListBody(p: CallsListBodyProps) {
  if (p.calls.isPending || p.calls.isError) {
    return (
      <CallsQueryState
        t={p.t}
        error={p.calls.error}
        isPending={p.calls.isPending}
        onRetry={() => void p.calls.refetch()}
      />
    );
  }
  if (p.callRows.length === 0) {
    return (
      <CallsCardNotice
        message={p.filtered ? p.t("callsBoard.list.emptyFiltered") : p.t("callsBoard.list.emptyTitle")}
        actionLabel={p.filtered ? p.t("callsBoard.filter.clear") : undefined}
        onAction={p.filtered ? p.onClear : undefined}
      />
    );
  }
  return <CallsListTable {...p} />;
}

function useCallsPageQueries(search: CallsSearch, searchStr: string, authenticated: boolean, canKeys: boolean) {
  const router = useRouter();
  const { group_by, tab, call: selectedId, ...filters } = search;
  const groupBy: CallGroupBy = group_by ?? "day";
  const sessionsTab = tab === "sessions";
  const calls = useInfiniteQuery({ ...callsQuery(filters), enabled: !sessionsTab });
  const sessions = useInfiniteQuery({ ...callSessionsQuery(filters), enabled: sessionsTab });
  const report = useQuery(callAggregateQuery(filters, groupBy));
  const selectedQuery = useQuery({ ...callQuery(selectedId ?? ""), enabled: Boolean(selectedId) });
  const models = useQuery({ ...modelsQuery({}), enabled: authenticated });
  const keys = useInfiniteQuery({ ...keysQuery({}), enabled: canKeys });
  const workspaces = useQuery({ ...workspacesQuery, enabled: canKeys });
  const exported = useCallsExport(filters);
  const callRows = useMemo(() => calls.data?.pages.flatMap((p) => p.data) ?? [], [calls.data]);
  const keyList = useMemo(() => keys.data?.pages.flatMap((p) => p.data) ?? [], [keys.data]);
  const keyNames = useMemo(() => new Map(keyList.map((k) => [k.id, k.name || k.id])), [keyList]);
  useEffect(() => {
    const ready = sessionsTab ? sessions.isSuccess : calls.isSuccess;
    if (!ready) return;
    const saved = takeCallsScroll(searchStr);
    if (saved === null || saved <= 0) return;
    let cancelled = false;
    const restore = () => {
      requestAnimationFrame(() => {
        if (!cancelled) {
          requestAnimationFrame(() => {
            if (!cancelled) window.scrollTo(0, saved);
          });
        }
      });
    };
    restore();
    const unsub = router.subscribe("onRendered", restore);
    const timer = setTimeout(unsub, 500);
    return () => {
      cancelled = true;
      clearTimeout(timer);
      unsub();
    };
  }, [calls.isSuccess, sessions.isSuccess, sessionsTab, searchStr, router]);
  return {
    filters,
    group_by,
    groupBy,
    tab,
    sessionsTab,
    selectedId,
    calls,
    sessions,
    report,
    selectedQuery,
    models,
    keyList,
    keyNames,
    workspaces,
    exported,
    callRows,
  };
}

function CallsResults({
  t,
  locale,
  phone,
  search,
  searchStr,
  q,
  setSearch,
  clearAll,
  onOpenCall,
}: {
  t: TFunction;
  locale: string;
  phone: boolean;
  search: CallsSearch;
  searchStr: string;
  q: ReturnType<typeof useCallsPageQueries>;
  setSearch: (patch: Partial<CallsSearch>, replace?: boolean) => void;
  clearAll: () => void;
  onOpenCall: (callId: string) => void;
}) {
  const unsupported = !q.sessionsTab && filtersUnsupported(q.filters, q.calls.data?.pages ?? []);
  const filtered = hasActiveCallFilters(q.filters);
  const unsupportedName = q.filters.fell_back ? t("callsBoard.filter.statusFallback") : t("callsBoard.filter.status");
  return (
    <>
      {!q.sessionsTab ? (
        <SummarySection
          t={t}
          report={q.report}
          loadedCalls={q.callRows}
          groupBy={q.groupBy}
          fellBackActive={Boolean(q.filters.fell_back)}
          keyNames={q.keyNames}
          onGroupBy={(next) => setSearch({ group_by: next })}
        />
      ) : null}
      {unsupported ? (
        <CallsAlertBanner
          lead={t("callsBoard.list.unsupportedTitle")}
          body={t("callsBoard.list.unsupportedBody", { filter: unsupportedName })}
          actionLabel={t("callsBoard.filter.clear")}
          onAction={clearAll}
        />
      ) : null}
      {q.sessionsTab ? (
        <SessionsSection
          t={t}
          sessions={q.sessions}
          filtered={filtered}
          onClear={clearAll}
          onPickClient={(c) => setSearch({ tab: undefined, client_app: c || undefined, call: undefined })}
        />
      ) : (
        <CallsListBody
          t={t}
          locale={locale}
          phone={phone}
          calls={q.calls}
          callRows={q.callRows}
          selectedId={q.selectedId}
          selectedQuery={q.selectedQuery}
          keyNames={q.keyNames}
          filtered={filtered}
          onClear={clearAll}
          onSelectCall={(call) => setSearch({ call }, true)}
          onOpenCall={(id) => {
            saveLastCallsSearch(search);
            saveCallsScroll(searchStr, window.scrollY);
            onOpenCall(id);
          }}
        />
      )}
    </>
  );
}

export default function Calls() {
  const { t, i18n } = useTranslation();
  const session = useSession();
  const phone = usePhoneWidth();
  const navigate = useNavigate();
  const searchStr = useLocation({ select: (l) => l.searchStr });
  const search: CallsSearch = useSearch({ from: "/app/calls" });
  const q = useCallsPageQueries(search, searchStr, Boolean(session.account), can(session, "api_keys.view"));
  usePageTitle(t("callsBoard.title"));
  const setSearch = (patch: Partial<CallsSearch>, replace = false) =>
    void navigate({ to: "/calls", search: cleanSearchFilters({ ...search, ...patch }), replace, resetScroll: false });
  const clearAll = () =>
    void navigate({ to: "/calls", search: cleanSearchFilters({ tab: q.tab }), resetScroll: false });
  return (
    <CallsPageShell
      phone={phone}
      heading={t("callsBoard.title")}
      headerActions={
        <CallsHeaderActions
          t={t}
          sessionsTab={q.sessionsTab}
          exportBusy={q.exported.busy}
          onTab={(next) => setSearch({ tab: next, call: undefined })}
          onExport={q.exported.start}
        />
      }
    >
      <ExportNotice
        t={t}
        error={q.exported.failed}
        onUse7d={() => {
          q.exported.dismiss();
          setSearch(dateRangeForPreset("d7"));
        }}
        onDismiss={q.exported.dismiss}
      />
      <CallsFilterBar
        t={t}
        filters={q.filters}
        models={q.models.data?.data ?? []}
        keys={q.keyList}
        workspaces={q.workspaces.data?.data ?? []}
        onApply={(next) => setSearch({ ...next, group_by: q.group_by, tab: q.tab })}
      />
      <CallsResults
        t={t}
        locale={i18n.resolvedLanguage ?? i18n.language}
        phone={phone}
        search={search}
        searchStr={searchStr}
        q={q}
        setSearch={setSearch}
        clearAll={clearAll}
        onOpenCall={(callId) => void navigate({ to: "/calls/$callId", params: { callId } })}
      />
    </CallsPageShell>
  );
}
