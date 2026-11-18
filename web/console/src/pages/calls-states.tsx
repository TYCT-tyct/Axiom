import { ApiError } from "@axiom/api";
import { useErrors } from "@axiom/app";
import Logs from "@axiom/boards/AxApp/regions/Logs";
import logsZh from "@axiom/boards/AxApp/regions/Logs.zh";
import LogsEmpty from "@axiom/boards/AxApp/regions/LogsEmpty";
import emptyZh from "@axiom/boards/AxApp/regions/LogsEmpty.zh";
import LogsFail from "@axiom/boards/AxApp/regions/LogsFail";
import failZh from "@axiom/boards/AxApp/regions/LogsFail.zh";
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useState } from "react";

const bareErrDetailStyle = "background: transparent; box-shadow: none; margin: 0; border-radius: 0; max-width: none;";

export function CallsAlertBanner({
  lead,
  body,
  actionLabel,
  onAction,
  role = "alert",
}: {
  lead: string;
  body: string;
  actionLabel?: string;
  onAction?: () => void;
  role?: "alert" | "status";
}) {
  return (
    <LogsFail
      t={(k) => failZh[k]}
      values={{
        onlyErr: true,
        unbuilt: true,
        hideList: true,
        hideScrim: true,
        wrapStyle: "grid-template-columns: minmax(0, 1fr); margin-bottom: 12px;",
        detailStyle: bareErrDetailStyle,
        errSecStyle: "padding: 0; box-shadow: none;",
        errRole: role,
        hideErrBtn: !actionLabel || !onAction,
        onErrBtn: onAction,
      }}
      text={{
        errLead: lead,
        errBody: body,
        errBtnLabel: actionLabel ?? "",
      }}
    />
  );
}

export function CallsCardNotice({
  message,
  actionLabel,
  onAction,
  role = "status",
}: {
  message: string;
  actionLabel?: string;
  onAction?: () => void;
  role?: "status" | "alert";
}) {
  return (
    <LogsEmpty
      t={(k) => emptyZh[k]}
      values={{
        onlyEmpty: true,
        unbuilt: true,
        emptyRole: role,
        hideClear: !actionLabel || !onAction,
        onClear: onAction,
      }}
      text={{
        heading: "",
        emptyMessage: message,
        clearLabel: actionLabel ?? "",
      }}
    />
  );
}

export function CallsQueryState({
  t,
  error,
  isPending,
  onRetry,
  deniedTitle,
  deniedBody,
}: {
  t: TFunction;
  error: unknown;
  isPending: boolean;
  onRetry: () => void;
  deniedTitle?: string;
  deniedBody?: string;
}) {
  const errors = useErrors();
  if (isPending) {
    return <CallsCardNotice message={t("callsBoard.states.loading")} />;
  }
  if (error instanceof ApiError && error.status === 403) {
    return (
      <CallsAlertBanner
        lead={deniedTitle ?? t("callsBoard.states.deniedTitle")}
        body={deniedBody ?? t("callsBoard.states.deniedBody")}
      />
    );
  }
  const detail = error ? errors.message(error) : "";
  return (
    <CallsAlertBanner
      lead={t("callsBoard.states.errorTitle")}
      body={detail || t("callsBoard.states.errorBody")}
      actionLabel={t("callsBoard.states.retry")}
      onAction={onRetry}
    />
  );
}

export function segClickIndex(e: React.SyntheticEvent) {
  const btn = (e.target as Element).closest("button");
  return btn?.parentElement ? [...btn.parentElement.querySelectorAll("button")].indexOf(btn) : -1;
}

export function usePhoneWidth() {
  const [phone, setPhone] = useState(() =>
    typeof window !== "undefined" ? window.matchMedia("(max-width: 819px)").matches : false,
  );
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 819px)");
    const update = () => setPhone(mq.matches);
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);
  return phone;
}

export function CallsPageShell({
  phone,
  heading,
  headerActions,
  children,
}: {
  phone: boolean;
  heading: string;
  headerActions: ReactNode;
  children: ReactNode;
}) {
  const headerPageStyle = phone
    ? "display: block; width: 100%; box-sizing: border-box; padding: 14px 16px 0; max-width: none;"
    : "display: block; width: 100%; box-sizing: border-box; max-width: 1180px; margin: 0 auto; padding: 30px 40px 0;";
  const bodyPageStyle = phone
    ? "display: block; width: 100%; box-sizing: border-box; padding: 0 16px 120px; max-width: none;"
    : "display: block; width: 100%; box-sizing: border-box; max-width: 1180px; margin: 0 auto; padding: 0 40px 96px;";
  return (
    <>
      <Logs
        t={(k) => logsZh[k]}
        values={{ pageStyle: headerPageStyle, onlyHeader: true, unbuilt: true }}
        rows={{ summaryItems0: [], headers0: [], rows0: [] }}
        text={{ heading, moreLabel: "" }}
        pageActions={headerActions}
      />
      <Logs
        t={(k) => logsZh[k]}
        values={{
          pageStyle: bodyPageStyle,
          onlyHeader: true,
          hideTitle: true,
          headerStyle: "display: block; margin-bottom: 0; min-height: 0;",
          actionsStyle: "display: block; width: 100%; margin: 0; min-height: 0;",
          unbuilt: true,
        }}
        rows={{ summaryItems0: [], headers0: [], rows0: [] }}
        text={{ heading: "", moreLabel: "" }}
        pageActions={children}
      />
    </>
  );
}
