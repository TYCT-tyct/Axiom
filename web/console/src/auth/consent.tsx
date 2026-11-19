import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { useErrors, useSession } from "@axiom/app";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useAttempt } from "./attempt";
import { canSubmitWithLegal, LegalConsent, type LegalConsentState, useLegalConsent } from "./legal";
import { LoginFrame, useStage } from "./login-frame";
import { FormProblem, LoginGate, primaryLook, SignOutNote, wrapAnywhere } from "./login-parts";

// A member whose accepted versions are behind the site's current ones lands here, outside the shell: every shell page
// would be refused with consent_required until this succeeds.
export default function Consent() {
  return (
    <LoginGate stage="consent">
      <ConsentStep />
    </LoginGate>
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
  const stage = useStage();
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
      stage={stage}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) attempt.run("accept", () => accept.mutateAsync({ consent: legal.versions ?? [] }));
      }}
    >
      <LegalConsent consent={legal} />
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
