import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { AuthFrame, Gate, readForm, useErrors, useSignIn } from "@axiom/app";
import { DataState, FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";

export default function Register() {
  return (
    <Gate stage="guest">
      <RegisterPage />
    </Gate>
  );
}

function RegisterPage() {
  const { t } = useTranslation();
  const next = useSearch({ strict: false }).next;
  const site = useQuery(siteQuery);
  return (
    <AuthFrame
      brand={site.data}
      title={t("auth.register.title")}
      description={t("auth.register.description")}
      footer={
        <>
          {t("auth.register.haveAccount")}{" "}
          <Link to="/sign-in" search={{ next }} className="text-accent underline underline-offset-4">
            {t("auth.register.signIn")}
          </Link>
        </>
      }
    >
      <DataState query={site} compact>
        {(data) =>
          data.registration === "invite" ? <RegisterForm /> : <p className="text-t3">{t("auth.register.closed")}</p>
        }
      </DataState>
    </AuthFrame>
  );
}

function RegisterFields({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const invite = useSearch({ strict: false }).invite;
  return (
    <>
      <TextField
        label={t("auth.register.invite")}
        name="invite_code"
        defaultValue={invite}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        error={errors.field(error, "invite_code")}
      />
      <TextField
        label={t("auth.email", { ns: "common" })}
        name="email"
        autoComplete="username"
        inputMode="email"
        error={errors.field(error, "email")}
      />
      <TextField
        label={t("auth.password", { ns: "common" })}
        hint={t("auth.register.passwordHint")}
        name="password"
        type="password"
        autoComplete="new-password"
        error={errors.field(error, "password")}
      />
    </>
  );
}

// Registering does not sign the user in: the page signs in next, with the same credentials. When that step fails the
// account exists, so the form says to sign in instead of offering the registration again.
function RegisterForm() {
  const { t } = useTranslation();
  const errors = useErrors();
  const signIn = useSignIn();
  const register = useMutation({
    mutationFn: (body: Schemas["RegisterRequest"]) =>
      unwrap(consoleApi.POST("/api/console/v1/auth/register", { body })),
    onSuccess: (_registered, { email, password }) => signIn.mutate({ email, password }),
  });
  const failure = register.error ?? signIn.error;
  const pending = register.isPending || signIn.isPending;
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const { invite_code = "", email = "", password = "" } = readForm(event.currentTarget);
        if (pending) {
          return;
        }
        // an account already made is signed in to, not registered again
        if (register.isSuccess) {
          signIn.mutate({ email: email.trim(), password });
        } else {
          register.mutate({ invite_code: invite_code.trim(), email: email.trim(), password });
        }
      }}
    >
      {failure ? (
        <FormError>{register.isSuccess ? t("auth.register.signInFailed") : errors.message(failure)}</FormError>
      ) : null}
      <RegisterFields error={register.error} />
      <SubmitButton pending={pending} className="w-full">
        {t("auth.register.submit")}
      </SubmitButton>
    </form>
  );
}
