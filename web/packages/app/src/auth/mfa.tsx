import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { readForm } from "../forms";
import { Gate, useVerifyMfa } from "../session";
import { AuthFrame, type Brand, OtherAccount } from "./frame";

// MfaPage asks a signed-in password for its second step.
export function MfaPage({ brand }: { brand?: Brand }) {
  return (
    <Gate stage="pending">
      <MfaForm brand={brand} />
    </Gate>
  );
}

function MfaForm({ brand }: { brand?: Brand }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const verify = useVerifyMfa();
  return (
    <AuthFrame
      title={t("auth.mfa.title")}
      description={t("auth.mfa.description")}
      brand={brand}
      footer={<OtherAccount />}
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
        />
        <SubmitButton pending={verify.isPending} wide>
          {t("auth.mfa.submit")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
