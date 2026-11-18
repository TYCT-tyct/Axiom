import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { readForm } from "../forms";
import { Gate, useSignIn } from "../session";
import { AuthFrame, type Brand } from "./frame";

// SignInPage is the page of an app's guests; what it says about the app's purpose is the app's (description).
export function SignInPage({ description, brand }: { description: string; brand?: Brand }) {
  return (
    <Gate stage="guest">
      <SignInForm description={description} brand={brand} />
    </Gate>
  );
}

function SignInForm({ description, brand }: { description: string; brand?: Brand }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const [missing, setMissing] = useState(false);
  const signIn = useSignIn();
  return (
    <AuthFrame title={t("auth.signIn.title")} description={description} brand={brand}>
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
        <SubmitButton pending={signIn.isPending} wide>
          {t("auth.signIn.submit")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
