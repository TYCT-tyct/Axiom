import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { Button, FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { readForm } from "../forms";
import { Gate, startSession, useSignOut } from "../session";
import { AuthFrame } from "./frame";

export default function Mfa() {
  return (
    <Gate stage="pending">
      <MfaPage />
    </Gate>
  );
}

function MfaPage() {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const signOut = useSignOut();
  const verify = useMutation({
    mutationFn: (body: Schemas["MfaRequest"]) => unwrap(consoleApi.POST("/api/console/v1/auth/mfa", { body })),
    onSuccess: (grant) => startSession(queryClient, grant.csrf_token),
  });
  return (
    <AuthFrame
      title={t("auth.mfa.title")}
      description={t("auth.mfa.description")}
      footer={
        <Button variant="ghost" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
          {t("auth.mfa.otherAccount")}
        </Button>
      }
    >
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          const code = (readForm(event.currentTarget).code ?? "").trim();
          if (code && !verify.isPending) {
            verify.mutate({ code });
          }
        }}
      >
        {verify.isError ? <FormError>{errors.message(verify.error)}</FormError> : null}
        <TextField
          label={t("auth.mfa.code")}
          hint={t("auth.mfa.hint")}
          name="code"
          autoComplete="one-time-code"
          autoCapitalize="none"
          spellCheck={false}
          className="font-mono tracking-wider"
        />
        <SubmitButton pending={verify.isPending} className="w-full">
          {t("auth.mfa.submit")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
