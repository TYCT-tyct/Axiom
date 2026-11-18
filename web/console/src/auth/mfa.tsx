import { Gate, useErrors, useVerifyMfa } from "@axiom/app";
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
import { FormProblem, LoginFrame, OtherAccount, useHear } from "./login-frame";
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
  const hear = useHear();
  const site = useQuery(siteQuery);
  const verify = useVerifyMfa();
  const passkey = usePasskeySignIn("mfa");
  const code = useRef<HTMLInputElement | null>(null);
  const problem = verify.isError ? errors.message(verify.error) : "";
  return (
    <LoginFrame
      title={t("auth.mfa.title", { ns: "common" })}
      sub={t("auth.mfa.description", { ns: "common" })}
      shape={passkey.pending ? "key" : verify.isSuccess ? "enter" : "code"}
      onSubmit={(event) => {
        event.preventDefault();
        const value = (code.current?.value ?? "").trim();
        if (value && !verify.isPending) {
          verify.mutate({ code: value }, { onError: () => hear({ type: "bad" }) });
        }
      }}
    >
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
            hear({ type: "digit", n: Math.min(6, value.replace(/\D/g, "").length) });
          },
        }}
      />
      <FormProblem id="mfa-problem" message={problem} />
      <LoginPrimary t={String} label={t("auth.mfa.submit", { ns: "common" })} values={{ disabled: verify.isPending }} />
      <LoginNote t={String}>
        <span id="mfa-hint">{t("auth.mfa.hint", { ns: "common" })}</span>
      </LoginNote>
      {site.data?.passkeys ? (
        <>
          <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
          <LoginStack t={String}>
            <LoginButton
              t={String}
              label={t("auth.mfa.usePasskey")}
              values={{ disabled: passkey.pending, onPress: () => void passkey.start() }}
            />
          </LoginStack>
          <FormProblem message={passkey.failure ? errors.message(passkey.failure) : ""} />
        </>
      ) : null}
      <OtherAccount />
    </LoginFrame>
  );
}
