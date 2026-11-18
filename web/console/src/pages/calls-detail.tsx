import { ApiError, type Schemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import DesktopAttempts from "@axiom/boards/AxApp/regions/LogsAttempts";
import attemptsZh from "@axiom/boards/AxApp/regions/LogsAttempts.zh";
import DesktopFail from "@axiom/boards/AxApp/regions/LogsFail";
import failZh from "@axiom/boards/AxApp/regions/LogsFail.zh";
import PhoneAttempts from "@axiom/boards/AxAppPhone/regions/LogsAttempts";
import PhoneFail from "@axiom/boards/AxAppPhone/regions/LogsFail";
import { useFormat } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { computeTimeline, modelDotColor } from "./calls-data";
import { buildChargeCells, buildDetailSteps, buildIdCells, buildSpeedMetrics } from "./calls-detail-rows";
import { type HeaderItem, type ListRowItem, statusLabelAndClass } from "./calls-rows";
import { CallBoardLink, CallsAlertBanner, CallsQueryState } from "./calls-states";

function detailHeaderFields(
  call: Schemas["Call"] | undefined,
  keyNames: Map<string, string>,
  fallbackTitle: string,
  t: TFunction,
  format: ReturnType<typeof useFormat>,
) {
  const dash = t("callsBoard.states.dash");
  if (!call) {
    return {
      statusBadgeStyle: "",
      statusDotStyle: "",
      detailModel: fallbackTitle,
      detailStatus: dash,
      detailSub: "",
      modelDotStyle: "",
    };
  }
  const st = statusLabelAndClass(call, t);
  const subParts = [
    format.dateTime(new Date(call.at)),
    keyNames.get(call.key_id) ?? call.key_id,
    call.client_app || "",
  ];
  return {
    statusBadgeStyle: st.badge,
    statusDotStyle: st.dot,
    detailModel: call.model || dash,
    detailStatus: st.text,
    detailSub: subParts.filter(Boolean).join(" · "),
    modelDotStyle: `background: ${modelDotColor(call.model)};`,
  };
}

function detailContent(
  call: Schemas["Call"],
  keyNames: Map<string, string>,
  t: TFunction,
  format: ReturnType<typeof useFormat>,
) {
  const hdr = detailHeaderFields(call, keyNames, "", t, format);
  const tl = computeTimeline(call);
  return {
    values: {
      statusBadgeStyle: hdr.statusBadgeStyle,
      statusDotStyle: hdr.statusDotStyle,
    },
    rows: {
      steps0: buildDetailSteps(call, format, t),
      chargeCells0: buildChargeCells(call, format, t),
      speedMetrics0: buildSpeedMetrics(call, format, t),
      idCells0: buildIdCells(call, t),
    },
    text: {
      heading: "",
      detailModel: hdr.detailModel,
      detailStatus: hdr.detailStatus,
      detailSub: hdr.detailSub,
      attemptsTitle: t("callsBoard.detail.sections.attempts"),
      billingTitle: t("callsBoard.detail.sections.billing"),
      speedTitle: t("callsBoard.detail.sections.speed"),
      idsTitle: `${t("callsBoard.detail.sections.ids")} / ${t("callsBoard.detail.sections.meta")}`,
      tlStart: `0${t("callsBoard.states.secSuffix")}`,
      tlEnd: `${tl.scaleSec}${t("callsBoard.states.secSuffix")}`,
      modelDotStyle: hdr.modelDotStyle,
      failBarStyle: `width: ${tl.failPct}%;`,
      waitBarStyle: `width: ${tl.waitPct}%;`,
      genBarStyle: `width: ${tl.genPct}%;`,
    },
  };
}

export function StandaloneCallDetail({
  query,
  keyNames,
  t,
}: {
  query: UseQueryResult<Schemas["Call"], Error>;
  keyNames: Map<string, string>;
  t: TFunction;
}) {
  const format = useFormat();
  if (query.isPending || query.isError) {
    if (query.error instanceof ApiError && query.error.status === 404) {
      return (
        <CallsAlertBanner
          lead={t("callsBoard.states.notFoundTitle")}
          body={t("callsBoard.states.notFoundBody")}
          role="status"
        />
      );
    }
    return (
      <CallsQueryState t={t} error={query.error} isPending={query.isPending} onRetry={() => void query.refetch()} />
    );
  }
  const call = query.data;
  const content = detailContent(call, keyNames, t, format);
  return (
    <>
      {call.error_kind !== "" ? (
        <CallsAlertBanner
          lead={t("callsBoard.detail.failedTitle")}
          body={t("callsBoard.detail.failedBody", { reason: call.error_kind })}
          role="status"
        />
      ) : null}
      <DesktopAttempts
        t={(k) => attemptsZh[k]}
        link={CallBoardLink}
        values={{
          ...content.values,
          onlyBody: true,
          unbuilt: true,
          hideList: true,
          hideScrim: true,
          hideNav: true,
          wrapStyle: "grid-template-columns: minmax(0, 1fr);",
          detailStyle: "position: static; max-width: none; max-height: none; overflow: visible;",
        }}
        rows={{ headers0: [], rows0: [], ...content.rows }}
        text={{ ...content.text, moreLabel: "" }}
      />
    </>
  );
}

export type SplitProps = {
  phone: boolean;
  headers: HeaderItem[];
  rows: ListRowItem[];
  moreLabel: string;
  hideMore: boolean;
  moreDisabled: boolean;
  onMore: () => void;
  onListClick: (e: React.SyntheticEvent) => void;
  selectedCall?: Schemas["Call"];
  selectedQuery: UseQueryResult<Schemas["Call"], Error>;
  keyNames: Map<string, string>;
  hasPrev: boolean;
  hasNext: boolean;
  onPrev: () => void;
  onNext: () => void;
  onClose: () => void;
  t: TFunction;
};

type FailRowItem = Parameters<typeof DesktopFail>[0]["rows"]["rows0"][number];

function toFailRows(rows: ListRowItem[]): FailRowItem[] {
  return rows.map((r) =>
    r.kind === "0" ? { key: r.key, kind: "0", f0: r.f0, f1: r.f2 ? `${r.f1} ${r.f2}` : r.f1, f2: r.f3 } : r,
  );
}

type CallsListTableProps = Pick<
  SplitProps,
  "headers" | "rows" | "moreLabel" | "hideMore" | "moreDisabled" | "onMore" | "onListClick" | "t"
>;

export function CallsBoardList(p: CallsListTableProps) {
  return (
    <Logs
      t={(k) => logsZh[k]}
      link={CallBoardLink}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        unbuilt: true,
        tableLabel: p.t("callsBoard.list.tableLabel"),
        hideMore: p.hideMore,
        moreDisabled: p.moreDisabled,
        onMore: p.onMore,
        onListClick: p.onListClick,
      }}
      rows={{ summaryItems0: [], headers0: p.headers, rows0: p.rows }}
      text={{ heading: "", moreLabel: p.moreLabel }}
    />
  );
}

