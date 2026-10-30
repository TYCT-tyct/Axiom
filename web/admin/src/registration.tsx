import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, CheckField, DataState, Icon, SelectField, TextAreaField, TextField, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts } from "./parts";
import { siteRegistrationQuery } from "./queries";

type Policy = AdminSchemas["RegistrationPolicy"];
type View = AdminSchemas["RegistrationPolicyView"];

// RegistrationUnavailable stands in for the card where an older node has no registration operation: the rest of the
// settings still work.
function RegistrationUnavailable() {
  const { t } = useTranslation();
  return (
    <Facts
      title={t("settings.registration.title")}
      rows={[[t("settings.registration.unavailableLabel"), t("settings.registration.unavailable")]]}
    />
  );
}

export function SiteRegistration({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const query = useQuery(siteRegistrationQuery(siteId));
  // Only a 404 says this node has no registration operation. A timeout, a 503 or a permission error is a current
  // failure and belongs in DataState, with its reason and retry.
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <RegistrationUnavailable />;
  }
  return <DataState query={query}>{(view) => <RegistrationCard siteId={siteId} view={view} />}</DataState>;
}

function RegistrationCard({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: View }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [open, setOpen] = useState(false);
  const { policy } = view;
  const rows: [string, ReactNode][] = [
    [t("settings.registration.mode"), <span key="mode">{t(`settings.registration.modes.${policy.mode}`)}</span>],
    [
      t("settings.registration.humanCheck"),
      <span key="human">
        {policy.human_check
          ? t("settings.registration.on", { bits: policy.human_check_bits })
          : t("settings.registration.off")}
      </span>,
    ],
    [t("settings.registration.domainBurst"), <span key="burst">{format.number(policy.domain_burst)}</span>],
    [t("settings.registration.domainPerHour"), <span key="perHour">{format.number(policy.domain_per_hour)}</span>],
    [
      t("settings.registration.domainAllow"),
      <span key="allow" className="break-all">
        {policy.domain_allow.length ? policy.domain_allow.join(", ") : t("settings.registration.any")}
      </span>,
    ],
    [
      t("settings.registration.domainDeny"),
      <span key="deny" className="break-all">
        {policy.domain_deny.length ? policy.domain_deny.join(", ") : t("settings.registration.none")}
      </span>,
    ],
  ];
  return (
    <>
      <Facts
        title={t("settings.registration.title")}
        rows={rows}
        action={
          <Button size="md" onClick={() => setOpen(true)}>
            <Icon name="pencil" />
            {t("settings.registration.edit")}
          </Button>
        }
      />
      {open ? <RegistrationDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function splitDomains(text: string): string[] {
  return text.split(/[\s,]+/).filter(Boolean);
}

function readPolicy(values: Record<string, string>, words: string[]): Policy | Problem {
  const mode = values.mode;
  if (mode !== "invite" && mode !== "closed" && mode !== "open") {
    return { problem: words[0] ?? "" };
  }
  const read = (name: string, min: number, max: number): number | undefined => {
    const typed = (values[name] ?? "").trim();
    const value = Number(typed);
    return /^\d+$/.test(typed) && value >= min && value <= max ? value : undefined;
  };
  const bits = read("human_check_bits", 12, 24);
  const burst = read("domain_burst", 1, 10_000);
  const perHour = read("domain_per_hour", 1, 100_000);
  if (bits === undefined || burst === undefined || perHour === undefined) {
    return { problem: words[1] ?? "" };
  }
  return {
    mode,
    human_check: values.human_check === "true",
    human_check_bits: bits,
    domain_burst: burst,
    domain_per_hour: perHour,
    domain_allow: splitDomains(values.domain_allow ?? ""),
    domain_deny: splitDomains(values.domain_deny ?? ""),
  };
}

function NumericPolicyField({
  label,
  hint,
  name,
  value,
  error,
}: {
  label: string;
  hint: string;
  name: string;
  value: number;
  error?: string;
}) {
  return (
    <TextField
      label={label}
      hint={hint}
      name={name}
      inputMode="numeric"
      autoComplete="off"
      defaultValue={value}
      error={error}
    />
  );
}

function DomainPolicyField({
  label,
  name,
  domains,
  error,
}: {
  label: string;
  name: string;
  domains: string[];
  error?: string;
}) {
  const { t } = useTranslation();
  return (
    <TextAreaField
      label={label}
      hint={t("settings.registration.domainHint")}
      name={name}
      defaultValue={domains.join("\n")}
      error={error}
    />
  );
}

function RegistrationFields({ policy, error }: { policy: Policy; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <SelectField
        label={t("settings.registration.mode")}
        name="mode"
        defaultValue={policy.mode}
        error={errors.field(error, "mode")}
      >
        <option value="invite">{t("settings.registration.modes.invite")}</option>
        <option value="closed">{t("settings.registration.modes.closed")}</option>
        <option value="open">{t("settings.registration.modes.open")}</option>
      </SelectField>
      <CheckField
        label={t("settings.registration.humanCheck")}
        hint={t("settings.registration.humanCheckHint")}
        name="human_check"
        value="true"
        defaultChecked={policy.human_check}
      />
      <NumericPolicyField
        label={t("settings.registration.bits")}
        hint={t("settings.registration.bitsHint")}
        name="human_check_bits"
        value={policy.human_check_bits}
        error={errors.field(error, "human_check_bits")}
      />
      <NumericPolicyField
        label={t("settings.registration.domainBurst")}
        hint={t("settings.registration.burstHint")}
        name="domain_burst"
        value={policy.domain_burst}
        error={errors.field(error, "domain_burst")}
      />
      <NumericPolicyField
        label={t("settings.registration.domainPerHour")}
        hint={t("settings.registration.perHourHint")}
        name="domain_per_hour"
        value={policy.domain_per_hour}
        error={errors.field(error, "domain_per_hour")}
      />
      <DomainPolicyField
        label={t("settings.registration.domainAllow")}
        name="domain_allow"
        domains={policy.domain_allow}
        error={errors.field(error, "domain_allow")}
      />
      <DomainPolicyField
        label={t("settings.registration.domainDeny")}
        name="domain_deny"
        domains={policy.domain_deny}
        error={errors.field(error, "domain_deny")}
      />
    </>
  );
}

function RegistrationDialog({
  siteId,
  view,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  view: View;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`site-registration:${siteId}`}
      title={t("settings.registration.edit")}
      intro={<p>{t("settings.registration.intro")}</p>}
      action={t("settings.registration.save")}
      fields={(error) => <RegistrationFields policy={view.policy} error={error} />}
      read={(values) =>
        readPolicy(values, [t("settings.registration.invalidMode"), t("settings.registration.invalidNumber")])
      }
      send={(policy, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/registration", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { policy, reason },
          }),
        )
      }
      done={() => <p>{t("settings.registration.saved")}</p>}
      onClose={onClose}
    />
  );
}
