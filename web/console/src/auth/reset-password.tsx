import { ApiError, consoleApi, unwrapEmpty } from "@axiom/api";
import { useCurrentSession, useErrors, useSignOut } from "@axiom/app";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { LoginFrame } from "./login-frame";
import { FormProblem, NoteAction, NoteLink, useCooldown, WithEmail } from "./login-parts";

// The reset email links here with its single-use token in the fragment, which the browser does not send to the server
// (docs/frontend-api.md「注册与找回密码」). The page keeps the token in memory, takes it out of the address at once (so
// it is not left in the history or copied with the link), and sends it only to resetPassword. Without a token the page
// asks for the email instead. A reset ends every session and does not sign in: the next stop is the sign-in page.
export default function ResetPassword() {
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

const language = (resolved: string) => (resolved.startsWith("en") ? "en" : "zh");
const useNext = () => useSearch({ strict: false }).next;

function RequestReset() {
  const { t, i18n } = useTranslation();
  const errors = useErrors();
  const next = useNext();
  const [email, setEmail] = useState("");
  const [missing, setMissing] = useState(false);
  const request = useMutation({
    mutationFn: (address: string) =>
      unwrapEmpty(
        consoleApi.POST("/api/console/v1/auth/password-reset", {
          body: { email: address, language: language(i18n.resolvedLanguage ?? i18n.language) },
        }),
      ),
  });
  const waiting = useCooldown(request.error);
  if (request.isSuccess) {
    return (
      <LoginFrame
        title={t("auth.reset.sentTitle")}
        sub={t("auth.reset.sent")}
        step="sent"
        shape="enter"
        onSubmit={(e) => e.preventDefault()}
      >
        <NoteLink label={t("auth.reset.back")} to="/sign-in" next={next} />
      </LoginFrame>
    );
  }
  const problem = missing ? t("auth.signIn.missingEmail") : request.isError ? errors.message(request.error) : "";
  return (
    <LoginFrame
      title={t("auth.reset.title")}
      sub={t("auth.reset.description")}
      step="request"
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        const address = email.trim();
        setMissing(!address);
        if (address && !request.isPending && !waiting) request.mutate(address);
      }}
    >
      <ResetEmail
        email={email}
        problem={problem}
        onInput={(value) => {
          setEmail(value);
          setMissing(false);
        }}
      />
      <FormProblem id="reset-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.reset.submit")}</span>}
        values={{ disabled: request.isPending || waiting }}
      />
      <NoteLink label={t("auth.reset.back")} to="/sign-in" next={next} />
    </LoginFrame>
  );
}

function ResetEmail({ email, problem, onInput }: { email: string; problem: string; onInput: (value: string) => void }) {
  const { t } = useTranslation();
  return (
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
        onInput: (event) => onInput((event.currentTarget as HTMLInputElement).value),
      }}
    />
  );
}

// What a failed reset means for the visitor. The contract's guard refuses a short password with a 400 naming the
// field; the server's one answer for a wrong, expired, spent or superseded link is a 422 (not a weak password); and a
// reset whose answer never came may have gone through, so the visitor can try the new password as well as ask again.
function outcome(error: unknown) {
  if (error instanceof ApiError && error.fields.some(({ pointer }) => pointer === "/new_password")) return "short";
  if (error instanceof ApiError && error.status === 422 && error.code !== "weak_password") return "invalid";
  if (!(error instanceof ApiError) || error.status === 0 || error.status >= 500) return "unknown";
  return "refused";
}

// The words for a failed reset: its own for what the page can explain, the error's otherwise.
function resetProblem(
  result: ReturnType<typeof outcome> | null,
  error: unknown,
  t: (key: "auth.reset.tooShort" | "auth.reset.invalid" | "auth.reset.unknown") => string,
  message: (error: unknown) => string,
) {
  if (result === null) return "";
  if (result === "short") return t("auth.reset.tooShort");
  if (result === "invalid") return t("auth.reset.invalid");
  if (result === "unknown") return t("auth.reset.unknown");
  return message(error);
}