function splitNavValues(p: SplitProps) {
  return {
    prevDisabled: !p.hasPrev,
    nextDisabled: !p.hasNext,
    prevLabel: p.t("callsBoard.detail.prev"),
    nextLabel: p.t("callsBoard.detail.next"),
    closeLabel: p.t("callsBoard.detail.close"),
    onPrev: p.onPrev,
    onNext: p.onNext,
    onClose: p.onClose,
  };
}

function splitErrorState(p: SplitProps, errorMessage: (err: unknown) => string) {
  if (p.selectedQuery.isPending) {
    return {
      lead: p.t("callsBoard.states.loading"),
      body: "",
      role: "status" as const,
      hideBtn: true,
    };
  }
  const missing = p.selectedQuery.error instanceof ApiError && p.selectedQuery.error.status === 404;
  if (missing) {
    return {
      lead: p.t("callsBoard.states.notFoundTitle"),
      body: p.t("callsBoard.states.notFoundBody"),
      role: "status" as const,
      hideBtn: true,
    };
  }
  return {
    lead: p.t("callsBoard.states.errorTitle"),
    body: errorMessage(p.selectedQuery.error) || p.t("callsBoard.states.errorBody"),
    role: "alert" as const,
    hideBtn: false,
  };
}

function buildUnavailableProps(
  p: SplitProps,
  format: ReturnType<typeof useFormat>,
  errors: ReturnType<typeof useErrors>,
) {
  const errState = splitErrorState(p, errors.message);
  const summary = p.selectedCall;
  const hdr = detailHeaderFields(summary, p.keyNames, errState.lead, p.t, format);
  return {
    values: {
      unbuilt: true,
      tableLabel: p.t("callsBoard.list.tableLabel"),
      onListClick: p.onListClick,
      statusBadgeStyle: hdr.statusBadgeStyle,
      statusDotStyle: hdr.statusDotStyle,
      errRole: errState.role,
      hideErrBtn: errState.hideBtn,
      onErrBtn: () => void p.selectedQuery.refetch(),
      hideAttempts: true,
      hideSpeed: !summary,
      hideIds: !summary,
      ...splitNavValues(p),
    },
    rows: {
      headers0: p.headers,
      rows0: p.phone ? [] : toFailRows(p.rows),
      speedMetrics0: summary ? buildSpeedMetrics(summary, format, p.t) : [],
      idCells0: summary ? buildIdCells(summary, p.t) : [],
    },
    text: {
      heading: "",
      detailModel: hdr.detailModel,
      detailStatus: hdr.detailStatus,
      detailSub: hdr.detailSub,
      speedTitle: p.t("callsBoard.detail.sections.speed"),
      idsTitle: `${p.t("callsBoard.detail.sections.ids")} / ${p.t("callsBoard.detail.sections.meta")}`,
      errLead: errState.lead,
      errBody: errState.body,
      errBtnLabel: p.t("callsBoard.states.retry"),
      modelDotStyle: hdr.modelDotStyle,
    },
  };
}

