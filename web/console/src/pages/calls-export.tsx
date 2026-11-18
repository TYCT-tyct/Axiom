import { useErrors, useSessionBound } from "@axiom/app";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useState } from "react";
import { callsCsv, callsName } from "../calls";
import type { CallFilters } from "../queries";
import { save } from "../statement";
import { classifyExportError, type ExportErrorKind } from "./calls-data";
import { CallsAlertBanner, CallsCardNotice } from "./calls-states";

type ExportFailure = { error: unknown; kind: ExportErrorKind };

export function useCallsExport(filters: CallFilters) {
  const bound = useSessionBound();
  const [failure, setFailure] = useState<ExportFailure | null>(null);
  const [busy, setBusy] = useState(false);
  const start = () => {
    setBusy(true);
    setFailure(null);
    void bound((signal) => callsCsv(filters, signal))
      .then((ans) => {
        if (ans) save(new File([ans.value], callsName(), { type: "text/csv" }));
      })
      .catch((error: unknown) => setFailure({ error, kind: classifyExportError(error, filters) }))
      .finally(() => setBusy(false));
  };
  return { failure, busy, start, dismiss: () => setFailure(null) };
}

export function ExportNotice({
  t,
  failure,
  onUse7d,
  onDismiss,
}: {
  t: TFunction;
  failure: ExportFailure | null;
  onUse7d: () => void;
  onDismiss: () => void;
}) {
  const errors = useErrors();
  if (!failure) return null;
  const { error, kind } = failure;
  if (kind === "rows_limit" || kind === "window_limit") {
    return (
      <CallsAlertBanner
        lead={t(kind === "rows_limit" ? "callsBoard.export.rowsLimitTitle" : "callsBoard.export.windowLimitTitle")}
        body={t("callsBoard.export.limitBody")}
        actionLabel={t("callsBoard.export.use7d")}
        onAction={onUse7d}
      />
    );
  }
  if (kind === "invalid_field") {
    return (
      <CallsAlertBanner
        lead={t("callsBoard.export.invalidParamsTitle")}
        body={t("callsBoard.export.invalidParamsBody", { reason: errors.message(error) })}
        actionLabel={t("callsBoard.export.dismiss")}
        onAction={onDismiss}
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

export function ExportScopeNote({ t, filters }: { t: TFunction; filters: CallFilters }) {
  const format = useFormat();
  const boundText = (value?: string) => {
    if (!value) return undefined;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : format.dateTime(date);
  };
  const since = boundText(filters.since);
  const until = boundText(filters.until);
  const scopeText =
    since && until
      ? t("callsBoard.export.scopeRange", { since, until })
      : since
        ? t("callsBoard.export.scopeSince", { since })
        : until
          ? t("callsBoard.export.scopeUntil", { until })
          : t("callsBoard.export.scopeDefault");
  return (
    <>
      {filters.fell_back ? <CallsCardNotice message={t("callsBoard.export.fallbackIgnored")} /> : null}
      <Logs
        t={(k) => logsZh[k]}
        values={{
          hideHeader: true,
          hideFilters: true,
          hideList: true,
          unbuilt: true,
          summaryStyle: "margin-bottom: 8px;",
        }}
        rows={{ summaryItems0: [{ key: "exp-scope", kind: "1", f0: scopeText, f1: "" }], headers0: [], rows0: [] }}
        text={{ heading: "", moreLabel: "" }}
      />
    </>
  );
}
