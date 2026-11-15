import { ApiError } from "@axiom/api";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { Glyph } from "./glyph";
import { RequestId } from "./request-id";
import s from "./submit-failed.module.css";

// SubmitFailed says a write did not go through: why, in the words for its error code, and what to do next, which by
// default is that what was typed is still there to send again (the form keeps it). With the request ID when the
// server answered with one.
export function SubmitFailed({ error, next }: { error: unknown; next?: string }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const requestId = error instanceof ApiError ? error.requestId : null;
  return (
    <div role="alert" className={s.root}>
      <span className={s.icon}>
        <Glyph name="warn" />
      </span>
      <div className={s.text}>
        <strong className={s.title}>{t("failed.title")}</strong>
        <span>
          {errors.message(error)} {next ?? t("failed.kept")}
        </span>
        {requestId ? (
          <span className={s.id}>
            <RequestId id={requestId} />
          </span>
        ) : null}
      </div>
    </div>
  );
}
