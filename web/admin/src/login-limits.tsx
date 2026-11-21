import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, DataState, Field, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { readBoundedInts } from "./key-limits";
import { Facts, State } from "./parts";
import { siteLoginLimitsQuery } from "./queries";

type View = AdminSchemas["LoginLimitsView"];

// The sign-in attempt thresholds of a site, each with the contract bounds from api/admin.yaml. Leaving a field empty
// omits it so the code default applies.
const fields = [
  { name: "ip_attempt_burst", min: 1, max: 10_000 },
  { name: "ip_attempts_per_minute", min: 1, max: 10_000 },
  { name: "email_sign_in_failures", min: 1, max: 1_000 },
  { name: "user_code_failures", min: 1, max: 1_000 },
  { name: "challenge_code_attempts", min: 1, max: 100 },
] as const;

export function SiteLoginLimits({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const { t } = useTranslation();
  const query = useQuery(siteLoginLimitsQuery(siteId));
  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <Facts
        title={t("settings.loginLimits.title")}
        rows={[[t("settings.loginLimits.unavailableLabel"), t("settings.loginLimits.unavailable")]]}
      />
    );
  }
  return <DataState query={query}>{(view) => <LoginLimitsCard siteId={siteId} view={view} />}</DataState>;
}

function LoginLimitsCard({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: View }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [open, setOpen] = useState(false);
  const limits = view.limits ?? {};
  const defaults = view.defaults ?? {};
  const rows = fields.map(({ name }): [string, ReactNode] => {
    const own = limits[name];
    const shown = own ?? defaults[name];
    return [
      t(`settings.loginLimits.field.${name}`),
      <span key={name}>
        {shown === undefined ? "—" : format.number(shown)}{" "}
        <State tone="neutral">{own === undefined ? t("settings.default") : t("settings.own")}</State>
      </span>,
    ];
  });
  return (
    <>
      <Facts
        title={t("settings.loginLimits.title")}
        actions={
          <ActionButton onClick={() => setOpen(true)} icon="pencil">
            {t("settings.loginLimits.edit")}
          </ActionButton>
        }
        rows={rows}
      />
      {open ? <LoginLimitsDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function LoginLimitsDialog({
  siteId,
  view,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  view: View;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const errors = useErrors();
  const limits = view.limits ?? {};
  const defaults = view.defaults ?? {};
  return (
    <ChangeDialog
      operation={`login-limits:${siteId}`}
      title={t("settings.loginLimits.edit")}
      intro={<p>{t("settings.loginLimits.intro")}</p>}
      action={t("settings.loginLimits.save")}
      fields={(error) => (
        <>
          {fields.map(({ name, min, max }) => (
            <Field
              key={name}
              label={t(`settings.loginLimits.field.${name}`)}
              hint={t("settings.loginLimits.hint", {
                min: format.number(min),
                max: format.number(max),
                default: defaults[name] === undefined ? "—" : format.number(defaults[name]),
              })}
              name={name}
              inputMode="numeric"
              autoComplete="off"
              defaultValue={limits[name]}
              error={errors.field(error, `limits/${name}`)}
            />
          ))}
        </>
      )}
      read={(values) => readBoundedInts(values, fields, t("settings.loginLimits.invalid"))}
      send={(next, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/login-limits", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { limits: next, reason },
          }),
        )
      }
      done={() => <p>{t("settings.loginLimits.saved")}</p>}
      onClose={onClose}
    />
  );
}
