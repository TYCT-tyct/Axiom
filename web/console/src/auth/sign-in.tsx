import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { Gate, useErrors, useSignIn } from "@axiom/app";
import LoginButton from "@axiom/boards/Login/regions/LoginButton";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginLink from "@axiom/boards/Login/regions/LoginLink";
import LoginNote from "@axiom/boards/Login/regions/LoginNote";
import LoginOr from "@axiom/boards/Login/regions/LoginOr";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import LoginStack from "@axiom/boards/Login/regions/LoginStack";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { type AnchorHTMLAttributes, type RefObject, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { FormProblem, LoginFrame, NoteAction, NoteLink, useHear } from "./login-frame";
import { usePasskeySignIn } from "./passkey-sign-in";

export default function SignIn() {
  const site = useQuery(siteQuery);
  const origin = site.data?.login_origin;
  const methods = site.data;

  // A sign-in domain owns the provider callback and the passkey relying party. On another console domain the
  // password still works, but the visitors who need those two are sent to the domain that has them.
  useEffect(() => {
    if (!origin || !methods || window.location.origin === new URL(origin).origin) {
      return;
    }
    if ((methods.oauth_providers?.length ?? 0) > 0 || methods.passkeys) {
      window.location.replace(origin.replace(/\/$/, "") + window.location.pathname + window.location.search);
    }
  }, [origin, methods]);

  return (
    <Gate stage="guest">
      <SignInSteps />
    </Gate>
  );
}

// Shown to the stage as an address that looks whole; the server decides what an address is.
const looksLikeEmail = (value: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);

// SignInSteps asks for the email, then the password, and sends both in one signIn: the two steps are only how the
// page asks (port design §9). The server's answer moves the visitor on through the gate: to the second factor, to
// consent, or back to where they were going.
function SignInSteps() {
  const { t } = useTranslation();
  const errors = useErrors();
  const hear = useHear();
  const signIn = useSignIn();
  const [step, setStep] = useState<"email" | "password">("email");
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const password = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    if (step === "password") password.current?.focus();
  }, [step]);

  const submit = () => {
    const address = email.trim();
    if (step === "email") {
      setMissing(!address);
      if (address) setStep("password");
      return;
    }
    const secret = password.current?.value ?? "";
    setMissing(!secret);
    if (secret && !signIn.isPending) {
      signIn.mutate({ email: address, password: secret }, { onError: () => hear({ type: "bad" }) });
    }
  };
  const problem = missing
    ? t(step === "email" ? "auth.signIn.missingEmail" : "auth.signIn.missingPassword")
    : signIn.isError
      ? errors.message(signIn.error)
      : "";

  return (
    <LoginFrame
      title={step === "email" ? t("auth.signIn.title", { ns: "common" }) : t("auth.signIn.passwordTitle")}
      sub={step === "email" ? t("auth.signIn.description") : t("auth.signIn.as", { email: email.trim() })}
      shape={signIn.isSuccess ? "enter" : "start"}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      {step === "email" ? (
        <EmailStep
          email={email}
          problem={problem}
          onInput={(value) => {
            setEmail(value);
            setMissing(false);
            hear({ type: "email", len: value.trim().length, ok: looksLikeEmail(value.trim()) });
          }}
        />
      ) : (
        <PasswordStep
          email={email}
          problem={problem}
          pending={signIn.isPending}
          input={password}
          onInput={() => setMissing(false)}
          onBack={() => {
            signIn.reset();
            setMissing(false);
            setStep("email");
          }}
        />
      )}
    </LoginFrame>
  );
}

// The first step: the address, and the ways in that need none (providers, passkeys), and the way to register.
function EmailStep({ email, problem, onInput }: { email: string; problem: string; onInput: (value: string) => void }) {
  const { t } = useTranslation();
  return (
    <>
      <LoginEmail
        t={String}
        text={{ label: t("auth.email", { ns: "common" }) }}
        values={{
          id: "sign-in-email",
          name: "email",
          type: "email",
          autoComplete: "username",
          placeholder: "name@company.com",
          value: email,
          fieldCls: problem ? "bad" : "",
          describedBy: problem ? "sign-in-problem" : "",
          onInput: (event) => onInput((event.currentTarget as HTMLInputElement).value),
        }}
      />
      <FormProblem id="sign-in-problem" message={problem} />
      <LoginPrimary t={String} label={t("auth.signIn.next")} />
      <Methods />
      <Register />
    </>
  );
}

