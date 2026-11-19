import { ApiError, consoleApi, unwrapEmpty } from "@axiom/api";
import { useCachedSession, useErrors, useReadSession } from "@axiom/app";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useSearch } from "@tanstack/react-router";
import { type RefObject, useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Attempt, useAttempt } from "./attempt";
import { LoginFrame, type Stage, useStage } from "./login-frame";
import {
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

// The reset email links here with its single-use token in the fragment, which the browser does not send to the server
// (docs/frontend-api.md「注册与找回密码」). The page keeps the token in memory, takes it out of the address at once (so
// it is not left in the history or copied with the link), and sends it only to resetPassword. Without a token the page
// asks for the email instead. A reset ends the sessions of its account and does not sign in. The page has no gate: a
// link works in a browser signed in as anyone, and the way on finds out as whom (Onward).
export default function ResetPassword() {
  useLoginFace();
  const [link, again] = useLinkToken();
  return link.token ? <NewPassword key={link.n} token={link.token} onAgain={again} /> : <RequestReset key={link.n} />;
}

const readToken = () => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "";
const clearFragment = () => {
  if (window.location.hash) history.replaceState(history.state, "", window.location.pathname + window.location.search);
};

// useLinkToken is the link's token: read when the page opens, and again when a link is opened in this same tab, which
// changes only the fragment and so does not load the page anew. Each new token starts its form afresh (n).
function useLinkToken() {
  const [link, setLink] = useState(() => ({ token: readToken(), n: 0 }));
  useEffect(() => {
    clearFragment();
    const onHash = () => {
      const token = readToken();
      clearFragment();
      if (token) setLink((previous) => ({ token, n: previous.n + 1 }));
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return [link, () => setLink((previous) => ({ token: "", n: previous.n + 1 }))] as const;
}

const useNext = () => useSearch({ strict: false }).next;

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

// The email step, and once its request is accepted, the step that says to check the mail. The server answers every
// address alike, so the page cannot say more than that.
function RequestReset() {
  const { t } = useTranslation();
  const errors = useErrors();
  const stage = useStage();
  const attempt = useAttempt();
  const language = useMailLanguage();
  const next = useNext();
  const input = useRef<HTMLInputElement | null>(null);
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const [sent, setSent] = useState(false);
  const failed = attempt.failure?.kind === "request" ? attempt.failure.error : null;
  const cooling = useCooldown(failed);
  const blocked = attempt.kind !== null || cooling.waiting;
  if (sent) return <Sent attempt={attempt} stage={stage} next={next} onAgain={() => setSent(false)} />;
  const refused = fieldRefused(failed, "/email");
  const failure = refused ? t("auth.signIn.badEmail") : failed && !cooling.over ? errors.message(failed) : "";
  const send = (address: string) => () =>
    unwrapEmpty(consoleApi.POST("/api/console/v1/auth/password-reset", { body: { email: address, language } })).then(
      () => setSent(true),
    );
  return (
    <LoginFrame
      title={t("auth.reset.title")}
      sub={t("auth.reset.description")}
      step="request"
      shape="start"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => {
        event.preventDefault();
        // the address the field holds, which a password manager may have written without a word to the page
        const typed = input.current?.value ?? email;
        const address = typed.trim();
        setEmail(typed);
        setMissing(!address);
        if (address && !blocked) attempt.run("request", send(address), { onFail: () => stage.hear({ type: "bad" }) });
      }}
    >
      <ResetEmail
        email={email}
        input={input}
        problem={missing ? t("auth.signIn.missingEmail") : failure}
        onInput={(value) => {
          setEmail(value);
          setMissing(false);
          if (refused) attempt.dismiss();
          hearAddress(stage, value);
        }}
      />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.reset.submit")}</span>}
        values={{ disabled: blocked, style: primaryLook(blocked) }}
      />
      <RouteNote to="/sign-in" label={t("auth.reset.back")} next={next} />
    </LoginFrame>
  );
}

function ResetEmail({
  email,
  input,
  problem,
  onInput,
}: {
  email: string;
  input: RefObject<HTMLInputElement | null>;
  problem: string;
  onInput: (value: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <LoginEmail
        t={String}
        text={{ label: t("auth.email", { ns: "common" }) }}
        values={{
          id: "reset-email",
          name: "email",
          type: "email",
          autoComplete: "username",
          placeholder: "name@company.com",
          value: email,
          fieldCls: problem ? "bad" : "",
          describedBy: problem ? "reset-problem" : "",
          invalid: problem ? true : undefined,
          input: (element) => {
            input.current = element as HTMLInputElement | null;
          },
          onInput: (event) => onInput((event.currentTarget as HTMLInputElement).value),
        }}
      />
      <FormProblem id="reset-problem" message={problem} />
    </>
  );
}

// The request is accepted: the mail may come. The form is one step back, with the address, to send again or to another.
function Sent({
  attempt,
  stage,
  next,
  onAgain,
}: {
  attempt: Attempt;
  stage: Stage;
  next?: string;
  onAgain: () => void;
}) {
  const { t } = useTranslation();
  return (
    <LoginFrame
      title={t("auth.reset.sentTitle")}
      sub={t("auth.reset.sent")}
      step="sent"
      shape="enter"
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => event.preventDefault()}
    >
      <NoteAction label={t("auth.reset.resend")} onPress={onAgain} />
      <RouteNote to="/sign-in" label={t("auth.reset.back")} next={next} />
    </LoginFrame>
  );
}

type After = "done" | "invalid" | "unknown";

// The link's step: the new password, and once the reset is over, the way on. The page's requests (attempt.ts) are the
// reset and, on the way on, a sign-out.
function NewPassword({ token, onAgain }: { token: string; onAgain: () => void }) {
  const stage = useStage();
  const attempt = useAttempt();
  const [after, setAfter] = useState<After | null>(null);
  if (after) return <Onward after={after} attempt={attempt} stage={stage} onAgain={onAgain} />;
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
  const failure = !failed || cooling.over ? "" : badLength(failed) ? t("auth.reset.length") : errors.message(failed);
  const problem = missing ? t("auth.signIn.missingPassword") : failure;
  const reset = (secret: string) => () =>
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
        attempt.run("reset", reset(secret), {
          onFail: (error) => {
            const next = afterFailure(error);
            if (next) onAfter(next);
            else stage.hear({ type: "bad" });
          },
        });
      }}
    >
      <NewPasswordField problem={problem} input={password} onInput={() => setMissing(false)} />
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
  input,
  onInput,
}: {
  problem: string;
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
        fieldCls: problem ? "bad" : "",
        describedBy: problem ? "reset-problem" : "",
        invalid: problem ? true : undefined,
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
// A reset ends the sessions of the account it was for, not of another one this browser may be signed in as, and the
// sign-in page would open that one's console. So the way to sign in is offered only once the browser is known to be
// signed out (useBrowserSession); signed in, the step says as whom and offers to sign out first; a read that fails is
// offered again. Not knowing is never signed out.
function Onward({
  after,
  attempt,
  stage,
  onAgain,
}: {
  after: After;
  attempt: Attempt;
  stage: Stage;
  onAgain: () => void;
}) {
  const { t, i18n } = useTranslation();
  const next = useNext();
  const browser = useBrowserSession();
  const words = afterWords[after];
  const said = {
    reading: "",
    failed: t("auth.reset.unsure"),
    in: t("auth.reset.signedIn", { email: browser.email }),
    out: "",
  }[browser.state];
  const sentences = [t(words.sub), said].filter(Boolean);
  return (
    <LoginFrame
      title={t(words.title)}
      sub={sentences.join((i18n.resolvedLanguage ?? i18n.language).startsWith("en") ? " " : "")}
      subStyle={wrapAnywhere}
      step={after}
      shape={after === "done" ? "enter" : "start"}
      attempt={attempt}
      stage={stage}
      onSubmit={(event) => event.preventDefault()}
    >
      {browser.state === "out" ? <RouteNote to="/sign-in" label={t(words.signIn)} next={next} /> : null}
      {browser.state === "in" ? <SignOutNote label={t("auth.reset.signOut")} attempt={attempt} /> : null}
      {browser.state === "failed" ? <NoteAction label={t("auth.reset.check")} onPress={browser.check} /> : null}
      {after === "done" ? null : <NoteAction label={t("auth.reset.again")} onPress={onAgain} />}
    </LoginFrame>
  );
}

// useBrowserSession is who this browser is signed in as, read afresh when the way on opens: the same read a sign-in
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
  const state = known === "read" ? (session ? "in" : "out") : known;
  return { state, email: session?.user.email ?? "", check } as const;
}
