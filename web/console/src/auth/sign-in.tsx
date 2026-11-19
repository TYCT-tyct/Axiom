import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { consoleAuth } from "@axiom/api/auth";
import { useErrors, useSignInEnded, useStartSession } from "@axiom/app";
import LoginButton from "@axiom/boards/Login/regions/LoginButton";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginOr from "@axiom/boards/Login/regions/LoginOr";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import LoginStack from "@axiom/boards/Login/regions/LoginStack";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { type RefObject, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { type Attempt, useAttempt } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import {
  FormProblem,
  fieldRefused,
  hearAddress,
  LoginGate,
  NoteAction,
  primaryLook,
  RouteNote,
  wrapAnywhere,
} from "./login-parts";
import { keepOAuthNext } from "./oauth-return";
import { usePasskey } from "./passkey-sign-in";

export default function SignIn() {
  const site = useQuery(siteQuery);
  const away = loginOriginOf(site.data);

  // A sign-in domain owns the provider callback and the passkey relying party. On another console domain the
  // password still works, but the visitors who need those two are sent to the domain that has them.
  useEffect(() => {
    if (away) window.location.replace(away + window.location.pathname + window.location.search);
  }, [away]);

  // Until the site's settings say whether this is the domain to sign in on, nothing is sent from it: a session set here
  // would not be the sign-in domain's. A site that cannot be read leaves the password to this domain.
  const ready = (site.isSuccess || site.isError) && !away;
  return (
    <LoginGate stage="guest">
      <SignInSteps ready={ready} />
    </LoginGate>
  );
}

// The sign-in domain to send this visitor to, or undefined to stay: another domain than this one, where the site has
// providers or passkeys, which only it can serve.
function loginOriginOf(site: Schemas["Site"] | undefined) {
  const origin = site?.login_origin;
  if (!origin || window.location.origin === new URL(origin).origin) return undefined;
  return (site.oauth_providers?.length ?? 0) > 0 || site.passkeys ? origin.replace(/\/$/, "") : undefined;
}

// An answer that names the email as what is wrong belongs to the first step.
const aboutEmail = (error: unknown) => fieldRefused(error, "/email");

// SignInSteps asks for the email, then the password, and sends both in one signIn: the two steps are only how the
// page asks (port design §9). The server's answer moves the visitor on through the gate: to the second factor, to
// consent, or back to where they were going.
function SignInSteps({ ready }: { ready: boolean }) {
  const { t } = useTranslation();
  const stage = useStage();
  const flow = useSignInFlow(stage, ready);
  const ended = useSignInEnded();
  const emailStep = flow.step === "email";
  return (
    <LoginFrame
      title={emailStep ? t("auth.signIn.title", { ns: "common" }) : t("auth.signIn.passwordTitle")}
      sub={
        !emailStep
          ? t("auth.signIn.as", { email: flow.email.trim() })
          : ended
            ? t("auth.signIn.ended", { ns: "common" })
            : t("auth.signIn.description")
      }
      subStyle={emailStep ? undefined : wrapAnywhere}
      step={flow.step}
      shape={flow.attempt.done === "password" ? "enter" : "start"}
      attempt={flow.attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        flow.submit();
      }}
    >
      {emailStep ? <EmailStep flow={flow} /> : <PasswordStep flow={flow} />}
    </LoginFrame>
  );
}

type Flow = ReturnType<typeof useSignInFlow>;

// useSignInFlow holds the two steps' state: the address typed, which step is asked, and the page's request
// (attempt.ts), the password among the others. A refusal that names the email takes the visitor back to the email,
// where the refused address stays until it is edited. A provider's failed return (?oauth=failed) is said once: it
// leaves the address, and the first request that starts replaces it.
function useSignInFlow(stage: Stage, ready: boolean) {
  const { t } = useTranslation();
  const errors = useErrors();
  const start = useStartSession();
  const { attempt, returned } = useSignInAttempt();
  const [step, setStep] = useState<"email" | "password">("email");
  const [missing, setMissing] = useState(false);
  const password = useRef<HTMLInputElement | null>(null);
  const failed = attempt.failure?.kind === "password" ? attempt.failure.error : null;
  const refused = aboutEmail(failed);
  const { email, emailInput, typeEmail } = useAddress(stage, attempt, refused, () => setMissing(false));
  // The email step takes the address the field holds when it is sent, which a password manager may have written
  // without a word to the page; that is the one the password step shows and signs in.
  const confirmAddress = () => {
    const typed = emailInput.current?.value ?? email;
    if (typed !== email) typeEmail(typed);
    setMissing(!typed.trim());
    if (ready && typed.trim() && (!refused || typed !== email)) setStep("password");
  };
  const submit = () => {
    if (step === "email") return confirmAddress();
    const address = email.trim();
    const secret = password.current?.value ?? "";
    setMissing(!secret);
    if (!ready || !secret) return;
    // not cancellable: a sign-in sent may set its cookie, and its answer is the session the page then shows
    attempt.run(
      "password",
      async () => {
        const grant = await consoleAuth.signIn({ email: address, password: secret });
        await start(grant.csrf_token);
      },
      {
        onFail: (error) => {
          if (aboutEmail(error)) setStep("email");
          stage.hear({ type: "bad" });
        },
      },
    );
  };
  const missingText = t(step === "email" ? "auth.signIn.missingEmail" : "auth.signIn.missingPassword");
  const failure = refused ? t("auth.signIn.badEmail") : failed ? errors.message(failed) : "";
  return {
    step,
    email,
    attempt,
    ready,
    returned,
    password,
    emailInput,
    submit,
    problem: missing ? missingText : failure,
    typeEmail,
    typePassword: () => setMissing(false),
    back: () => {
      attempt.dismiss();
      setMissing(false);
      setStep("email");
    },
  };
}

