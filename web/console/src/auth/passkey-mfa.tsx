import { consoleApi, unwrap } from "@axiom/api";
import { useErrors, useStartSession } from "@axiom/app";
import { Button, FormError, Icon } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { signInWithPasskey } from "../webauthn";

// PasskeyMfa completes a pending password sign-in with a passkey instead of a code.
export function PasskeyMfa() {
  const { t } = useTranslation();
  const errors = useErrors();
  const start = useStartSession();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  async function run() {
    setPending(true);
    setFailure(null);
    try {
      const begin = await unwrap(consoleApi.POST("/api/console/v1/auth/passkeys/mfa/begin", { body: {} }));
      const credential = await signInWithPasskey(begin.public_key);
      const grant = await unwrap(
        consoleApi.POST("/api/console/v1/auth/passkeys/mfa/finish", {
          params: { query: { flow_id: begin.flow_id } },
          body: credential,
        }),
      );
      await start(grant.csrf_token);
    } catch (error) {
      setFailure(error);
    } finally {
      setPending(false);
    }
  }
  return (
    <div className="mt-6 space-y-3">
      {failure ? <FormError>{errors.message(failure)}</FormError> : null}
      <p className="text-center text-label text-t3">{t("auth.mfa.orPasskey")}</p>
      <Button
        type="button"
        size="md"
        variant="secondary"
        className="w-full"
        disabled={pending}
        onClick={() => void run()}
      >
        <Icon name="shield" />
        {t("auth.mfa.usePasskey")}
      </Button>
    </div>
  );
}