// The second step: the password, which stays in its field until it is sent, never in the page's state.
function PasswordStep({
  email,
  problem,
  pending,
  input,
  onInput,
  onBack,
}: {
  email: string;
  problem: string;
  pending: boolean;
  input: RefObject<HTMLInputElement | null>;
  onInput: () => void;
  onBack: () => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      {/* the address the password belongs to, for the browser's password manager */}
      <input type="email" name="email" autoComplete="username" value={email} readOnly hidden />
      <LoginField
        t={String}
        text={{ label: t("auth.password", { ns: "common" }) }}
        values={{
          id: "sign-in-password",
          name: "password",
          type: "password",
          autoComplete: "current-password",
          fieldCls: problem ? "bad" : "",
          describedBy: problem ? "sign-in-problem" : "",
          input: (element) => {
            input.current = element as HTMLInputElement | null;
          },
          onInput,
        }}
      />
      <FormProblem id="sign-in-problem" message={problem} />
      <LoginPrimary t={String} label={t("auth.signIn.submit", { ns: "common" })} values={{ disabled: pending }} />
      <NoteLink label={t("auth.reset.forgot")} to="/reset-password" />
      <NoteAction label={t("auth.signIn.changeEmail")} onPress={onBack} />
    </>
  );
}

// Methods are the ways in besides the password, beside the first step. A provider is a browser round trip; a passkey
// is a WebAuthn assertion the server checks against the public key it stored.
function Methods() {
  const { t } = useTranslation();
  const errors = useErrors();
  const site = useQuery(siteQuery);
  const search = useSearch({ strict: false });
  const oauth = useOAuthStart();
  const passkey = usePasskeySignIn("signIn");
  const providers = site.data?.oauth_providers ?? [];
  const passkeys = site.data?.passkeys ?? false;
  const failure =
    search.oauth === "failed"
      ? t("auth.signIn.oauthFailed")
      : oauth.failure
        ? errors.message(oauth.failure)
        : passkey.failure
          ? errors.message(passkey.failure)
          : "";
  if (!passkeys && providers.length === 0) {
    return <FormProblem message={failure} />;
  }
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      <LoginStack t={String}>
        {providers.map((provider) => (
          <LoginButton
            key={provider}
            t={String}
            label={t(`auth.signIn.providers.${provider}`)}
            values={{ disabled: oauth.pending, onPress: () => void oauth.start(provider) }}
          />
        ))}
        {passkeys ? (
          <LoginButton
            t={String}
            label={t("auth.signIn.passkey")}
            values={{ disabled: passkey.pending, onPress: () => void passkey.start() }}
          />
        ) : null}
      </LoginStack>
      <FormProblem message={failure} />
    </>
  );
}

// Register offers the registration page, keeping where the visitor was going.
function Register() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false });
  const next = search.next;
  const RegisterLink = useMemo(
    () =>
      ({ href: _, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) => (
        <Link to="/register" search={{ next, invite: undefined }} {...props} />
      ),
    [next],
  );
  return (
    <LoginNote t={String}>
      {t("auth.signIn.noAccount")}{" "}
      <LoginLink t={String} link={RegisterLink} label={t("auth.signIn.register")} values={{ to: "/register" }} />
    </LoginNote>
  );
}

function useOAuthStart() {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  async function start(provider: Schemas["OAuthProvider"]) {
    setPending(true);
    setFailure(null);
    try {
      const { authorize_url } = await unwrap(
        consoleApi.POST("/api/console/v1/auth/oauth/start", { body: { provider } }),
      );
      window.location.assign(authorize_url);
    } catch (error) {
      setFailure(error);
      setPending(false);
    }
  }
  return { start, pending, failure };
}