function useReset(token: string) {
  const { i18n } = useTranslation();
  return useMutation({
    mutationFn: (secret: string) =>
      unwrapEmpty(
        consoleApi.POST("/api/console/v1/auth/password-reset/complete", {
          body: { token, new_password: secret, language: language(i18n.resolvedLanguage ?? i18n.language) },
        }),
      ),
  });
}

function NewPassword({ token, onAgain }: { token: string; onAgain: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const next = useNext();
  const password = useRef<HTMLInputElement | null>(null);
  const [missing, setMissing] = useState(false);
  const reset = useReset(token);
  const waiting = useCooldown(reset.error);
  if (reset.isSuccess) return <ResetDone next={next} />;
  const result = reset.isError ? outcome(reset.error) : null;
  const problem = missing ? t("auth.signIn.missingPassword") : resetProblem(result, reset.error, t, errors.message);
  return (
    <LoginFrame
      title={t("auth.reset.newTitle")}
      sub={t("auth.reset.newDescription")}
      step="new"
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        const secret = password.current?.value ?? "";
        setMissing(!secret);
        if (secret && !reset.isPending && !waiting) reset.mutate(secret);
      }}
    >
      <NewPasswordField problem={problem} input={password} onInput={() => setMissing(false)} />
      <FormProblem id="reset-problem" message={problem} />
      <LoginPrimary
        t={String}
        label={<span>{t("auth.reset.newSubmit")}</span>}
        values={{ disabled: reset.isPending || waiting }}
      />
      {result === "invalid" || result === "unknown" ? (
        <>
          <NoteLink label={t("auth.reset.signInNew")} to="/sign-in" next={next} />
          <NoteAction label={t("auth.reset.again")} onPress={onAgain} />
        </>
      ) : null}
    </LoginFrame>
  );
}

function NewPasswordField({
  problem,
  input,
  onInput,
}: {
  problem: string;
  input: { current: HTMLInputElement | null };
  onInput: () => void;
}) {
  const { t } = useTranslation();
  return (
    <LoginField
      t={String}
      text={{ label: t("auth.password", { ns: "common" }) }}
      values={{
        id: "reset-password",
        name: "new-password",
        type: "password",
        autoComplete: "new-password",
        fieldCls: problem ? "bad" : "",
        describedBy: problem ? "reset-problem" : "",
        input: (element) => {
          input.current = element as HTMLInputElement | null;
        },
        onInput,
      }}
    />
  );
}

// The reset's last step. A reset ends the sessions of the account it was for, not another account this browser is
// signed in as: going on to sign in would then open that other account's console. So while a session is here the step
// says whose it is and offers to sign out of it first.
function ResetDone({ next }: { next?: string }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const navigate = useNavigate();
  const signOut = useSignOut();
  const current = useCurrentSession();
  const other = current.data;
  return (
    <LoginFrame
      title={t("auth.reset.doneTitle")}
      sub={
        other ? (
          <WithEmail sentence={(email) => t("auth.reset.stillSignedIn", { email })} email={other.user.email} />
        ) : (
          t("auth.reset.done")
        )
      }
      step="done"
      shape="enter"
      onSubmit={(event) => event.preventDefault()}
    >
      {current.isPending ? null : other ? (
        <>
          <NoteAction
            label={t("auth.reset.switchAccount")}
            onPress={() =>
              !signOut.isPending &&
              signOut.mutate(undefined, {
                onSuccess: () => void navigate({ to: "/sign-in", search: { next, invite: undefined } }),
              })
            }
          />
          <FormProblem
            message={signOut.error ? t("signOutFailed", { ns: "common", reason: errors.message(signOut.error) }) : ""}
          />
        </>
      ) : (
        <NoteLink label={t("auth.reset.signIn")} to="/sign-in" next={next} />
      )}
    </LoginFrame>
  );
}
