import { ApiError, consoleApi, unwrapEmpty } from "@axiom/api";
import { useErrors } from "@axiom/app";
import LoginEmail from "@axiom/boards/Login/regions/LoginEmail";
import LoginField from "@axiom/boards/Login/regions/LoginField";
import LoginPrimary from "@axiom/boards/Login/regions/LoginPrimary";
import { useMutation } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { FormProblem, LoginFrame, NoteAction, NoteLink } from "./login-frame";

// The reset email links here with its single-use token in the fragment, which the browser does not send to the server
// (docs/frontend-api.md「注册与找回密码」). The page keeps the token in memory, takes it out of the address at once (so
// it is not left in the history or copied with the link), and sends it only to resetPassword. Without a token the page
// asks for the email instead. A reset ends every session and does not sign in: the next stop is the sign-in page.
export default function ResetPassword() {
  const [token, setToken] = useState(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "");
  useEffect(() => {
    if (window.location.hash)
      history.replaceState(history.state, "", window.location.pathname + window.location.search);
  }, []);
  return token ? <NewPassword token={token} onAgain={() => setToken("")} /> : <RequestReset />;
}

const language = (resolved: string) => (resolved.startsWith("en") ? "en" : "zh");

function RequestReset() {
  const { t, i18n } = useTranslation();
  const errors = useErrors();
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
  if (request.isSuccess) {
    return (
      <LoginFrame
        title={t("auth.reset.sentTitle")}
        sub={t("auth.reset.sent")}
        shape="enter"
        onSubmit={(e) => e.preventDefault()}
      >
        <NoteLink label={t("auth.reset.back")} to="/sign-in" />
      </LoginFrame>
    );
  }
  const problem = missing ? t("auth.signIn.missingEmail") : request.isError ? errors.message(request.error) : "";
  return (
    <LoginFrame
      title={t("auth.reset.title")}
      sub={t("auth.reset.description")}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        const address = email.trim();
        setMissing(!address);
        if (address && !request.isPending) request.mutate(address);
      }}
    >
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
          onInput: (event) => {
            setEmail((event.currentTarget as HTMLInputElement).value);
            setMissing(false);
          },
        }}
      />
      <FormProblem id="reset-problem" message={problem} />
      <LoginPrimary t={String} label={t("auth.reset.submit")} values={{ disabled: request.isPending }} />
      <NoteLink label={t("auth.reset.back")} to="/sign-in" />
    </LoginFrame>
  );
}

// A 422 other than a weak password is the server's one answer for a wrong, expired, spent or superseded link.
const invalidLink = (error: unknown) =>
  error instanceof ApiError && error.status === 422 && error.code !== "weak_password";

function NewPassword({ token, onAgain }: { token: string; onAgain: () => void }) {
  const { t, i18n } = useTranslation();
  const errors = useErrors();
  const password = useRef<HTMLInputElement | null>(null);
  const [missing, setMissing] = useState(false);
  const reset = useMutation({
    mutationFn: (secret: string) =>
      unwrapEmpty(
        consoleApi.POST("/api/console/v1/auth/password-reset/complete", {
          body: { token, new_password: secret, language: language(i18n.resolvedLanguage ?? i18n.language) },
        }),
      ),
  });
  if (reset.isSuccess) {
    return (
      <LoginFrame
        title={t("auth.reset.doneTitle")}
        sub={t("auth.reset.done")}
        shape="enter"
        onSubmit={(e) => e.preventDefault()}
      >
        <NoteLink label={t("auth.reset.signIn")} to="/sign-in" />
      </LoginFrame>
    );
  }
  const invalid = invalidLink(reset.error);
  const problem = missing
    ? t("auth.signIn.missingPassword")
    : invalid
      ? t("auth.reset.invalid")
      : reset.isError
        ? errors.message(reset.error)
        : "";
  return (
    <LoginFrame
      title={t("auth.reset.newTitle")}
      sub={t("auth.reset.newDescription")}
      shape="start"
      onSubmit={(event) => {
        event.preventDefault();
        const secret = password.current?.value ?? "";
        setMissing(!secret);
        if (secret && !reset.isPending) reset.mutate(secret);
      }}
    >
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
            password.current = element as HTMLInputElement | null;
          },
          onInput: () => setMissing(false),
        }}
      />
      <FormProblem id="reset-problem" message={problem} />
      <LoginPrimary t={String} label={t("auth.reset.newSubmit")} values={{ disabled: reset.isPending }} />
      {invalid ? <NoteAction label={t("auth.reset.again")} onPress={onAgain} /> : null}
    </LoginFrame>
  );
}
