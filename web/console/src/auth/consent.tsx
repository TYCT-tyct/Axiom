import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { Gate, useErrors, useSession } from "@axiom/app";
import LoginConsent from "@axiom/boards/Login/regions/LoginConsent";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useAttempt } from "./attempt";
import { canSubmitWithLegal, type LegalConsentState, LegalDocuments, useLegalConsent } from "./legal";
import { LoginFrame } from "./login-frame";
import { FormProblem, NoteAction, primaryLook, SignOutNote, wrapAnywhere } from "./login-parts";

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

// Accepting and signing out are the page's requests (attempt.ts): one at a time, the page taking nothing else meanwhile.
function ConsentStep() {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const legal = useLegalConsent();
  const accept = useAccept(legal);
  const attempt = useAttempt();
  const missing = legal.query.isSuccess && legal.documents.length === 0;
  const ready = canSubmitWithLegal(legal) && !missing;
  // waiting for the box to be ticked, or for the server: both disable the button, only the second is busy
  const blocked = attempt.kind !== null || !ready;
  const failed = attempt.failure?.kind === "accept" ? attempt.failure.error : null;
  return (
    <LoginFrame
      title={t("legal.title")}
      sub={t("legal.description", { email: session.user.email })}
      subStyle={wrapAnywhere}
      step="consent"
      attempt={attempt}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) attempt.run("accept", () => accept.mutateAsync({ consent: legal.versions ?? [] }));
      }}
    >
      <Documents legal={legal} />
      <FormProblem message={missing ? t("legal.missing") : failed ? errors.message(failed) : ""} />
      <LoginPrimary
        t={String}
        label={<span>{t("legal.acceptSubmit")}</span>}
        values={{ disabled: blocked, style: primaryLook(blocked) }}
      />
      <SignOutNote label={t("auth.mfa.otherAccount", { ns: "common" })} attempt={attempt} />
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
