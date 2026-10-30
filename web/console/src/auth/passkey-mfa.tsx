import { useErrors } from "@axiom/app";
import { Button, FormError, Icon } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { usePasskeySignIn } from "./passkey-sign-in";

// PasskeyMfa completes a pending password sign-in with a passkey instead of a code.
export function PasskeyMfa() {
  const { t } = useTranslation();
  const errors = useErrors();
  const passkey = usePasskeySignIn("mfa");
  return (
    <div className="mt-6 space-y-3">
      {passkey.failure ? <FormError>{errors.message(passkey.failure)}</FormError> : null}
      <p className="text-center text-label text-t3">{t("auth.mfa.orPasskey")}</p>
      <Button
        type="button"
        size="md"
        variant="secondary"
        className="w-full"
        disabled={passkey.pending}
        onClick={() => void passkey.start()}
      >
        <Icon name="shield" />
        {t("auth.mfa.usePasskey")}
      </Button>
    </div>
  );
}
