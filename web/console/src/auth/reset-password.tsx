import { ApiError, consoleApi, unwrapEmpty } from "@axiom/api";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { type Attempt, useAttempt } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { EmailField, NoteAction, primaryLook, useLoginFace, wrapAnywhere } from "./login-parts";
import { dropMailLink, mailLink, onMailLink } from "./mail-link";
import {
  badEmail,
  PasswordStepBody,
  unanswered,
  useEmailStep,
  useFocusAgain,
  useMailLanguage,
  usePasswordStep,
} from "./request-state";
import { AfterStep, AnswerStep, useBrowserSession, useSaid, WayOn } from "./way-on";

// The reset email links here with its single-use token, which the route has already taken out of the address and kept
// (mail-link.ts); the page sends it only to resetPassword. Without a token the page asks for the email instead. A
// reset ends the sessions of its account and does not sign in. The page has no gate: a link works in a browser signed
// in as anyone, and the way on finds out as whom (Onward). Each new link starts its form afresh.
export default function ResetPassword() {
  useLoginFace();
  const link = useSyncExternalStore(onMailLink, mailLink);
  return link.token ? <NewPassword key={link.n} token={link.token} /> : <RequestReset key={link.n} />;
}

// The email step, and once its request is accepted, the step that says to check the mail. The server answers every
// address alike, so the page cannot say more than that. Both steps' way back to sign in is the one the browser's session
// decides (way-on.tsx), read when each step opens.
function RequestReset() {
  const { t } = useTranslation();
  const stage = useStage();
  const attempt = useAttempt();
  const browser = useBrowserSession();
  const said = useSaid(browser);
  const [sent, setSent] = useState(false);
  const form = useRequestForm(stage, attempt, () => setSent(true));
  const editAgain = useFocusAgain(form.input);
  if (sent) {
    return (
      <AnswerStep
        title={t("auth.reset.sentTitle")}
        sub={t("auth.reset.sent")}
        step="sent"
        shape="enter"
        signIn={t("auth.reset.back")}
        attempt={attempt}
        stage={stage}
      >
        <NoteAction label={t("auth.reset.resend")} onPress={() => editAgain(() => setSent(false))} />
      </AnswerStep>
    );
  }
  return (
    <LoginFrame
      title={t("auth.reset.title")}
      sub={said(t("auth.reset.description"))}
      subStyle={wrapAnywhere}
      step="request"
      shape="start"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        form.submit();
      }}
    >
      <EmailField
        id="reset-email"
        problemId="reset-problem"
        email={form.email}
        input={form.input}
        problem={form.problem}
        invalid={form.invalid}
        onInput={form.type}
      />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.reset.submit")}</span>}
        values={{ disabled: form.blocked, style: primaryLook(form.blocked) }}
      />
      <WayOn browser={browser} signIn={t("auth.reset.back")} attempt={attempt} />
    </LoginFrame>
  );
}

// useRequestForm is the email step (useEmailStep) and its request.
function useRequestForm(stage: Stage, attempt: Attempt, onSent: () => void) {
  const language = useMailLanguage();
  const step = useEmailStep(stage, attempt, "request", badEmail);
  const send = (address: string) =>
    unwrapEmpty(consoleApi.POST("/api/console/v1/auth/password-reset", { body: { email: address, language } })).then(
      onSent,
    );
  return {
    ...step,
    submit: () => {
      const address = step.take();
      if (address && !step.blocked) {
        attempt.run("request", () => send(address), { onFail: () => stage.hear({ type: "bad" }) });
      }
    },
  };
}

type After = "done" | "invalid" | "unknown";

// The link's step: the new password, and once the reset is over, the way on. The page's requests (attempt.ts) are the
// reset and, on the way on, a sign-out.
function NewPassword({ token }: { token: string }) {
  const stage = useStage();
  const attempt = useAttempt();
  const [after, setAfter] = useState<After | null>(null);
  if (after) return <Onward after={after} attempt={attempt} stage={stage} />;
  return <NewPasswordForm token={token} attempt={attempt} stage={stage} onAfter={setAfter} />;
}

// What a failed reset leaves to the way on. The server's one answer for a link that is wrong, expired, spent or
// superseded is a 422 unprocessable. A reset whose answer never came, or any 5xx, may have gone through: the server's
// own 500 can follow a commit whose reply was lost, and its 503 for a request out of time says to look before trying
// again. Either way the form has nothing more to do. Any other refusal is the form's to say: the length, a limit.
function afterFailure(error: unknown): After | null {
  if (!(error instanceof ApiError)) return "unknown";
  if (error.status === 422 && error.code === "unprocessable") return "invalid";
  return unanswered(error) ? "unknown" : null;
}

const completeReset = (body: { token: string; new_password: string; language: "zh" | "en" }) =>
  unwrapEmpty(consoleApi.POST("/api/console/v1/auth/password-reset/complete", { body }));

function NewPasswordForm({
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
  const language = useMailLanguage();
  const step = usePasswordStep(attempt, "reset", "/new_password", t("auth.reset.length"));
  return (
    <LoginFrame
      title={t("auth.reset.newTitle")}
      sub={t("auth.reset.newDescription")}
      step="new"
      shape="start"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        const secret = step.take();
        if (!secret || step.blocked) return;
        // not cancellable: a reset sent may end this browser's session
        const body = { token, new_password: secret, language };
        attempt.run("reset", () => completeReset(body).then(() => onAfter("done")), {
          onFail: (error) => {
            const next = afterFailure(error);
            if (next) onAfter(next);
            else stage.hear({ type: "bad" });
          },
        });
      }}
    >
      <PasswordStepBody
        id="reset-password"
        label={t("auth.reset.newPassword")}
        step={step}
        submit={t("auth.reset.newSubmit")}
      />
    </LoginFrame>
  );
}

const afterWords = {
  done: { title: "auth.reset.doneTitle", sub: "auth.reset.done", signIn: "auth.reset.signIn" },
  invalid: { title: "auth.reset.invalidTitle", sub: "auth.reset.invalid", signIn: "auth.reset.signInNew" },
  unknown: { title: "auth.reset.unknownTitle", sub: "auth.reset.unknown", signIn: "auth.reset.signInNew" },
} as const;

// The way on after a reset: to sign in with the new password, and, for a link that did not work, to ask for a new one.
function Onward({ after, attempt, stage }: { after: After; attempt: Attempt; stage: Stage }) {
  const { t } = useTranslation();
  const words = afterWords[after];
  return (
    <AfterStep
      after={after}
      title={t(words.title)}
      sub={t(words.sub)}
      signIn={t(words.signIn)}
      attempt={attempt}
      stage={stage}
    >
      {after === "done" ? null : <NoteAction label={t("auth.reset.again")} onPress={dropMailLink} />}
    </AfterStep>
  );
}
