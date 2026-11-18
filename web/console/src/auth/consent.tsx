import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { Gate, useErrors, useSession } from "@axiom/app";
import LoginConsent from "@axiom/boards/Login/regions/LoginConsent";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { canSubmitWithLegal, LegalDocuments, useLegalConsent } from "./legal";
import { FormProblem, LoginFrame, NoteAction, OtherAccount } from "./login-frame";

// A member whose accepted versions are behind the site's current ones lands here, outside the shell: every shell page
// would be refused with consent_required until this succeeds.
export default function Consent() {
  return (
    <Gate stage="consent">
      <ConsentStep />
    </Gate>
  );
}

function ConsentStep() {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  const errors = useErrors();
  const session = useSession();
  const legal = useLegalConsent();
  const client = useQueryClient();
  const accept = useMutation({
    mutationFn: (body: Schemas["LegalAcceptance"]) =>
      unwrapEmpty(consoleApi.POST("/api/console/v1/legal/accept", { body })),
    onSuccess: () => client.invalidateQueries({ queryKey: ["session"] }),
    onError: (error) => {
      if (error instanceof ApiError && error.code === "consent_required") {
        legal.reload();
      }
    },
  });
  const missing = legal.query.isSuccess && legal.documents.length === 0;
  const failed = legal.query.isError
    ? legal.query.error instanceof ApiError && legal.query.error.status === 403
      ? common("state.forbiddenTitle")
      : t("legal.error")
    : "";
  // The box is the board's own and keeps its state: a new version on screen starts it unticked again.
  const versions = JSON.stringify(legal.versions ?? []);
  return (
    <LoginFrame
      title={t("legal.title")}
      sub={t("legal.description", { email: session.user.email })}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        if (!accept.isPending && canSubmitWithLegal(legal) && !missing) {
          accept.mutate({ consent: legal.versions ?? [] });
        }
      }}
    >
      {legal.query.isPending ? (
        <LoginNote t={String}>
          <span role="status">{t("legal.loading")}</span>
        </LoginNote>
      ) : null}
      <FormProblem message={failed} />
      {failed && !(legal.query.error instanceof ApiError && legal.query.error.status === 403) ? (
        <NoteAction label={t("legal.retry")} onPress={legal.reload} />
      ) : null}
      {legal.documents.length > 0 ? (
        <>
          <LegalDocuments documents={legal.documents} />
          <LoginConsent
            key={versions}
            t={String}
            label={t("legal.accept")}
            values={{
              onInput: (event) => legal.setAccepted((event.currentTarget as HTMLInputElement).checked),
            }}
          />
        </>
      ) : null}
      <FormProblem message={missing ? t("legal.missing") : accept.error ? errors.message(accept.error) : ""} />
      <LoginPrimary
        t={String}
        label={t("legal.acceptSubmit")}
        values={{ disabled: accept.isPending || !canSubmitWithLegal(legal) || missing }}
      />
      <OtherAccount />
    </LoginFrame>
  );
}
