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
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { FormProblem, NoteAction, OtherAccount } from "./login-parts";
import { usePasskeySignIn } from "./passkey-sign-in";

// The second step of a password sign-in: a code from the authenticator app or a recovery code, in one field (the
// server tells them apart), or the user's passkey where the site has passkeys.
export default function Mfa() {
  return (
    <Gate stage="pending">
      <MfaStep />
    </Gate>
  );
}

function MfaStep() {
  const { t } = useTranslation();
  const errors = useErrors();
  const stage = useStage();
  const passkeys = useQuery(siteQuery).data?.passkeys ?? false;
  const verify = useVerifyMfa();
  const passkey = usePasskeySignIn("mfa");
  const code = useRef<HTMLInputElement | null>(null);
  const problem = verify.isError ? errors.message(verify.error) : "";
  return (
    <LoginFrame
      title={t("auth.mfa.title", { ns: "common" })}
      sub={t("auth.mfa.description", { ns: "common" })}
      step="mfa"
      shape={passkey.pending ? "key" : verify.isSuccess ? "enter" : "code"}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        const value = (code.current?.value ?? "").trim();
        if (value && !verify.isPending) {
          verify.mutate({ code: value }, { onError: () => stage.hear({ type: "bad" }) });
        }
      }}
    >
      <CodeField problem={problem} code={code} stage={stage} />
      <FormProblem id="mfa-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.mfa.submit", { ns: "common" })}</span>}
        values={{ disabled: verify.isPending }}
      />
      <LoginNote t={String}>
        <span id="mfa-hint">{t("auth.mfa.hint", { ns: "common" })}</span>
      </LoginNote>
      {passkeys ? <PasskeyWay passkey={passkey} /> : null}
      <OtherAccount />
    </LoginFrame>
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

// The passkey as the second step. A 401 means the pending sign-in is over (its attempts are spent, or it expired):
// the same button would be refused again, so the step says so and offers to start the sign-in again, which keeps
// where the visitor was going. Any other failure, a cancelled prompt among them, leaves the button to try again.
function PasskeyWay({ passkey }: { passkey: ReturnType<typeof usePasskeySignIn> }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const signOut = useSignOut();
  const over = passkey.failure instanceof ApiError && passkey.failure.status === 401;
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      {over ? null : (
        <LoginStack t={String}>
          <LoginButton
            t={String}
            label={<span>{t("auth.mfa.usePasskey")}</span>}
            values={{ disabled: passkey.pending, onPress: () => void passkey.start() }}
          />
        </LoginStack>
      )}
      <FormProblem message={over ? t("auth.mfa.over") : passkey.failure ? errors.message(passkey.failure) : ""} />
      {over ? <NoteAction label={t("auth.mfa.again")} onPress={() => !signOut.isPending && signOut.mutate()} /> : null}
    </>
  );
}
