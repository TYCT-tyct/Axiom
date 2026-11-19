import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
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
import { type Attempt, type Attempts, useAttempts } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import { FormProblem, NoteAction, primaryLook, wrapAnywhere } from "./login-parts";
import { usePasskey } from "./passkey-sign-in";

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

// An answer that names the email as what is wrong (the contract's 400 with its pointer) belongs to the first step.
const aboutEmail = (error: unknown) =>
  error instanceof ApiError && error.fields.some(({ pointer }) => pointer === "/email");

// SignInSteps asks for the email, then the password, and sends both in one signIn: the two steps are only how the
// page asks (port design §9). The server's answer moves the visitor on through the gate: to the second factor, to
// consent, or back to where they were going.
function SignInSteps() {
  const { t } = useTranslation();
  const stage = useStage();
  const flow = useSignInFlow(stage);
  const emailStep = flow.step === "email";
  return (
    <LoginFrame
      title={emailStep ? t("auth.signIn.title", { ns: "common" }) : t("auth.signIn.passwordTitle")}
      sub={emailStep ? t("auth.signIn.description") : t("auth.signIn.as", { email: flow.email.trim() })}
      subStyle={emailStep ? undefined : wrapAnywhere}
      step={flow.step}
      shape={flow.signIn.isSuccess ? "enter" : "start"}
      busy={flow.attempts.kind !== null}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        flow.submit();
      }}
    >
      {emailStep ? (
        <EmailStep email={flow.email} problem={flow.problem} attempts={flow.attempts} onInput={flow.typeEmail} />
      ) : (
        <PasswordStep flow={flow} />
      )}
    </LoginFrame>
  );
}

type Flow = ReturnType<typeof useSignInFlow>;

// useSignInFlow holds the two steps' state: the address typed, which step is asked, and the page's attempts
// (attempt.ts), the password among them. A refusal that names the email takes the visitor back to the email, where the
// refused address stays until it is edited.
function useSignInFlow(stage: Stage) {
  const { t } = useTranslation();
  const errors = useErrors();
  const signIn = useSignIn();
  const attempts = useAttempts();
  const [step, setStep] = useState<"email" | "password">("email");
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const password = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    if (step === "password") password.current?.focus();
  }, [step]);
  const badEmail = aboutEmail(signIn.error);
  useEffect(() => {
    if (badEmail) setStep("email");
  }, [badEmail]);
  const submit = () => {
    const address = email.trim();
    setMissing(step === "email" ? !address : !password.current?.value);
    if (step === "email") {
      if (address && !badEmail && !attempts.kind) setStep("password");
      return;
    }
    const secret = password.current?.value ?? "";
    const attempt = secret ? attempts.begin("password") : null;
    if (!attempt) return;
    signIn.mutate(
      { email: address, password: secret },
      {
        onSuccess: attempt.done,
        onError: (error) => {
          attempt.fail(error);
          stage.hear({ type: "bad" });
        },
      },
    );
  };
  const missingText = t(step === "email" ? "auth.signIn.missingEmail" : "auth.signIn.missingPassword");
  const failure = badEmail ? t("auth.signIn.badEmail") : signIn.isError ? errors.message(signIn.error) : "";
  return {
    step,
    email,
    signIn,
    attempts,
    password,
    submit,
    problem: missing ? missingText : failure,
    typeEmail: (value: string) => {
      setEmail(value);
      setMissing(false);
      if (badEmail) signIn.reset();
      stage.hear({ type: "email", len: value.trim().length, ok: looksLikeEmail(value.trim()) });
    },
    typePassword: () => setMissing(false),
    back: () => {
      signIn.reset();
      setMissing(false);
      setStep("email");
    },
  };
}

// The first step: the address, and the ways in that need none (providers, passkeys), and the way to register.
function EmailStep({
  email,
  problem,
  attempts,
  onInput,
}: {
  email: string;
  problem: string;
  attempts: Attempts;
  onInput: (value: string) => void;
}) {
  const { t } = useTranslation();
  const busy = attempts.kind !== null;
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
      <LoginPrimary
        t={String}
        label={<span>{t("auth.signIn.next")}</span>}
        values={{ disabled: busy, style: primaryLook(busy) }}
      />
      <Methods attempts={attempts} />
      <Register />
    </>
  );
}

