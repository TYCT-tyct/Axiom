import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { QrCode, useErrors, useSession, useSessionBound, useShowRecoveryCodes, useStartSession } from "@axiom/app";
import { Button, Cluster, Dialog, Field, FormError, Stack, SubmitButton } from "@axiom/ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { sessionsQuery, validatePasswordChange, validateTotpReplace } from "./security-data";

const passwordErrorKeys = {
  currentMissing: "security.password.currentMissing",
  tooShort: "security.password.tooShort",
  mismatch: "security.password.mismatch",
  same: "security.password.same",
} as const;

function useChangePassword(onSaved: () => void, onClose: () => void) {
  const { t } = useTranslation();
  const errors = useErrors();
  const session = useSession();
  const start = useStartSession();
  const showCodes = useShowRecoveryCodes();
  const bound = useSessionBound("identity");
  const client = useQueryClient();
  const change = useMutation({
    mutationFn: (body: { current_password: string; new_password: string; language: "zh" | "en" }) =>
      bound((signal) => unwrap(consoleApi.POST("/api/console/v1/password", { body, signal }))),
    onSuccess: async (answer) => {
      if (!answer) return;
      const { csrf_token, recovery_codes } = answer.value;
      if (recovery_codes?.length) {
        showCodes({ csrf_token: csrf_token ?? session.csrf_token, recovery_codes }, answer.claim.identity);
      }
      await start(csrf_token ?? "").catch(() => {});
      await client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
      onSaved();
      onClose();
    },
  });
  const serverError =
    change.error instanceof ApiError && change.error.code === "invalid_credentials"
      ? t("security.password.wrongCurrent")
      : change.isError
        ? errors.message(change.error)
        : null;
  return { change, serverError };
}

export function ChangePasswordDialog({
  english,
  onClose,
  onSaved,
}: {
  english: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);
  const { change, serverError } = useChangePassword(onSaved, onClose);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (change.isPending) return;
    const issue = validatePasswordChange(current, next, confirm);
    if (issue) return setValidationError(t(passwordErrorKeys[issue]));
    setValidationError(null);
    change.mutate({ current_password: current, new_password: next, language: english ? "en" : "zh" });
  }

  return (
    <Dialog title={t("security.password.dialogTitle")} onClose={onClose} dismissible={!change.isPending}>
      <form onSubmit={submit} noValidate>
        <Stack gap={16}>
          <p>{t("security.password.dialogBody")}</p>
          {(validationError ?? serverError) ? <FormError>{validationError ?? serverError}</FormError> : null}
          <Field
            label={t("security.password.current")}
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
            error={errors.field(change.error, "current_password")}
          />
          <Field
            label={t("security.password.new")}
            hint={t("security.password.newHint")}
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(event) => setNext(event.target.value)}
            error={errors.field(change.error, "new_password")}
          />
          <Field
            label={t("security.password.confirm")}
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
          />
          <Cluster justify="end" gap={8}>
            <Button onClick={onClose} disabled={change.isPending}>
              {t("security.cancel")}
            </Button>
            <SubmitButton pending={change.isPending}>{t("security.password.submit")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}

function useReplaceTotp(onReplaced: () => void, onClose: () => void) {
  const start = useStartSession();
  const showCodes = useShowRecoveryCodes();
  const bound = useSessionBound("identity");
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["TotpReplaceRequest"]) =>
      bound((signal) => unwrap(consoleApi.POST("/api/console/v1/auth/totp/replace", { body, signal }))),
    onSuccess: async (answer) => {
      if (!answer) return;
      const { csrf_token, recovery_codes } = answer.value;
      if (recovery_codes.length) showCodes({ csrf_token, recovery_codes }, answer.claim.identity);
      await start(csrf_token).catch(() => {});
      await client.invalidateQueries({ queryKey: sessionsQuery.queryKey });
      onReplaced();
      onClose();
    },
  });
}

export function ReplaceTotpDialog({
  setup,
  onClose,
  onReplaced,
}: {
  setup: Schemas["TotpSetup"];
  onClose: () => void;
  onReplaced: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [currentCode, setCurrentCode] = useState("");
  const [newCode, setNewCode] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);
  const replace = useReplaceTotp(onReplaced, onClose);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (replace.isPending) return;
    const issue = validateTotpReplace(currentCode, newCode);
    if (issue) {
      return setValidationError(
        t(issue === "currentMissing" ? "security.twoStep.currentCode" : "security.twoStep.codeInvalid"),
      );
    }
    setValidationError(null);
    replace.mutate({ secret: setup.secret, current_code: currentCode.trim(), code: newCode.trim() });
  }

  return (
    <Dialog title={t("security.twoStep.replaceDialogTitle")} onClose={onClose} dismissible={!replace.isPending}>
      <form onSubmit={submit} noValidate>
        <Stack gap={16}>
          <p>{t("security.twoStep.replaceBody")}</p>
          <p>{t("security.twoStep.setupScanHint")}</p>
          <Cluster gap={16}>
            <QrCode value={setup.uri} label={t("security.twoStep.setupQrAlt")} />
            <Stack gap={4}>
              <span>{t("security.twoStep.setupSecret")}</span>
              <code>{setup.secret}</code>
            </Stack>
          </Cluster>
          {(validationError ?? (replace.isError ? errors.message(replace.error) : null)) ? (
            <FormError>{validationError ?? errors.message(replace.error)}</FormError>
          ) : null}
          <Field
            label={t("security.twoStep.currentCode")}
            autoComplete="one-time-code"
            value={currentCode}
            onChange={(event) => setCurrentCode(event.target.value)}
            error={errors.field(replace.error, "current_code")}
          />
          <Field
            label={t("security.twoStep.newCode")}
            inputMode="numeric"
            autoComplete="one-time-code"
            value={newCode}
            onChange={(event) => setNewCode(event.target.value)}
            error={errors.field(replace.error, "code")}
          />
          <Cluster justify="end" gap={8}>
            <Button onClick={onClose} disabled={replace.isPending}>
              {t("security.cancel")}
            </Button>
            <SubmitButton pending={replace.isPending}>{t("security.twoStep.replaceSubmit")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}

export function AddPasskeyDialog({
  initialPassword,
  busy,
  errorText,
  onSubmit,
  onClose,
}: {
  initialPassword: string;
  busy: boolean;
  errorText: string | null;
  onSubmit: (name: string, password: string) => Promise<boolean>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [password, setPassword] = useState(initialPassword);
  const [localError, setLocalError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    if (password === "") return setLocalError(t("security.passkeys.passwordRequired"));
    setLocalError(null);
    if (await onSubmit(name.trim(), password)) onClose();
  }

  return (
    <Dialog title={t("security.passkeys.addTitle")} onClose={onClose} dismissible={!busy}>
      <form onSubmit={(event) => void submit(event)} noValidate>
        <Stack gap={16}>
          <p>{t("security.passkeys.addBody")}</p>
          {(localError ?? errorText) ? <FormError>{localError ?? errorText}</FormError> : null}
          <Field
            label={t("security.passkeys.name")}
            placeholder={t("security.passkeys.namePlaceholder")}
            maxLength={100}
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
          <Field
            label={t("security.passkeys.password")}
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
          <Cluster justify="end" gap={8}>
            <Button onClick={onClose} disabled={busy}>
              {t("security.cancel")}
            </Button>
            <SubmitButton pending={busy}>{t("security.passkeys.add")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}
