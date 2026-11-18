import { useErrors, useSessionBound } from "@axiom/app";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useState } from "react";
import { callsCsv, callsName } from "../calls";
import type { CallFilters } from "../queries";
import { save } from "../statement";
import { classifyExportError } from "./calls-data";
import { CallsAlertBanner, CallsCardNotice } from "./calls-states";

export function useCallsExport(filters: CallFilters) {
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

export function ExportNotice({
  t,
  error,
  filters,
  onUse7d,
  onDismiss,
}: {
  t: TFunction;
  error: unknown;
  filters: CallFilters;
  onUse7d: () => void;
  onDismiss: () => void;
}) {
  const errors = useErrors();
  if (!error) return null;
  const kind = classifyExportError(error, filters);
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
  const scopeText =
    filters.since && filters.until
      ? t("callsBoard.export.scopeRange", {
          since: format.dateTime(new Date(filters.since)),
          until: format.dateTime(new Date(filters.until)),
        })
      : filters.since
        ? t("callsBoard.export.scopeSince", { since: format.dateTime(new Date(filters.since)) })
        : filters.until
          ? t("callsBoard.export.scopeUntil", { until: format.dateTime(new Date(filters.until)) })
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
