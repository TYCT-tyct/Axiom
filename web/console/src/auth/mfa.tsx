import { consoleAuth } from "@axiom/api/auth";
import {
  pendingOver,
  sessionBinding,
  UnreadSession,
  useAdoptGrant,
  useEndPending,
  useErrors,
  useSession,
} from "@axiom/app";
import LoginButton from "@axiom/boards/Login/regions/LoginButton";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginOr from "@axiom/boards/Login/regions/LoginOr";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import LoginStack from "@axiom/boards/Login/regions/LoginStack";
import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { type Attempt, useAttempt } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { FormProblem, LoginGate, primaryLook, SignOutNote } from "./login-parts";
import { usePasskey } from "./passkey-sign-in";

// The second step of a password sign-in: a code from the authenticator app or a recovery code, in one field (the
// server tells them apart), or the user's passkey where the site has passkeys. Both answer the same pending sign-in,
// so they are requests of one page (attempt.ts).
export default function Mfa() {
  return (
    <LoginGate stage="pending">
      <Pending />
    </LoginGate>
  );
}

// The step belongs to one pending session: another one (the same person signed in again in another tab) is a new
// step, with none of the old one's failures. It offers the person's own factors: a session that says they have no
// authenticator app (totp_enabled false: only a passkey, so no recovery codes either) is not asked for a code; an
// older server's session, without the field, is asked as before.
function Pending() {
  const session = useSession();
  const pending = sessionBinding(session);
  return <MfaStep key={pending} pending={pending} codes={session.totp_enabled !== false} />;
}

// A pending sign-in that is over (pendingOver) cannot be finished, so the step signs it out (useEndPending), and the
// sign-in page says why the visitor is back there; a sign-out that fails is said, and offered again. A step that passed
// but whose new session could not be read (UnreadSession) offers only to read it again.
function MfaStep({ pending, codes }: { pending: string; codes: boolean }) {
  const { t } = useTranslation();
  const stage = useStage();
  const step = useSecondStep(stage, pending);
  const { attempt } = step;
  const phase = step.unread ? "unread" : step.over ? "over" : "mfa";
  const verified = attempt.done === "code" || attempt.done === "passkey";
  const sub = {
    unread: t("auth.mfa.unread", { ns: "common" }),
    over: t("auth.mfa.over", { ns: "common" }),
    mfa: codes ? t("auth.mfa.description", { ns: "common" }) : t("auth.mfa.passkeyOnly"),
  };
  const send = { unread: step.reread, over: step.end, mfa: codes ? step.verify : step.passkey };
  return (
    <LoginFrame
      title={t("auth.mfa.title", { ns: "common" })}
      sub={sub[phase]}
      step={phase}
      attempt={attempt}
      shape={attempt.kind === "passkey" || !codes ? "key" : verified ? "enter" : "code"}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        send[phase]();
      }}
    >
      <StepBody step={step} phase={phase} stage={stage} codes={codes} />
    </LoginFrame>
  );
}

// What the step asks, by where it is: the factors, the end of a sign-in that is over, or the read of a passed one.
function StepBody({
  step,
  phase,
  stage,
  codes,
}: {
  step: ReturnType<typeof useSecondStep>;
  phase: "unread" | "over" | "mfa";
  stage: Stage;
  codes: boolean;
}) {
  const { t } = useTranslation();
  if (phase === "unread") return <Reread attempt={step.attempt} reason={step.unread?.reason} />;
  if (phase === "over") {
    return <SignOutNote label={t("auth.mfa.again", { ns: "common" })} attempt={step.attempt} ended />;
  }
  return (
    <SecondFactor
      attempt={step.attempt}
      stage={stage}
      code={step.code}
      codes={codes}
      problem={step.missing ? t("auth.mfa.missing") : ""}
      onType={step.typed}
      onPasskey={step.passkey}
    />
  );
}

// useSecondStep holds the step's requests: the code, the passkey and, once the pending sign-in is over, its sign-out.
// The code is sent outside the session's mutations, whose 401 would end the session on the page while its cookie
// still names it.
function useSecondStep(stage: Stage, pending: string) {
  const attempt = useAttempt();
  const adopt = useAdoptGrant();
  const endPending = useEndPending();
  const passkey = usePasskey("mfa");
  const code = useRef<HTMLInputElement | null>(null);
  const [over, setOver] = useState(false);
  const [unread, setUnread] = useState<UnreadSession | null>(null);
  const [missing, setMissing] = useState(false);
  const end = () => attempt.run("signOut", () => endPending(pending));
  const fail = (error: unknown) => {
    if (error instanceof UnreadSession) {
      setUnread(error);
      return;
    }
    if (!pendingOver(error)) return;
    setOver(true);
    end();
  };
  const verify = () => {
    const value = (code.current?.value ?? "").trim();
    setMissing(!value);
    if (!value) return;
    const send = async () => adopt((await consoleAuth.verifyMfa({ code: value })).csrf_token);
    attempt.run("code", send, {
      onFail: (error) => {
        fail(error);
        stage.hear({ type: "bad" });
      },
    });
  };
  return {
    attempt,
    over,
    unread,
    missing,
    code,
    end,
    verify,
    passkey: () => attempt.run("passkey", passkey, { cancellable: true, onFail: fail }),
    typed: () => setMissing(false),
    reread: () => {
      if (unread) attempt.run("session", () => adopt(unread.csrf), { onFail: fail });
    },
  };
}

