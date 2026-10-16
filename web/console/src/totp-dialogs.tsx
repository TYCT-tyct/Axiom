import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { Button, CopyButton, Dialog, FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useErrors } from "./errors";
import { readForm } from "./forms";
import { QrCode } from "./qr";
import { adoptToken } from "./session";

// Recovery codes exist only in the answer that makes them. The dialog that shows them does not close on Escape or a
// click outside, and the dialog that asks for them does not close while the request is out, so they cannot be lost by
// accident.
function RecoveryCodes({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Dialog title={t("security.codes.title")} onClose={onClose} dismissible={false}>
      <div className="space-y-4">
        <p className="text-t2">{t("security.codes.body")}</p>
        <ul className="grid grid-cols-1 gap-x-4 gap-y-1 rounded-btn border border-b2 bg-s2 p-3 font-mono text-ui text-t1 select-all min-[420px]:grid-cols-2">
          {codes.map((code) => (
            <li key={code}>{code}</li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-3">
            <CopyButton text={codes.join("\n")} />
          </div>
          <Button variant="primary" size="md" onClick={onClose}>
            {t("security.codes.done")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

// Changing a factor ends the user's other sessions and issues this one a new cookie and token: the page adopts the
// token and shows the codes that came with it. Neither the dialog nor the codes wait for the session to be read again.
function useFactorChange<Body>(request: (body: Body) => Promise<Schemas["FactorChange"]>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: request,
    onSuccess: (change) => adoptToken(queryClient, change.csrf_token),
  });
}

// Enabling takes three steps in one dialog: scan the secret, prove it with the password and a code, keep the codes.
export function TotpSetupDialog({ setup, onClose }: { setup: Schemas["TotpSetup"]; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const confirm = useFactorChange((body: Schemas["TotpConfirmRequest"]) =>
    unwrap(consoleApi.POST("/api/console/v1/auth/totp/confirm", { body })),
  );
  if (confirm.data) {
    return <RecoveryCodes codes={confirm.data.recovery_codes} onClose={onClose} />;
  }
  return (
    <Dialog title={t("security.setup.title")} onClose={onClose} dismissible={!confirm.isPending}>
      <div className="space-y-4">
        <p className="text-t2">{t("security.setup.scan")}</p>
        <div className="flex flex-col items-start gap-4 min-[420px]:flex-row">
          <QrCode value={setup.uri} label={t("security.setup.qr")} />
          <div className="min-w-0 space-y-1.5">
            <p className="text-label text-t3">{t("security.setup.manual")}</p>
            <code className="block font-mono text-ui break-all text-t1 select-all">{setup.secret}</code>
          </div>
        </div>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            const { password = "", code = "" } = readForm(event.currentTarget);
            if (!confirm.isPending) {
              confirm.mutate({ secret: setup.secret, password, code: code.trim() });
            }
          }}
        >
          {confirm.isError ? <FormError>{errors.message(confirm.error)}</FormError> : null}
          <TextField
            label={t("auth.password")}
            name="password"
            type="password"
            autoComplete="current-password"
            error={errors.field(confirm.error, "password")}
          />
          <TextField
            label={t("security.setup.code")}
            name="code"
            inputMode="numeric"
            autoComplete="one-time-code"
            error={errors.field(confirm.error, "code")}
          />
          <div className="flex justify-end gap-2">
            <Button size="md" onClick={onClose} disabled={confirm.isPending}>
              {t("cancel")}
            </Button>
            <SubmitButton pending={confirm.isPending}>{t("security.setup.enable")}</SubmitButton>
          </div>
        </form>
      </div>
    </Dialog>
  );
}

export function RecoveryDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const regenerate = useFactorChange((body: Schemas["RecoveryCodesRequest"]) =>
    unwrap(consoleApi.POST("/api/console/v1/auth/recovery-codes", { body })),
  );
  if (regenerate.data) {
    return <RecoveryCodes codes={regenerate.data.recovery_codes} onClose={onClose} />;
  }
  return (
    <Dialog title={t("security.regenerate.title")} onClose={onClose} dismissible={!regenerate.isPending}>
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (!regenerate.isPending) {
            regenerate.mutate({ code: (readForm(event.currentTarget).code ?? "").trim() });
          }
        }}
      >
        <p className="text-t2">{t("security.regenerate.body")}</p>
        {regenerate.isError ? <FormError>{errors.message(regenerate.error)}</FormError> : null}
        <TextField
          label={t("security.setup.code")}
          name="code"
          inputMode="numeric"
          autoComplete="one-time-code"
          error={errors.field(regenerate.error, "code")}
        />
        <div className="flex justify-end gap-2">
          <Button size="md" onClick={onClose} disabled={regenerate.isPending}>
            {t("cancel")}
          </Button>
          <SubmitButton pending={regenerate.isPending}>{t("security.regenerate.confirm")}</SubmitButton>
        </div>
      </form>
    </Dialog>
  );
}
