import { ApiError, consoleApi, type Schemas, unwrapEmpty } from "@axiom/api";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Attempt, useAttempt } from "./attempt";
import { canSubmitWithLegal, LegalConsent, useLegalConsent } from "./legal";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { fieldRefused, RouteNote, useLoginFace } from "./login-parts";
import { useMailLink } from "./mail-link";
import { PasswordStepBody, unanswered, usePasswordStep } from "./request-state";
import { AfterStep, AnswerStep } from "./way-on";

// An open registration's mail links here with its single-use token, which the route has already taken out of the
// address and kept (mail-link.ts); the page sends it only to verifyEmail, with the password the mailbox owner chooses
// here and the legal documents they accept. Verifying makes the account and does not sign in. The page has no gate: a
// link works in a browser signed in as anyone, and the way on to sign in finds out as whom (way-on.tsx).
export default function VerifyEmail() {
  useLoginFace();
  const link = useMailLink("/verify-email");
  return link.token ? <Verify key={link.n} token={link.token} /> : <Missing />;
}

// What a verification led to: the account is made, the link did not work, the address was registered meanwhile, its
// domain is no longer taken, or the answer never came (it may have been made).
type After = "done" | "invalid" | "taken" | "domain" | "unknown";

// What a failed verification leaves to the way on, as nothing on the form can change it: the server's one answer for an
// unknown, expired or used link is invalid_verification, and its guard's for a token that is not one (cut short when
// copied) a 400 naming /token; an address registered in the meantime is email_taken, and one whose domain the site
// stopped taking since the mail email_domain_not_allowed; an answer that never came may have gone through. Any other
// refusal is the form's to say: the password's length, a limit, the documents.
const afterCodes: Record<string, After> = {
  invalid_verification: "invalid",
  email_taken: "taken",
  email_domain_not_allowed: "domain",
};
function afterFailure(error: unknown): After | null {
  if (unanswered(error)) return "unknown";
  if (fieldRefused(error, "/token")) return "invalid";
  return (error instanceof ApiError && afterCodes[error.code ?? ""]) || null;
}

function Missing() {
  const { t } = useTranslation();
  const stage = useStage();
  const attempt = useAttempt();
  return (
    <AnswerStep
      title={t("auth.verify.title")}
      sub={t("auth.verify.missing")}
      step="missing"
      shape="start"
      signIn={t("auth.verify.signIn")}
      attempt={attempt}
      stage={stage}
      guest={<RouteNote to="/register" label={t("auth.verify.again")} />}
    />
  );
}

function Verify({ token }: { token: string }) {
  const stage = useStage();
  const attempt = useAttempt();
  const [after, setAfter] = useState<After | null>(null);
  if (after) return <Verified after={after} attempt={attempt} stage={stage} />;
  return <VerifyForm token={token} attempt={attempt} stage={stage} onAfter={setAfter} />;
}

const verifyEmail = (body: Schemas["VerifyEmailRequest"]) =>
  unwrapEmpty(consoleApi.POST("/api/console/v1/auth/verify-email", { body }));

// The password and the documents to accept. Not cancellable once sent: the account it makes is made.
function VerifyForm({
  token,
  attempt,
  stage,
  onAfter,
}: {
  token: string;
  attempt: Attempt;
  stage: Stage;
  onAfter: (after: After) => void;
}) {
  const { t } = useTranslation();
  const legal = useLegalConsent();
  const step = usePasswordStep(attempt, "verify", "/password", t("auth.register.length"));
  return (
    <LoginFrame
      title={t("auth.verify.title")}
      sub={t("auth.verify.description")}
      step="verify"
      shape="start"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        const secret = step.take();
        if (!secret || step.blocked || !canSubmitWithLegal(legal)) return;
        const body = { token, password: secret, consent: legal.versions };
        attempt.run("verify", () => verifyEmail(body).then(() => onAfter("done")), {
          onFail: (error) => {
            const next = afterFailure(error);
            if (next) return onAfter(next);
            stage.hear({ type: "bad" });
            if (error instanceof ApiError && error.code === "consent_required") legal.reload();
          },
        });
      }}
    >
      <PasswordStepBody
        id="verify-password"
        label={t("auth.password", { ns: "common" })}
        step={{ ...step, blocked: step.blocked || !canSubmitWithLegal(legal) }}
        submit={t("auth.verify.submit")}
      >
        <LegalConsent consent={legal} />
      </PasswordStepBody>
    </LoginFrame>
  );
}

const afterWords = {
  done: { title: "auth.verify.doneTitle", sub: "auth.verify.done" },
  invalid: { title: "auth.verify.invalidTitle", sub: "auth.verify.invalid" },
  taken: { title: "auth.verify.takenTitle", sub: "auth.verify.taken" },
  domain: { title: "auth.verify.domainTitle", sub: "auth.verify.domain" },
  unknown: { title: "auth.verify.unknownTitle", sub: "auth.verify.unknown" },
} as const;

// The way on after a verification: to sign in, and, for a link that did not work, an address the site no longer takes or
// an answer that never came, to register again; both only once the browser is known to be signed out (WayOn).
function Verified({ after, attempt, stage }: { after: After; attempt: Attempt; stage: Stage }) {
  const { t } = useTranslation();
  const words = afterWords[after];
  const again = after === "invalid" || after === "domain" || after === "unknown";
  return (
    <AfterStep
      after={after}
      title={t(words.title)}
      sub={t(words.sub)}
      signIn={t("auth.verify.signIn")}
      attempt={attempt}
      stage={stage}
      guest={again ? <RouteNote to="/register" label={t("auth.verify.again")} /> : null}
    />
  );
}
