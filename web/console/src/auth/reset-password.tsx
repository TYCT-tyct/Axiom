import { ApiError, consoleApi, unwrapEmpty } from "@axiom/api";
import { useCachedSession, useErrors, useReadSession } from "@axiom/app";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useSearch } from "@tanstack/react-router";
import { type RefObject, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { type Attempt, useAttempt } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import {
  EmailField,
  FormProblem,
  fieldRefused,
  hearAddress,
  NoteAction,
  primaryLook,
  RouteNote,
  SignOutNote,
  useLoginFace,
  wrapAnywhere,
} from "./login-parts";
import { dropResetLink, onResetLink, resetLink } from "./reset-link";

// The reset email links here with its single-use token, which the route has already taken out of the address and kept
// (reset-link.ts); the page sends it only to resetPassword. Without a token the page asks for the email instead. A
// reset ends the sessions of its account and does not sign in. The page has no gate: a link works in a browser signed
// in as anyone, and the way on finds out as whom (Onward). Each new link starts its form afresh.
export default function ResetPassword() {
  useLoginFace();
  const link = useSyncExternalStore(onResetLink, resetLink);
  return link.token ? <NewPassword key={link.n} token={link.token} /> : <RequestReset key={link.n} />;
}

// The language the mail is written in: the page's.
function useMailLanguage() {
  const { i18n } = useTranslation();
  return (i18n.resolvedLanguage ?? i18n.language).startsWith("en") ? "en" : "zh";
}

// useCooldown is the wait a limit's 429 asks for (Retry-After): sending sooner is refused the same way, so the step's
// submit waits until then, and then the refusal is over and its words go with it.
function useCooldown(error: unknown) {
  const limit = error instanceof ApiError && error.status === 429 ? error : null;
  const [over, setOver] = useState<ApiError | null>(null);
  useEffect(() => {
    if (!limit) return;
    const timer = window.setTimeout(() => setOver(limit), (limit.retryAfter ?? 60) * 1000);
    return () => window.clearTimeout(timer);
  }, [limit]);
  return { waiting: limit !== null && over !== limit, over: limit !== null && over === limit };
}

// An address the server does not take: the contract's guard names the field (400), and the server's own check answers
// 422 unprocessable (cmd/axiom/console_errors.go, ErrBadEmail), the endpoint's only one.
const badEmail = (error: unknown) =>
  fieldRefused(error, "/email") ||
  (error instanceof ApiError && error.status === 422 && error.code === "unprocessable");

// The email step, and once its request is accepted, the step that says to check the mail. The server answers every
// address alike, so the page cannot say more than that. Both steps' way back to sign in is the one the browser's session
// decides (WayOn), read when the page opens.
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
      <Sent
        attempt={attempt}
        stage={stage}
        browser={browser}
        said={said}
        onAgain={() => editAgain(() => setSent(false))}
      />
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

// useRequestForm is the email step's state: the address, what is wrong with it or with the request, and the request.
function useRequestForm(stage: Stage, attempt: Attempt, onSent: () => void) {
  const { t } = useTranslation();
  const errors = useErrors();
  const language = useMailLanguage();
  const input = useRef<HTMLInputElement | null>(null);
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const failed = attempt.failure?.kind === "request" ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const blocked = attempt.kind !== null || cooling.waiting;
  const refused = badEmail(failed);
  const failure = refused ? t("auth.signIn.badEmail") : failed && !cooling.over ? errors.message(failed) : "";
  const send = (address: string) =>
    unwrapEmpty(consoleApi.POST("/api/console/v1/auth/password-reset", { body: { email: address, language } })).then(
      onSent,
    );
  return {
    input,
    email,
    blocked,
    problem: missing ? t("auth.signIn.missingEmail") : failure,
    invalid: missing || refused,
    submit: () => {
      // the address the field holds, which a password manager may have written without a word to the page
      const typed = input.current?.value ?? email;
      const address = typed.trim();
      setEmail(typed);
      setMissing(!address);
      if (address && !blocked)
        attempt.run("request", () => send(address), { onFail: () => stage.hear({ type: "bad" }) });
    },
    type: (value: string) => {
      setEmail(value);
      setMissing(false);
      if (refused) attempt.dismiss();
      hearAddress(stage, value);
    },
  };
}

// useFocusAgain is the way back to a form whose step had left it: the step goes (leave), and once the form is on
// screen again its field takes the focus, so the address kept there can be edited at once.
function useFocusAgain(input: RefObject<HTMLInputElement | null>) {
  const [back, setBack] = useState(0);
  useEffect(() => {
    if (back > 0) input.current?.focus();
  }, [input, back]);
  return (leave: () => void) => {
    leave();
    setBack((n) => n + 1);
  };
}

// The request is accepted: the mail may come. The form is one step back, with the address, to send again or to another.
function Sent({
  attempt,
  stage,
  browser,
  said,
  onAgain,
}: {
  attempt: Attempt;
  stage: Stage;
  browser: BrowserSession;
  said: Said;
  onAgain: () => void;
}) {
  const { t } = useTranslation();
  return (
    <LoginFrame
      title={t("auth.reset.sentTitle")}
      sub={said(t("auth.reset.sent"))}
      subStyle={wrapAnywhere}
      step="sent"
      shape="enter"
      attempt={attempt}
      stage={stage}
      answer
      onSubmit={(event) => event.preventDefault()}
    >
      <NoteAction label={t("auth.reset.resend")} onPress={onAgain} />
      <WayOn browser={browser} signIn={t("auth.reset.back")} attempt={attempt} />
    </LoginFrame>
  );
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
// superseded is a 422 unprocessable; a reset whose answer never came (no response, or a gateway's page without the
// contract's error) may have gone through. Either way the form has nothing more to do. Any other refusal is the form's
// to say: the password's length, a limit, the service.
function afterFailure(error: unknown): After | null {
  if (!(error instanceof ApiError)) return "unknown";
  if (error.status === 422 && error.code === "unprocessable") return "invalid";
  return error.status === 0 || (error.status >= 500 && error.code === undefined) ? "unknown" : null;
}

// A password outside 12 to 256 characters: the contract's guard names the field (400), the server's own check, which
// counts the same, answers weak_password.
const badLength = (error: unknown) =>
  fieldRefused(error, "/new_password") || (error instanceof ApiError && error.code === "weak_password");

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
  const errors = useErrors();
  const language = useMailLanguage();
  const password = useRef<HTMLInputElement | null>(null);
  const [missing, setMissing] = useState(false);
  const failed = attempt.failure?.kind === "reset" ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const blocked = attempt.kind !== null || cooling.waiting;
  const tooLong = badLength(failed);
  const failure = !failed || cooling.over ? "" : tooLong ? t("auth.reset.length") : errors.message(failed);
  const problem = missing ? t("auth.signIn.missingPassword") : failure;
  const reset = (secret: string) =>
    unwrapEmpty(
      consoleApi.POST("/api/console/v1/auth/password-reset/complete", {
        body: { token, new_password: secret, language },
      }),
    ).then(() => onAfter("done"));
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
        const secret = password.current?.value ?? "";
        setMissing(!secret);
        if (!secret || blocked) return;
        // not cancellable: a reset sent may end this browser's session
        attempt.run("reset", () => reset(secret), {
          onFail: (error) => {
            const next = afterFailure(error);
            if (next) onAfter(next);
            else stage.hear({ type: "bad" });
          },
        });
      }}
    >
      <NewPasswordField
        problem={problem}
        invalid={missing || tooLong}
        input={password}
        onInput={() => {
          setMissing(false);
          // the password's refusal is about what was typed; a limit's wait is not, and stays
          if (tooLong) attempt.dismiss();
        }}
      />
      <FormProblem id="reset-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.reset.newSubmit")}</span>}
        values={{ disabled: blocked, style: primaryLook(blocked) }}
      />
    </LoginFrame>
  );
}

