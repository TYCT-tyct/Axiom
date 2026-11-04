import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { AuthFrame, readForm, useErrors } from "@axiom/app";
import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";
import { canSubmitWithLegal, LegalConsent, type LegalConsentState, useLegalConsent } from "./legal";

// The verification link carries its single-use token in the fragment, which the browser does not send to the server.
// The password is chosen here, after the address is proved, so a caller cannot set it before the mailbox owner clicks.
function Verified() {
  const { t } = useTranslation();
  return (
    <div className="space-y-4">
      <p className="text-t2">{t("auth.verify.done")}</p>
      <Link to="/sign-in" className="text-accent underline underline-offset-4">
        {t("auth.verify.signIn")}
      </Link>
    </div>
  );
}

function useVerifyEmail(legal: LegalConsentState) {
  return useMutation({
    mutationFn: (body: Schemas["VerifyEmailRequest"]) =>
      unwrap(consoleApi.POST("/api/console/v1/auth/verify-email", { body })),
    onError: (error) => {
      if (error instanceof ApiError && error.code === "consent_required") {
        legal.reload();
      }
    },
  });
}

function VerifyForm({
  token,
  legal,
  verify,
}: {
  token: string;
  legal: LegalConsentState;
  verify: ReturnType<typeof useVerifyEmail>;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const { password = "" } = readForm(event.currentTarget);
        if (!verify.isPending && canSubmitWithLegal(legal)) {
          verify.mutate({ token, password, consent: legal.versions });
        }
      }}
    >
      {verify.error ? <FormError>{errors.message(verify.error)}</FormError> : null}
      <TextField
        label={t("auth.password", { ns: "common" })}
        hint={t("auth.register.passwordHint")}
        name="password"
        type="password"
        autoComplete="new-password"
        error={errors.field(verify.error, "password")}
      />
      <LegalConsent consent={legal} />
      <SubmitButton pending={verify.isPending} disabled={!canSubmitWithLegal(legal)} className="w-full">
        {t("auth.verify.submit")}
      </SubmitButton>
      {verify.error ? (
        <Link to="/register" className="inline-block text-accent underline underline-offset-4">
          {t("auth.verify.again")}
        </Link>
      ) : null}
    </form>
  );
}

export default function VerifyEmail() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  const legal = useLegalConsent();
  const [token] = useState(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "");
  const verify = useVerifyEmail(legal);
  useEffect(() => {
    if (verify.isSuccess) {
      history.replaceState(null, "", window.location.pathname);
    }
  }, [verify.isSuccess]);

  return (
    <AuthFrame brand={site.data} wide title={t("auth.verify.title")} description={t("auth.verify.description")}>
      {token === "" ? (
        <FormError>{t("auth.verify.missing")}</FormError>
      ) : verify.isSuccess ? (
        <Verified />
      ) : (
        <VerifyForm token={token} legal={legal} verify={verify} />
      )}
      {token === "" ? (
        <Link to="/register" className="mt-4 inline-block text-accent underline underline-offset-4">
          {t("auth.verify.again")}
        </Link>
      ) : null}
    </AuthFrame>
  );
}