function SplitUnavailableDetail(p: SplitProps) {
  const format = useFormat();
  const errors = useErrors();
  const view = buildUnavailableProps(p, format, errors);
  if (p.phone) {
    return (
      <>
        <CallsBoardList {...p} />
        <PhoneFail
          t={(k) => failZh[k]}
          link={CallBoardLink}
          values={{
            hidePage: true,
            hideList: true,
            scrimStyle: "position: fixed; inset: 0; z-index: 40;",
            detailStyle: "position: fixed; left: 0; right: 0; bottom: 0; top: auto; max-height: 80vh; z-index: 41;",
            ...view.values,
          }}
          rows={view.rows}
          text={view.text}
        />
      </>
    );
  }
  return (
    <DesktopFail
      t={(k) => failZh[k]}
      link={CallBoardLink}
      values={{ onlyBody: true, hideScrim: true, ...view.values }}
      rows={view.rows}
      text={view.text}
    />
  );
}

export function InlineCallSplit(p: SplitProps) {
  const format = useFormat();
  if (!p.selectedQuery.isSuccess) {
    return <SplitUnavailableDetail {...p} />;
  }
  const content = detailContent(p.selectedQuery.data, p.keyNames, p.t, format);
  const nav = splitNavValues(p);
  if (p.phone) {
    return (
      <>
        <CallsBoardList {...p} />
        <PhoneAttempts
          t={(k) => attemptsZh[k]}
          link={CallBoardLink}
          values={{
            hidePage: true,
            unbuilt: true,
            scrimStyle: "position: fixed; inset: 0; z-index: 40;",
            detailStyle: "position: fixed; left: 0; right: 0; bottom: 0; top: auto; max-height: 80vh; z-index: 41;",
            ...nav,
            ...content.values,
          }}
          rows={{ headers0: [], rows0: [], ...content.rows }}
          text={{ ...content.text, moreLabel: p.moreLabel }}
        />
      </>
    );
  }
  return (
    <DesktopAttempts
      t={(k) => attemptsZh[k]}
      link={CallBoardLink}
      values={{
        onlyBody: true,
        unbuilt: true,
        hideScrim: true,
        hideMore: p.hideMore,
        moreDisabled: p.moreDisabled,
        onMore: p.onMore,
        onListClick: p.onListClick,
        tableLabel: p.t("callsBoard.list.tableLabel"),
        ...nav,
        ...content.values,
      }}
      rows={{ headers0: p.headers, rows0: p.rows, ...content.rows }}
      text={{ ...content.text, moreLabel: p.moreLabel }}
    />
  );
}
