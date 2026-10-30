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

// siteSettingsInput is only the name: the operation keeps whatever registration mode is current when it writes, under
// the site row lock, so a mode another administrator changed while this form was open is not put back. Sending the mode
// read with the form would overwrite that change; an older node therefore refuses a name-only save rather than risking
// it, and the dialog says so.
export function siteSettingsInput(name: string): Settings {
  return { name };
}

// nameEditable is the capability a node of this release reports; an older node omits it, and the page then leaves the
// name read-only rather than sending a registration mode it read earlier.
export function nameEditable(view: AdminSchemas["SystemSettings"]): boolean {
  return view.name_editable === true;
}

// The site's name. The registration mode is edited on the registration card, which knows all three values.
export function SiteInformation({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const settings = useQuery(systemSettingsQuery(siteId));
  return <DataState query={settings}>{(view) => <Card siteId={siteId} view={view} />}</DataState>;
}

function Card({ siteId, view }: { siteId: AdminSchemas["SiteId"]; view: AdminSchemas["SystemSettings"] }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const editable = nameEditable(view);
  return (
    <>
      <Facts
        title={t("settings.site.title")}
        rows={[[t("settings.site.name"), view.name ?? "—"]]}
        action={
          editable ? (
            <Button size="md" onClick={() => setOpen(true)}>
              <Icon name="pencil" />
              {t("settings.site.edit")}
            </Button>
          ) : undefined
        }
      />
      {editable ? null : <p className="text-t2">{t("settings.site.oldNode")}</p>}
      {editable && open ? <EditDialog siteId={siteId} view={view} onClose={() => setOpen(false)} /> : null}
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
        return siteSettingsInput(name);
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
