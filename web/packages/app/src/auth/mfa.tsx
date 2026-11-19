import { FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useErrors } from "../errors";
import { readForm } from "../forms";
import {
  Gate,
  pendingOver,
  sessionBinding,
  UnreadSession,
  useAdoptGrant,
  useEndPending,
  useSession,
  useVerifyMfa,
} from "../session";
import { AuthFrame, type Brand, OtherAccount } from "./frame";

// MfaPage asks a signed-in password for its second step. A pending sign-in that is over is signed out, and the sign-in
// page says so.
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
  const endPending = useEndPending();
  const pending = sessionBinding(useSession());
  if (verify.error instanceof UnreadSession) return <Unread brand={brand} unread={verify.error} />;
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
            verify.mutate(
              { code },
              { onError: (error) => pendingOver(error) && void endPending(pending).catch(() => {}) },
            );
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

// A step that passed whose new session could not be read: the step is not offered again, only the read (UnreadSession).
function Unread({ brand, unread }: { brand?: Brand; unread: UnreadSession }) {
  const { t } = useTranslation("common");
  const errors = useErrors();
  const adopt = useAdoptGrant();
  const reread = useMutation({ mutationFn: adopt });
  const failed = reread.error instanceof UnreadSession ? reread.error.reason : (reread.error ?? unread.reason);
  return (
    <AuthFrame title={t("auth.mfa.title")} description={t("auth.mfa.unread")} brand={brand}>
      <form
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (!reread.isPending) reread.mutate(unread.csrf);
        }}
      >
        <FormError>{errors.message(failed)}</FormError>
        <SubmitButton pending={reread.isPending} wide>
          {t("state.retry")}
        </SubmitButton>
      </form>
    </AuthFrame>
  );
}
