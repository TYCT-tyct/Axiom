import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Button, DataState, Icon, TextField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { Facts } from "./parts";
import { systemSettingsQuery } from "./queries";

type Settings = AdminSchemas["SystemSettingsUpdate"]["settings"];

// The site's name and the registration mode the system settings operation still carries. The mode is edited on the
// registration card, which knows all three values; here it is sent back as it is, because that operation requires it.
export function SiteInformation({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const settings = useQuery(systemSettingsQuery(siteId));
  return <DataState query={settings}>{(view) => <Card siteId={siteId} view={view} />}</DataState>;
}

function Card({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: AdminSchemas["SystemSettings"] }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Facts
        title={t("settings.site.title")}
        rows={[[t("settings.site.name"), view.name ?? "—"]]}
        action={
          <Button size="md" onClick={() => setOpen(true)}>
            <Icon name="pencil" />
            {t("settings.site.edit")}
          </Button>
        }
      />
      {open ? <EditDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function EditDialog({
  siteId,
  view,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  view: AdminSchemas["SystemSettings"];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <ChangeDialog
      operation={`system-settings:${siteId}`}
      title={t("settings.site.edit")}
      intro={<p>{t("settings.site.intro")}</p>}
      action={t("settings.site.save")}
      fields={(error) => (
        <TextField
          label={t("settings.site.name")}
          name="name"
          autoComplete="off"
          maxLength={100}
          defaultValue={view.name ?? ""}
          error={errors.field(error, "settings/name")}
        />
      )}
      read={(values): Settings | Problem => {
        const name = (values.name ?? "").trim();
        if (!name || name.length > 100) {
          return { problem: t("settings.site.invalidName") };
        }
        // The operation keeps the registration mode when it is omitted, so open sites keep open registration.
        return { name };
      }}
      send={(settings, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/sites/{site_id}/system-settings", {
            params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
            body: { settings, reason },
          }),
        )
      }
      done={() => <p>{t("settings.site.saved")}</p>}
      onClose={onClose}
    />
  );
}