function NewPasswordField({
  problem,
  invalid,
  input,
  onInput,
}: {
  problem: string;
  invalid: boolean;
  input: RefObject<HTMLInputElement | null>;
  onInput: () => void;
}) {
  const { t } = useTranslation();
  return (
    <LoginField
      t={String}
      text={{ label: t("auth.reset.newPassword") }}
      values={{
        id: "reset-password",
        name: "password",
        type: "password",
        autoComplete: "new-password",
        fieldCls: invalid ? "bad" : "",
        describedBy: problem ? "reset-problem" : "",
        invalid: invalid || undefined,
        input: (element) => {
          input.current = element as HTMLInputElement | null;
        },
        onInput,
      }}
    />
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
  const browser = useBrowserSession();
  const said = useSaid(browser);
  const words = afterWords[after];
  return (
    <LoginFrame
      title={t(words.title)}
      sub={said(t(words.sub))}
      subStyle={wrapAnywhere}
      step={after}
      shape={after === "done" ? "enter" : "start"}
      attempt={attempt}
      stage={stage}
      answer
      onSubmit={(event) => event.preventDefault()}
    >
      <WayOn browser={browser} signIn={t(words.signIn)} attempt={attempt} />
      {after === "done" ? null : <NoteAction label={t("auth.reset.again")} onPress={dropResetLink} />}
    </LoginFrame>
  );
}

// Every way from this page to sign in. A reset ends the sessions of the account it was for, not of another one this
// browser may be signed in as, and the sign-in page opens the console of whoever is signed in. So the way to sign in is
// offered only once the browser is known to be signed out (useBrowserSession); otherwise the step says what the browser
// holds, truthfully (useSaid), and offers what fits it: signed in, to sign out first; a sign-in waiting for its second
// step, to finish it or end it; unread, to read again. Not knowing is never signed out.
function WayOn({ browser, signIn, attempt }: { browser: BrowserSession; signIn: string; attempt: Attempt }) {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  if (browser.state === "out") return <RouteNote to="/sign-in" label={signIn} next={next} />;
  if (browser.state === "in") return <SignOutNote label={t("auth.reset.signOut")} attempt={attempt} />;
  if (browser.state === "failed") return <NoteAction label={t("auth.reset.check")} onPress={browser.check} />;
  if (browser.state === "reading") return null;
  return (
    <>
      <RouteNote to="/mfa" label={t("auth.reset.goOn")} next={next} />
      <SignOutNote label={t("auth.reset.endPending")} attempt={attempt} />
    </>
  );
}

type BrowserSession = ReturnType<typeof useBrowserSession>;
type Said = ReturnType<typeof useSaid>;

// useSaid puts what the browser holds after a step's own sentence, in the step's line under its title, announced as it
// changes: that a read is out, whom the browser is signed in as, or that it could not be told.
function useSaid(browser: BrowserSession) {
  const { t, i18n } = useTranslation();
  const said = {
    reading: t("auth.reset.checking"),
    failed: t("auth.reset.unsure"),
    in: t("auth.reset.signedIn", { email: browser.email }),
    pending: t("auth.reset.pending", { email: browser.email }),
    out: "",
  }[browser.state];
  const gap = (i18n.resolvedLanguage ?? i18n.language).startsWith("en") ? " " : "";
  return (own: string) => <span role="status">{[own, said].filter(Boolean).join(gap)}</span>;
}

// useBrowserSession is who this browser is signed in as, read afresh when the step opens: the same read a sign-in
// confirms its session with (useReadSession), after which the cache follows the session (a sign-out here empties it).
// Only the latest read decides.
function useBrowserSession() {
  const read = useReadSession();
  const session = useCachedSession();
  const [known, setKnown] = useState<"reading" | "failed" | "read">("reading");
  const latest = useRef(0);
  const check = useCallback(() => {
    const n = ++latest.current;
    setKnown("reading");
    read().then(
      () => latest.current === n && setKnown("read"),
      () => latest.current === n && setKnown("failed"),
    );
  }, [read]);
  useEffect(check, [check]);
  const held = !session ? "out" : session.level === "pending_mfa" ? "pending" : "in";
  const state = known === "read" ? held : known;
  return { state, email: session?.user.email ?? "", check } as const;
}
