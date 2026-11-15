import { useTranslation } from "react-i18next";
import { useCopy } from "./copy-button";
import s from "./request-id.module.css";

// RequestId shows the server's ID of a failed request, which finds it in the server's log, and copies it. A refused
// copy says so, and the ID stays selectable by hand.
export function RequestId({ id }: { id: string }) {
  const { t } = useTranslation("common");
  const [state, copy] = useCopy(id);
  return (
    <span className={s.root}>
      {t("failed.requestId")} <code className={s.id}>{id}</code>
      <button type="button" className={s.copy} onClick={copy}>
        {state === "copied" ? t("copy.copied") : t("copy.copy")}
      </button>
      <span role="status">{state === "failed" ? t("copy.failed") : ""}</span>
    </span>
  );
}
