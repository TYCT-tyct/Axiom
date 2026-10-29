import { consoleApi, unwrap } from "@axiom/api";
import { AuthFrame, readForm, useErrors } from "@axiom/app";
import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { siteQuery } from "../queries";

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

export default function VerifyEmail() {
  const { t } = useTranslation();
  const errors = useErrors();
  const site = useQuery(siteQuery);
  const [token] = useState(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "");
  const verify = useMutation({
    mutationFn: (body: { token: string; password: string }) =>
      unwrap(consoleApi.POST("/api/console/v1/auth/verify-email", { body })),
  });
  useEffect(() => {
    if (verify.isSuccess) {
      history.replaceState(null, "", window.location.pathname);
    }
  }, [verify.isSuccess]);

  return (
    <AuthFrame brand={site.data} title={t("auth.verify.title")} description={t("auth.verify.description")}>
      {token === "" ? (
        <FormError>{t("auth.verify.missing")}</FormError>
      ) : verify.isSuccess ? (
        <Verified />
      ) : (
        <form
          noValidate
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            const { password = "" } = readForm(event.currentTarget);
            if (!verify.isPending) {
              verify.mutate({ token, password });
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
          <SubmitButton pending={verify.isPending} className="w-full">
            {t("auth.verify.submit")}
          </SubmitButton>
          {verify.error ? (
            <Link to="/register" className="inline-block text-accent underline underline-offset-4">
              {t("auth.verify.again")}
            </Link>
          ) : null}
        </form>
      )}
      {token === "" ? (
        <Link to="/register" className="mt-4 inline-block text-accent underline underline-offset-4">
          {t("auth.verify.again")}
        </Link>
      ) : null}
    </AuthFrame>
  );
}
