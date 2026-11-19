import { ApiError } from "@axiom/api";
import { Gate, useErrors, useSignOut, useVerifyMfa } from "@axiom/app";
import LoginButton from "@axiom/boards/Login/regions/LoginButton";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginOr from "@axiom/boards/Login/regions/LoginOr";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import LoginStack from "@axiom/boards/Login/regions/LoginStack";
import { useQuery } from "@tanstack/react-query";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { type Attempts, useAttempts } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { FormProblem, NoteAction, OtherAccount, primaryLook } from "./login-parts";
import { usePasskey } from "./passkey-sign-in";

// The second step of a password sign-in: a code from the authenticator app or a recovery code, in one field (the
// server tells them apart), or the user's passkey where the site has passkeys. Both answer the same pending sign-in,
// so they are attempts of one page (attempt.ts): while one is out the other waits.
export default function Mfa() {
  return (
    <Gate stage="pending">
      <MfaStep />
    </Gate>
  );
}

// A 401 unauthenticated says the pending sign-in itself is over (its attempts are spent, or it expired): no code or
// passkey can finish it any more, even a right one. A wrong code is a 401 too, but invalid_code: the step stays.
const isOver = (error: unknown) =>
  error instanceof ApiError && error.status === 401 && error.code === "unauthenticated";

function MfaStep() {
  const { t } = useTranslation();
  const stage = useStage();
  const attempts = useAttempts();
  const verify = useVerifyMfa();
  const code = useRef<HTMLInputElement | null>(null);
  const over = isOver(attempts.failure?.error);
  const submit = () => {
    const value = (code.current?.value ?? "").trim();
    const attempt = value && !over ? attempts.begin("code") : null;
    if (!attempt) return;
    verify.mutate(
      { code: value },
      {
        onSuccess: attempt.done,
        onError: (error) => {
          attempt.fail(error);
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
      busy={attempts.kind !== null}
      shape={attempts.kind === "passkey" ? "key" : verify.isSuccess ? "enter" : "code"}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      {over ? <SignInAgain /> : <SecondFactor attempts={attempts} stage={stage} code={code} />}
    </LoginFrame>
  );
}

// Once the pending sign-in is over the step offers only to start again: it signs the pending session out, and the gate
// takes the visitor to sign in, keeping where they were going; a sign-out that fails says so and can be tried again.
function SignInAgain() {
  const { t } = useTranslation();
  const errors = useErrors();
  const signOut = useSignOut();
  return (
    <>
      <NoteAction label={t("auth.mfa.again")} onPress={() => !signOut.isPending && signOut.mutate()} />
      <FormProblem
        message={signOut.error ? t("signOutFailed", { ns: "common", reason: errors.message(signOut.error) }) : ""}
      />
    </>
  );
}

function SecondFactor({
  attempts,
  stage,
  code,
}: {
  attempts: Attempts;
  stage: Stage;
  code: { current: HTMLInputElement | null };
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const passkeys = useQuery(siteQuery).data?.passkeys ?? false;
  const failed = attempts.failure;
  const problem = failed?.kind === "code" ? errors.message(failed.error) : "";
  return (
    <>
      <CodeField problem={problem} code={code} stage={stage} />
      <FormProblem id="mfa-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.mfa.submit", { ns: "common" })}</span>}
        values={{
          disabled: attempts.kind !== null,
          style: primaryLook(attempts.kind !== null),
        }}
      />
      <LoginNote t={String}>
        <span id="mfa-hint">{t("auth.mfa.hint", { ns: "common" })}</span>
      </LoginNote>
      {passkeys ? <PasskeyWay attempts={attempts} /> : null}
      <OtherAccount />
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
function PasskeyWay({ attempts }: { attempts: Attempts }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const passkey = usePasskey("mfa");
  const failed = attempts.failure;
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      <LoginStack t={String}>
        <LoginButton
          t={String}
          label={<span>{t("auth.mfa.usePasskey")}</span>}
          values={{
            disabled: attempts.kind !== null,
            onPress: () => {
              const attempt = attempts.begin("passkey");
              if (attempt) void passkey(attempt);
            },
          }}
        />
      </LoginStack>
      <FormProblem message={failed?.kind === "passkey" ? errors.message(failed.error) : ""} />
    </>
  );
}
