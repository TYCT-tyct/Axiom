import { ApiError } from "@axiom/api";
import { consoleAuth } from "@axiom/api/auth";
import { Gate, sessionBinding, useErrors, useSession, useStartSession } from "@axiom/app";
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
import { FormProblem, primaryLook, SignOutNote } from "./login-parts";
import { usePasskey } from "./passkey-sign-in";

// The second step of a password sign-in: a code from the authenticator app or a recovery code, in one field (the
// server tells them apart), or the user's passkey where the site has passkeys. Both answer the same pending sign-in,
// so they are requests of one page (attempt.ts).
export default function Mfa() {
  return (
    <Gate stage="pending">
      <Pending />
    </Gate>
  );
}

// The step belongs to one pending session: another one (the same person signed in again in another tab) is a new
// step, with none of the old one's failures, the end of it among them.
function Pending() {
  return <MfaStep key={sessionBinding(useSession())} />;
}

// A 401 unauthenticated says the pending sign-in itself is over (its attempts are spent, or it expired): no code or
// passkey can finish it any more, even a right one. A wrong code is a 401 too, but invalid_code: the step stays. The
// code is sent outside the session's mutations, whose 401 would end the session on the page while its cookie still
// names it; the page offers to sign out instead.
const isOver = (error: unknown) =>
  error instanceof ApiError && error.status === 401 && error.code === "unauthenticated";

function MfaStep() {
  const { t } = useTranslation();
  const stage = useStage();
  const attempt = useAttempt();
  const start = useStartSession();
  const code = useRef<HTMLInputElement | null>(null);
  // over for good: a sign-out that fails after it does not bring the step back
  const [over, setOver] = useState(false);
  const fail = (error: unknown) => isOver(error) && setOver(true);
  const verified = attempt.done === "code" || attempt.done === "passkey";
  const submit = () => {
    const value = (code.current?.value ?? "").trim();
    if (!value || over) return;
    attempt.run(
      "code",
      async () => {
        const grant = await consoleAuth.verifyMfa({ code: value });
        await start(grant.csrf_token);
      },
      {
        onFail: (error) => {
          fail(error);
          stage.hear({ type: "bad" });
        },
      },
    );
  };
  return (
    <LoginFrame
      title={t("auth.mfa.title", { ns: "common" })}
      sub={over ? t("auth.mfa.over") : t("auth.mfa.description", { ns: "common" })}
      step={over ? "over" : "mfa"}
      attempt={attempt}
      shape={attempt.kind === "passkey" ? "key" : verified ? "enter" : "code"}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      {over ? (
        <SignOutNote label={t("auth.mfa.again")} attempt={attempt} />
      ) : (
        <SecondFactor attempt={attempt} stage={stage} code={code} onFail={fail} />
      )}
    </LoginFrame>
  );
}

function SecondFactor({
  attempt,
  stage,
  code,
  onFail,
}: {
  attempt: Attempt;
  stage: Stage;
  code: { current: HTMLInputElement | null };
  onFail: (error: unknown) => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const passkeys = useQuery(siteQuery).data?.passkeys ?? false;
  const busy = attempt.kind !== null;
  const problem = attempt.failure?.kind === "code" ? errors.message(attempt.failure.error) : "";
  return (
    <>
      <CodeField problem={problem} code={code} stage={stage} />
      <FormProblem id="mfa-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.mfa.submit", { ns: "common" })}</span>}
        values={{ disabled: busy, style: primaryLook(busy) }}
      />
      <LoginNote t={String}>
        <span id="mfa-hint">{t("auth.mfa.hint", { ns: "common" })}</span>
      </LoginNote>
      {passkeys ? <PasskeyWay attempt={attempt} onFail={onFail} /> : null}
      <SignOutNote label={t("auth.mfa.otherAccount", { ns: "common" })} attempt={attempt} />
    </>
  );
}

// The code's field: each digit typed lights a stud of the padlock on the sea.
function CodeField({
  problem,
  code,
  stage,
}: {
  problem: string;
  code: { current: HTMLInputElement | null };
  stage: Stage;
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
        input: (element) => {
          code.current = element as HTMLInputElement | null;
        },
        onInput: (event) => {
          const value = (event.currentTarget as HTMLInputElement).value;
          stage.hear({ type: "digit", n: Math.min(6, value.replace(/\D/g, "").length) });
        },
      }}
    />
  );
}

// The passkey as the second step; a failure other than the end of the sign-in (a cancelled prompt among them) leaves
// the button to try again.
function PasskeyWay({ attempt, onFail }: { attempt: Attempt; onFail: (error: unknown) => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const passkey = usePasskey("mfa");
  const failed = attempt.failure;
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      <LoginStack t={String}>
        <LoginButton
          t={String}
          label={<span>{t("auth.mfa.usePasskey")}</span>}
          values={{
            disabled: attempt.kind !== null,
            onPress: () => attempt.run("passkey", passkey, { cancellable: true, onFail }),
          }}
        />
      </LoginStack>
      <FormProblem message={failed?.kind === "passkey" ? errors.message(failed.error) : ""} />
    </>
  );
}