// The read of a session a passed step started, tried again; its failure is said.
function Reread({ attempt, reason }: { attempt: Attempt; reason: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const busy = attempt.kind !== null;
  return (
    <>
      <FormProblem message={errors.message(reason)} />
      <LoginPrimary
        t={String}
        label={<span>{t("state.retry", { ns: "common" })}</span>}
        values={{ disabled: busy, style: primaryLook(busy) }}
      />
    </>
  );
}

type FactorProps = {
  attempt: Attempt;
  stage: Stage;
  code: { current: HTMLInputElement | null };
  // whether the person has an authenticator app (and recovery codes) to answer with
  codes: boolean;
  // the code's own problem, before anything is sent (none typed)
  problem: string;
  onType: () => void;
  onPasskey: () => void;
};

// The step's ways: the code and, where the site has passkeys, the passkey beside it; a person with only a passkey has
// the passkey as the step's one action.
function SecondFactor({ attempt, stage, code, codes, problem, onType, onPasskey }: FactorProps) {
  const { t } = useTranslation();
  const errors = useErrors();
  // a session with no authenticator app says the person has a passkey: the server checked the domain when it issued it,
  // so the passkey is offered whatever the site's settings say, or whether they could be read
  const site = useQuery(siteQuery);
  const passkeys = !codes || (site.data?.passkeys ?? false);
  const busy = attempt.kind !== null;
  const failed = attempt.failure;
  const codeProblem = problem || (failed?.kind === "code" ? errors.message(failed.error) : "");
  const passkeyProblem = failed?.kind === "passkey" ? errors.message(failed.error) : "";
  const passkeyLabel = <span>{t("auth.mfa.usePasskey")}</span>;
  return (
    <>
      {codes ? (
        <>
          <CodeField problem={codeProblem} code={code} stage={stage} onType={onType} />
          <FormProblem id="mfa-problem" message={codeProblem} />
          <LoginPrimary
            t={String}
            label={<span>{t("auth.mfa.submit", { ns: "common" })}</span>}
            values={{ disabled: busy, style: primaryLook(busy) }}
          />
          <LoginNote t={String}>
            <span id="mfa-hint">{t("auth.mfa.hint", { ns: "common" })}</span>
          </LoginNote>
        </>
      ) : passkeys ? (
        <LoginPrimary t={String} label={passkeyLabel} values={{ disabled: busy, style: primaryLook(busy) }} />
      ) : null}
      {codes && passkeys ? (
        <>
          <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
          <LoginStack t={String}>
            <LoginButton t={String} label={passkeyLabel} values={{ disabled: busy, onPress: onPasskey }} />
          </LoginStack>
        </>
      ) : null}
      <FormProblem message={passkeyProblem} />
      <SignOutNote label={t("auth.mfa.otherAccount", { ns: "common" })} attempt={attempt} />
    </>
  );
}

// The code's field: each digit typed lights a stud of the padlock on the sea.
function CodeField({
  problem,
  code,
  stage,
  onType,
}: {
  problem: string;
  code: { current: HTMLInputElement | null };
  stage: Stage;
  onType: () => void;
}) {
  const { t } = useTranslation();
  return (
    <LoginField
      t={String}
      text={{ label: t("auth.mfa.code", { ns: "common" }) }}
      values={{
        id: "mfa-code",
        name: "code",
        type: "text",
        autoComplete: "one-time-code",
        fieldCls: problem ? "bad" : "",
        describedBy: problem ? "mfa-problem mfa-hint" : "mfa-hint",
        invalid: problem ? true : undefined,
        input: (element) => {
          code.current = element as HTMLInputElement | null;
        },
        onInput: (event) => {
          const value = (event.currentTarget as HTMLInputElement).value;
          onType();
          stage.hear({ type: "digit", n: Math.min(6, value.replace(/\D/g, "").length) });
        },
      }}
    />
  );
}
