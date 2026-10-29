import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { AuthFrame, Gate, readForm, useErrors, useSignIn } from "@axiom/app";
import { DataState, FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { solveProof } from "./proof";

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
        {(data) => {
          if (data.registration === "open" && data.email_verification) {
            return <OpenRegisterForm site={data} />;
          }
          if (data.registration === "invite") {
            return <InviteRegisterForm />;
          }
          return <p className="text-t3">{t("auth.register.closed")}</p>;
        }}
      </DataState>
    </AuthFrame>
  );
}

function RegisterFields({
  error,
  invite: withInvite,
  password = true,
}: {
  error: unknown;
  invite: boolean;
  password?: boolean;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const invite = useSearch({ strict: false }).invite;
  return (
    <>
      {withInvite ? (
        <TextField
          label={t("auth.register.invite")}
          name="invite_code"
          defaultValue={invite}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          error={errors.field(error, "invite_code")}
        />
      ) : null}
      <TextField
        label={t("auth.email", { ns: "common" })}
        name="email"
        autoComplete="username"
        inputMode="email"
        error={errors.field(error, "email")}
      />
      {password ? (
        <TextField
          label={t("auth.password", { ns: "common" })}
          hint={t("auth.register.passwordHint")}
          name="password"
          type="password"
          autoComplete="new-password"
          error={errors.field(error, "password")}
        />
      ) : null}
    </>
  );
}

type RegisterResult = Schemas["Registered"] | Schemas["RegistrationPending"];

function isRegistered(result: RegisterResult | undefined): result is Schemas["Registered"] {
  return result !== undefined && "user_id" in result;
}

function CheckEmail() {
  const { t } = useTranslation();
  return (
    <div className="space-y-4">
      <p className="text-t2">{t("auth.register.checkEmail")}</p>
      <p className="text-t3 text-sm">{t("auth.register.checkEmailHint")}</p>
      <Link to="/sign-in" className="text-accent underline underline-offset-4">
        {t("auth.register.signIn")}
      </Link>
    </div>
  );
}

// Registering does not sign the user in: the page signs in next, with the same credentials. When that step fails the
// account exists, so the form says to sign in instead of offering the registration again. A mode change can turn an
// invite form's submission into a pending registration instead: that answer means no account exists yet.
function InviteRegisterForm() {
  const { t } = useTranslation();
  const errors = useErrors();
  const signIn = useSignIn();
  const [sent, setSent] = useState(false);
  const register = useMutation({
    mutationFn: (body: Schemas["RegisterRequest"]) =>
      unwrap(consoleApi.POST("/api/console/v1/auth/register", { body })),
    onSuccess: (result, { email, password }) => {
      if (isRegistered(result)) {
        signIn.mutate({ email, password: password ?? "" });
      } else {
        setSent(true);
      }
    },
  });
  const made = isRegistered(register.data);
  const failure = register.error ?? signIn.error;
  const pending = register.isPending || signIn.isPending;
  if (sent) {
    return <CheckEmail />;
  }
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
        if (made) {
          signIn.mutate({ email: email.trim(), password });
        } else {
          register.mutate({ invite_code: invite_code.trim(), email: email.trim(), password });
        }
      }}
    >
      {failure ? <FormError>{made ? t("auth.register.signInFailed") : errors.message(failure)}</FormError> : null}
      <RegisterFields error={register.error} invite />
      <SubmitButton pending={pending} className="w-full">
        {t("auth.register.submit")}
      </SubmitButton>
    </form>
  );
}

// OpenRegisterForm solves the site's proof of work in the page and waits for the mailed link; it does not sign the user
// in, because there is no user until the link is clicked.
function OpenRegisterForm({ site }: { site: Schemas["Site"] }) {
  const { t, i18n } = useTranslation();
  const errors = useErrors();
  const [working, setWorking] = useState(false);
  const register = useMutation({
    mutationFn: async ({ email }: { email: string }) => {
      const normalized = email.trim().toLowerCase();
      let human: string | undefined;
      if (site.human_check) {
        if (!site.id) {
          throw new Error("the site has no id for the proof of work");
        }
        setWorking(true);
        // Let the pending state paint before the synchronous solver takes the main thread.
        await new Promise((resolve) => setTimeout(resolve, 0));
        human = await solveProof(site.id, normalized, site.human_check.bits);
        setWorking(false);
      }
      return unwrap(
        consoleApi.POST("/api/console/v1/auth/register", {
          body: {
            email: email.trim(),
            human_check: human,
            language: i18n.language.startsWith("zh") ? "zh" : "en",
          },
        }),
      );
    },
    onSettled: () => setWorking(false),
  });
  if (register.isSuccess) {
    return <CheckEmail />;
  }
  const pending = register.isPending || working;
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const { email = "" } = readForm(event.currentTarget);
        if (!pending) {
          register.mutate({ email });
        }
      }}
    >
      {register.error ? <FormError>{errors.message(register.error)}</FormError> : null}
      {working ? <p className="text-t3 text-sm">{t("auth.register.working")}</p> : null}
      <RegisterFields error={register.error} invite={false} password={false} />
      <SubmitButton pending={pending} className="w-full">
        {t("auth.register.submitOpen")}
      </SubmitButton>
    </form>
  );
}