// The second step: the password, which stays in its field until it is sent, never in the page's state. While the
// sign-in is out the way back to the email is not offered: the answer, when it comes, signs in the address it was for.
function PasswordStep({ flow }: { flow: Flow }) {
  const { t } = useTranslation();
  const pending = flow.attempts.kind === "password";
  return (
    <>
      {/* the address the password belongs to, for the browser's password manager */}
      <input type="email" name="email" autoComplete="username" value={flow.email} readOnly hidden />
      <PasswordField
        label={t("auth.password", { ns: "common" })}
        problem={flow.problem}
        input={flow.password}
        onInput={flow.typePassword}
      />
      <FormProblem id="sign-in-problem" message={flow.problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.signIn.submit", { ns: "common" })}</span>}
        values={{ disabled: pending, style: primaryLook(pending) }}
      />
      {pending ? null : <NoteAction label={t("auth.signIn.changeEmail")} onPress={flow.back} />}
    </>
  );
}

function PasswordField({
  label,
  problem,
  input,
  onInput,
}: {
  label: string;
  problem: string;
  input: RefObject<HTMLInputElement | null>;
  onInput: () => void;
}) {
  return (
    <LoginField
      t={String}
      text={{ label }}
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
  );
}

// Methods are the ways in besides the password, beside the first step: a provider is a browser round trip, a passkey a
// WebAuthn assertion the server checks against the public key it stored. Each is one of the page's attempts, so while
// one is out every way in waits, and the failure shown is the latest attempt's, replacing an earlier one and the
// provider's return (?oauth=failed).
function Methods({ attempts }: { attempts: Attempts }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const site = useQuery(siteQuery);
  const search = useSearch({ strict: false });
  const passkey = usePasskey("signIn");
  const providers = site.data?.oauth_providers ?? [];
  const passkeys = site.data?.passkeys ?? false;
  const busy = attempts.kind !== null;
  const returned = search.oauth === "failed" && !attempts.failure && !busy;
  const failure =
    attempts.failure && attempts.failure.kind !== "password" ? errors.message(attempts.failure.error) : "";
  const message = returned ? t("auth.signIn.oauthFailed") : failure;
  if (!passkeys && providers.length === 0) return <FormProblem message={message} />;
  const run = (kind: string, go: (attempt: Attempt) => Promise<void>) => () => {
    const attempt = attempts.begin(kind);
    if (attempt) void go(attempt);
  };
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      <LoginStack t={String}>
        {providers.map((provider) => (
          <LoginButton
            key={provider}
            t={String}
            label={<span>{t(`auth.signIn.providers.${provider}`)}</span>}
            values={{ disabled: busy, onPress: run("oauth", (attempt) => startOAuth(attempt, provider)) }}
          />
        ))}
        {passkeys ? (
          <LoginButton
            t={String}
            label={<span>{t("auth.signIn.passkey")}</span>}
            values={{ disabled: busy, onPress: run("passkey", passkey) }}
          />
        ) : null}
      </LoginStack>
      <FormProblem message={message} />
    </>
  );
}

// How long a page that sent the browser to a provider waits for it to go before the attempt is over: a page still here
// was stopped (the browser's stop button) or failed to go, and its other ways in are offered again.
const stalled = 3000;

// startOAuth asks for the provider's address and sends the browser there. The attempt ends when the page is still here
// after `stalled`, or comes back from the back-forward cache (attempt.ts).
async function startOAuth(attempt: Attempt, provider: Schemas["OAuthProvider"]) {
  try {
    const { authorize_url } = await unwrap(
      consoleApi.POST("/api/console/v1/auth/oauth/start", { body: { provider }, signal: attempt.signal }),
    );
    if (!attempt.live()) return;
    window.location.assign(authorize_url);
    window.setTimeout(attempt.done, stalled);
  } catch (error) {
    attempt.fail(error);
  }
}

// Register offers the registration page, keeping where the visitor was going.
function Register() {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  const RegisterLink = useMemo(
    () =>
      ({ href: _, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) => (
        <Link to="/register" search={{ next, invite: undefined }} {...props} />
      ),
    [next],
  );
  return (
    <LoginNote t={String}>
      <span>{t("auth.signIn.noAccount")}</span>{" "}
      <LoginLink t={String} link={RegisterLink} label={t("auth.signIn.register")} values={{ to: "/register" }} />
    </LoginNote>
  );
}
