import { ApiError, type Schemas } from "@axiom/api";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import DesktopAttempts from "@axiom/boards/AxApp/regions/LogsAttempts";
import attemptsZh from "@axiom/boards/AxApp/regions/LogsAttempts.zh";
import PhoneAttempts from "@axiom/boards/AxAppPhone/regions/LogsAttempts";
import { useFormat } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { computeTimeline, formatSeconds, modelDotColor } from "./calls-data";
import { buildChargeCells, buildDetailSteps, buildIdCells, buildSpeedMetrics } from "./calls-detail-rows";
import { type HeaderItem, type ListRowItem, statusLabelAndClass } from "./calls-rows";
import { CallsAlertBanner, CallsQueryState } from "./calls-states";

function copyFromClick(event: React.MouseEvent, onCopied: () => void) {
  const code = (event.target as Element).closest("code");
  const text = code?.textContent?.trim();
  if (!text || text === "—") return;
  void globalThis.navigator?.clipboard?.writeText(text).then(onCopied, () => {});
}

function useCopiedFlash() {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1600);
    return () => clearTimeout(id);
  }, [copied]);
  return { copied, trigger: () => setCopied(true) };
}

function detailContent(
  call: Schemas["Call"],
  keyNames: Map<string, string>,
  copied: boolean,
  onCopied: () => void,
  t: TFunction,
  format: ReturnType<typeof useFormat>,
) {
  const st = statusLabelAndClass(call, t);
  const tl = computeTimeline(call);
  const subParts = [
    format.dateTime(new Date(call.at)),
    keyNames.get(call.key_id) ?? call.key_id,
    call.client_app || "",
    copied ? t("callsBoard.detail.copied") : "",
  ].filter(Boolean);
  return {
    values: {
      statusBadgeStyle: st.badge,
      statusDotStyle: st.dot,
      onCopy: (e: React.SyntheticEvent) => copyFromClick(e as unknown as React.MouseEvent, onCopied),
    },
    rows: {
      steps0: buildDetailSteps(call, format, t),
      chargeCells0: buildChargeCells(call, format, t),
      speedMetrics0: buildSpeedMetrics(call, format, t),
      idCells0: buildIdCells(call, t),
    },
    text: {
      heading: "",
      detailModel: call.model || t("callsBoard.states.dash"),
      detailStatus: st.text,
      detailSub: subParts.join(" · "),
      attemptsTitle: t("callsBoard.detail.sections.attempts"),
      billingTitle: t("callsBoard.detail.sections.billing"),
      speedTitle: t("callsBoard.detail.sections.speed"),
      idsTitle: `${t("callsBoard.detail.sections.ids")} / ${t("callsBoard.detail.sections.meta")}`,
      tlStart: `0${t("callsBoard.states.secSuffix")}`,
      tlEnd: `${tl.scaleSec}${t("callsBoard.states.secSuffix")}`,
      modelDotStyle: `background: ${modelDotColor(call.model)};`,
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
  const flash = useCopiedFlash();
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
  const content = detailContent(call, keyNames, flash.copied, flash.trigger, t, format);
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

function emptyDetailContent(message: string, t: TFunction) {
  return {
    values: { hideAttempts: true, hideBilling: true, hideTimeline: true },
    rows: {
      steps0: [],
      chargeCells0: [],
      speedMetrics0: [{ key: "st", kind: "0" as const, f0: t("callsBoard.col.status"), f1: message }],
      idCells0: [],
    },
    text: {
      heading: "",
      detailModel: message,
      detailStatus: formatSeconds(undefined, "", t("callsBoard.states.dash")),
      detailSub: "",
      attemptsTitle: "",
      billingTitle: "",
      speedTitle: "",
      idsTitle: "",
      tlStart: "",
      tlEnd: "",
      modelDotStyle: "",
      failBarStyle: "",
      waitBarStyle: "",
      genBarStyle: "",
    },
  };
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

function resolveDetailContent(
  p: SplitProps,
  copied: boolean,
  onCopied: () => void,
  format: ReturnType<typeof useFormat>,
) {
  const call = p.selectedQuery.data ?? p.selectedCall;
  if (call) return detailContent(call, p.keyNames, copied, onCopied, p.t, format);
  const missing = p.selectedQuery.error instanceof ApiError && p.selectedQuery.error.status === 404;
  const msg = p.selectedQuery.isPending
    ? p.t("callsBoard.states.loading")
    : missing
      ? p.t("callsBoard.states.notFoundTitle")
      : p.t("callsBoard.states.errorTitle");
  return emptyDetailContent(msg, p.t);
}

function PhoneList({
  headers,
  rows,
  moreLabel,
  hideMore,
  moreDisabled,
  onMore,
  onListClick,
  tableLabel,
}: {
  headers: HeaderItem[];
  rows: ListRowItem[];
  moreLabel: string;
  hideMore: boolean;
  moreDisabled: boolean;
  onMore: () => void;
  onListClick: (e: React.SyntheticEvent) => void;
  tableLabel: string;
}) {
  return (
    <Logs
      t={(k) => logsZh[k]}
      values={{
        hideHeader: true,
        hideFilters: true,
        hideSummary: true,
        unbuilt: true,
        tableLabel,
        hideMore,
        moreDisabled,
        onMore,
        onListClick,
      }}
      rows={{ summaryItems0: [], headers0: headers, rows0: rows }}
      text={{ heading: "", moreLabel }}
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

export function InlineCallSplit(p: SplitProps) {
  const format = useFormat();
  const flash = useCopiedFlash();
  const content = resolveDetailContent(p, flash.copied, flash.trigger, format);
  const nav = splitNavValues(p);
  if (p.phone) {
    return (
      <>
        <PhoneList
          headers={p.headers}
          rows={p.rows}
          moreLabel={p.moreLabel}
          hideMore={p.hideMore}
          moreDisabled={p.moreDisabled}
          onMore={p.onMore}
          onListClick={p.onListClick}
          tableLabel={p.t("callsBoard.list.tableLabel")}
        />
        <PhoneAttempts
          t={(k) => attemptsZh[k]}
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
