import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { Link, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { readForm } from "../forms";
import { Gate, useSignIn } from "../session";
import { AuthFrame } from "./frame";

export default function SignIn() {
  return (
    <Gate stage="guest">
      <SignInPage />
    </Gate>
  );
}

function SignInPage() {
  const { t } = useTranslation();
  const errors = useErrors();
  const next = useSearch({ strict: false }).next;
  const [missing, setMissing] = useState(false);
  const signIn = useSignIn();
  return (
    <AuthFrame
      title={t("auth.signIn.title")}
      description={t("auth.signIn.description")}
      footer={
        <>
          {t("auth.signIn.noAccount")}{" "}
          <Link
            to="/register"
            search={{ next, invite: undefined }}
            className="text-accent underline underline-offset-4"
          >
            {t("auth.signIn.register")}
          </Link>
        </>
      }
    >
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          const { email = "", password = "" } = readForm(event.currentTarget);
          setMissing(!email.trim() || !password);
          if (email.trim() && password && !signIn.isPending) {
            signIn.mutate({ email: email.trim(), password });
          }
        }}
      >
        {signIn.isError || missing ? (
          <FormError>{missing ? t("auth.missing") : errors.message(signIn.error)}</FormError>
        ) : null}
        <TextField label={t("auth.email")} name="email" autoComplete="username" inputMode="email" />
        <TextField label={t("auth.password")} name="password" type="password" autoComplete="current-password" />
        <SubmitButton pending={signIn.isPending} className="w-full">
          {t("auth.signIn.submit")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
