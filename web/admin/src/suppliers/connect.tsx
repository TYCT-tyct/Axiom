import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { readForm, useErrors } from "@axiom/app";
import { Button, Dialog, FormError, SubmitButton, TextField } from "@axiom/ui";
import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, isProblem, type Problem } from "../change";
import { Code } from "../parts";
import { LoginFields, readLogin } from "./login-fields";

type Detection = AdminSchemas["Detection"];

// Connecting a supplier takes two steps. First the address: the server probes it, which stores nothing, and says what
// system it runs and what login that kind needs. Then the account and the login, which is the change that connects.
export function ConnectDialog({ onClose }: { onClose: () => void }) {
  const [detection, setDetection] = useState<Detection | null>(null);
  return detection?.kind ? (
    <CreateDialog detection={detection} onClose={onClose} />
  ) : (
    <DetectDialog detection={detection} onDetected={setDetection} onClose={onClose} />
  );
}

function DetectDialog({
  detection,
  onDetected,
  onClose,
}: {
  detection: Detection | null;
  onDetected: (detection: Detection) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [missing, setMissing] = useState(false);
  const detect = useMutation({
    mutationFn: (origin: string) => unwrap(adminApi.POST("/api/admin/v1/suppliers/detections", { body: { origin } })),
    onSuccess: onDetected,
  });
  return (
    <Dialog title={t("suppliers.connect.title")} onClose={onClose}>
      <form
        noValidate
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          const origin = (readForm(event.currentTarget).origin ?? "").trim().replace(/\/+$/, "");
          setMissing(!/^https:\/\//i.test(origin));
          if (/^https:\/\//i.test(origin) && !detect.isPending) {
            detect.mutate(origin);
          }
        }}
      >
        <p className="text-t2">{t("suppliers.connect.detectIntro")}</p>
        {detect.isError || missing ? (
          <FormError>{missing ? t("suppliers.connect.originInvalid") : errors.message(detect.error)}</FormError>
        ) : null}
        {detection && !detection.kind ? <FormError>{t("suppliers.connect.unrecognised")}</FormError> : null}
        <TextField
          label={t("suppliers.connect.origin")}
          hint={t("suppliers.connect.originHint")}
          name="origin"
          type="url"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          maxLength={255}
          error={errors.field(detect.error, "origin")}
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button size="md" onClick={onClose}>
            {t("cancel", { ns: "common" })}
          </Button>
          <SubmitButton pending={detect.isPending}>{t("suppliers.connect.detect")}</SubmitButton>
        </div>
      </form>
    </Dialog>
  );
}

type Create = Omit<AdminSchemas["SupplierCreate"], "reason">;

function CreateDialog({ detection, onClose }: { detection: Detection; onClose: () => void }) {
  const { t } = useTranslation();
  const kind = detection.kind ?? "";
  const read = (values: Record<string, string>): Create | Problem => {
    const name = (values.name ?? "").trim();
    const account = (values.upstream_account ?? "").trim();
    const currency = (values.settlement_currency ?? "").trim().toUpperCase();
    const login = readLogin(values, detection.login_fields, t("suppliers.connect.loginMissing"));
    if (!name || !account || !/^[A-Z]{3}$/.test(currency)) {
      return { problem: t("suppliers.connect.detailsInvalid") };
    }
    if (isProblem(login)) {
      return login;
    }
    return { name, kind, origin: detection.origin, upstream_account: account, settlement_currency: currency, login };
  };
  return (
    <ChangeDialog
      title={t("suppliers.connect.title")}
      intro={
        <>
          <p>
            {t("suppliers.connect.detected", { kind })}
            {detection.generation ? ` · ${detection.generation}` : ""}
            {detection.detected_version ? ` · ${detection.detected_version}` : ""}
          </p>
          <p className="text-label text-t3">
            <Code>{detection.origin}</Code>
          </p>
        </>
      }
      action={t("suppliers.connect.submit")}
      fields={(error) => <CreateFields error={error} fields={detection.login_fields} />}
      read={read}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...input, reason },
          }),
        )
      }
      done={(supplier) => (
        <>
          <p className="text-t1">{t("suppliers.connect.connecting", { name: supplier.name })}</p>
          <p>{t("suppliers.connect.next")}</p>
          <Link
            to="/suppliers/$supplierId"
            params={{ supplierId: supplier.id }}
            className="text-accent underline underline-offset-4"
          >
            {t("suppliers.connect.view")}
          </Link>
        </>
      )}
      onClose={onClose}
    />
  );
}

function CreateFields({ error, fields }: { error: unknown; fields: Detection["login_fields"] }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <TextField
        label={t("suppliers.connect.name")}
        name="name"
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "name")}
      />
      <TextField
        label={t("suppliers.connect.account")}
        hint={t("suppliers.connect.accountHint")}
        name="upstream_account"
        maxLength={254}
        autoComplete="off"
        spellCheck={false}
        error={errors.field(error, "upstream_account")}
      />
      <TextField
        label={t("suppliers.connect.currency")}
        hint={t("suppliers.connect.currencyHint")}
        name="settlement_currency"
        maxLength={3}
        autoComplete="off"
        spellCheck={false}
        className="uppercase"
        error={errors.field(error, "settlement_currency")}
      />
      <LoginFields fields={fields} />
    </>
  );
}
