import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { Gate, useErrors, useSession } from "@axiom/app";
import LoginConsent from "@axiom/boards/Login/regions/LoginConsent";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { canSubmitWithLegal, type LegalConsentState, LegalDocuments, useLegalConsent } from "./legal";
import { LoginFrame } from "./login-frame";
import { FormProblem, NoteAction, OtherAccount, primaryLook, wrapAnywhere } from "./login-parts";

// A member whose accepted versions are behind the site's current ones lands here, outside the shell: every shell page
// would be refused with consent_required until this succeeds.
export default function Consent() {
  return (
    <Gate stage="consent">
      <ConsentStep />
    </Gate>
  );
}

function useAccept(legal: LegalConsentState) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["LegalAcceptance"]) =>
      unwrapEmpty(consoleApi.POST("/api/console/v1/legal/accept", { body })),
    onSuccess: () => client.invalidateQueries({ queryKey: ["session"] }),
    onError: (error) => {
      if (error instanceof ApiError && error.code === "consent_required") {
        legal.reload();
      }
    },
  });
}

function ConsentStep() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const legal = useLegalConsent();
  const accept = useAccept(legal);
  const missing = legal.query.isSuccess && legal.documents.length === 0;
  // waiting for the box to be ticked, or for the server: both disable the button, only the second is busy
  const blocked = accept.isPending || !canSubmitWithLegal(legal) || missing;
  return (
    <LoginFrame
      title={t("legal.title")}
      sub={t("legal.description", { email: session.user.email })}
      subStyle={wrapAnywhere}
      step="consent"
      busy={accept.isPending}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        if (!accept.isPending && canSubmitWithLegal(legal) && !missing) {
          accept.mutate({ consent: legal.versions ?? [] });
        }
      }}
    >
      <Documents legal={legal} />
      <FormProblem message={missing ? t("legal.missing") : accept.error ? errors.message(accept.error) : ""} />
      <LoginPrimary
        t={String}
        label={<span>{t("legal.acceptSubmit")}</span>}
        values={{ disabled: blocked, style: primaryLook(blocked) }}
      />
      <OtherAccount />
    </LoginFrame>
  );
}

// The documents and the box that accepts them, or why they are not there. The box shows the answer the form would
// send: a new version on screen, or a reload after a refusal, clears it.
function Documents({ legal }: { legal: LegalConsentState }) {
  const { t } = useTranslation();
  const { t: common } = useTranslation("common");
  if (legal.query.isPending) {
    return (
      <LoginNote t={String}>
        <span role="status">{t("legal.loading")}</span>
      </LoginNote>
    );
  }
  if (legal.query.isError) {
    const forbidden = legal.query.error instanceof ApiError && legal.query.error.status === 403;
    return (
      <>
        <FormProblem message={forbidden ? common("state.forbiddenTitle") : t("legal.error")} />
        {forbidden ? null : <NoteAction label={t("legal.retry")} onPress={legal.reload} />}
      </>
    );
  }
  if (legal.documents.length === 0) return null;
  return (
    <>
      <LegalDocuments documents={legal.documents} />
      <LoginConsent
        t={String}
        label={t("legal.accept")}
        values={{
          checked: legal.accepted,
          onToggle: (event) => legal.setAccepted((event.currentTarget as HTMLInputElement).checked),
        }}
      />
    </>
  );
}