// useAddress holds the address the email step has heard, the field it is typed in, and what typing it does: it
// clears a refusal of the address before, and tells the stage how whole it looks.
function useAddress(stage: Stage, attempt: Attempt, refused: boolean, typed: () => void) {
  const [email, setEmail] = useState("");
  const emailInput = useRef<HTMLInputElement | null>(null);
  const typeEmail = (value: string) => {
    setEmail(value);
    typed();
    if (refused) attempt.dismiss();
    hearAddress(stage, value);
  };
  return { email, emailInput, typeEmail };
}

// The page's request (attempt.ts), and the provider's failed return, which is said until the first request starts and
// is taken out of the address so a reload does not say it again.
function useSignInAttempt() {
  const navigate = useNavigate();
  const own = useAttempt();
  const [returned, setReturned] = useState(useSearch({ strict: false }).oauth === "failed");
  useEffect(() => {
    if (!returned) return;
    void navigate({
      to: ".",
      search: (search: Record<string, unknown>) => ({ ...search, oauth: undefined }),
      replace: true,
    });
  }, [returned, navigate]);
  const attempt: Attempt = {
    ...own,
    run: (...request) => {
      setReturned(false);
      own.run(...request);
    },
  };
  return { attempt, returned };
}
// The first step: the address, and the ways in that need none (providers, passkeys), and the way to register.
function EmailStep({ flow }: { flow: Flow }) {
  const { t } = useTranslation();
  const { email, problem, attempt } = flow;
  const busy = attempt.kind !== null || !flow.ready;
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
          invalid: problem ? true : undefined,
          input: (element) => {
            flow.emailInput.current = element as HTMLInputElement | null;
          },
          onInput: (event) => flow.typeEmail((event.currentTarget as HTMLInputElement).value),
        }}
      />
      <FormProblem id="sign-in-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.signIn.next")}</span>}
        values={{ disabled: busy, style: primaryLook(busy) }}
      />
      <Methods attempt={attempt} returned={flow.returned} ready={flow.ready} />
      <Register />
    </>
  );
}

// The second step: the password, which stays in its field until it is sent, never in the page's state, and the way to
// reset a forgotten one.
function PasswordStep({ flow }: { flow: Flow }) {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  const busy = flow.attempt.kind !== null || !flow.ready;
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
        values={{ disabled: busy, style: primaryLook(busy) }}
      />
      <RouteNote to="/reset-password" label={t("auth.reset.forgot")} next={next} />
      <NoteAction label={t("auth.signIn.changeEmail")} onPress={flow.back} />
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
        invalid: problem ? true : undefined,
        input: (element) => {
          input.current = element as HTMLInputElement | null;
        },
        onInput,
      }}
    />
  );
}

// Methods are the ways in besides the password, beside the first step: a provider is a browser round trip, a passkey a
// WebAuthn assertion the server checks against the public key it stored. Both can be cancelled until they would sign
// in. The failure shown is the latest request's, or the provider's return until a request starts.
function Methods({ attempt, returned, ready }: { attempt: Attempt; returned: boolean; ready: boolean }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const site = useQuery(siteQuery);
  const search = useSearch({ strict: false });
  const passkey = usePasskey("signIn");
  const providers = site.data?.oauth_providers ?? [];
  const passkeys = site.data?.passkeys ?? false;
  const busy = attempt.kind !== null || !ready;
  const failure = attempt.failure && attempt.failure.kind !== "password" ? errors.message(attempt.failure.error) : "";
  const message = returned ? t("auth.signIn.oauthFailed") : failure;
  if (!passkeys && providers.length === 0) return <FormProblem message={message} />;
  return (
    <>
      <LoginOr t={String} text={{ label: t("auth.signIn.or") }} />
      <LoginStack t={String}>
        {providers.map((provider) => (
          <LoginButton
            key={provider}
            t={String}
            label={<span>{t(`auth.signIn.providers.${provider}`)}</span>}
            values={{
              disabled: busy,
              onPress: () =>
                attempt.run("oauth", (signal) => startOAuth(provider, search.next, signal), { cancellable: true }),
            }}
          />
        ))}
        {passkeys ? (
          <LoginButton
            t={String}
            label={<span>{t("auth.signIn.passkey")}</span>}
            values={{ disabled: busy, onPress: () => attempt.run("passkey", passkey, { cancellable: true }) }}
          />
        ) : null}
      </LoginStack>
      <FormProblem message={message} />
    </>
  );
}

// startOAuth asks for the provider's address and sends the browser there, keeping where the visitor was going for the
// way back. It never settles: the page stays as it is until the browser has left it, and a cancel stops the
// navigation still under way (a slow provider), and forgets where it was going, before the form is offered again.
async function startOAuth(provider: Schemas["OAuthProvider"], next: string | undefined, signal: AbortSignal) {
  const { authorize_url } = await unwrap(
    consoleApi.POST("/api/console/v1/auth/oauth/start", { body: { provider }, signal }),
  );
  signal.throwIfAborted();
  keepOAuthNext(next);
  signal.addEventListener(
    "abort",
    () => {
      window.stop();
      keepOAuthNext(undefined);
    },
    { once: true },
  );
  window.location.assign(authorize_url);
  await new Promise<never>(() => {});
}

// Register offers the registration page, keeping where the visitor was going.
function Register() {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  return <RouteNote to="/register" lead={t("auth.signIn.noAccount")} label={t("auth.signIn.register")} next={next} />;
}
