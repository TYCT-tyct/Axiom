import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, DataState, Field, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { readBoundedInts } from "./key-limits";
import { Facts, State } from "./parts";
import { siteWorkspaceLimitQuery } from "./queries";

type View = AdminSchemas["WorkspaceLimitView"];
type Level = "site" | "account";
type Send = (
  maxWorkspaces: number | undefined,
  context: { reason: string; key: string },
) => Promise<{ data: View; replayed: boolean }>;

type CardProps = {
  operation: string;
  level: Level;
  view: View;
  send: Send;
  editable: boolean;
};

const bounds = [{ name: "max_workspaces", min: 1, max: 1_000 }] as const;
const [{ min, max }] = bounds;

export function WorkspaceLimitUnavailable({ level }: { level: Level }) {
  const { t } = useTranslation();
  return (
    <Facts
      title={t(`settings.workspaceLimit.title.${level}`)}
      rows={[[t("settings.workspaceLimit.unavailableLabel"), t("settings.workspaceLimit.unavailable")]]}
    />
  );
}

// WorkspaceLimitCard shows the active workspace ceiling for a site or account and whether it is set at this level or
// inherited from the level above.
export function WorkspaceLimitCard(props: CardProps) {
  const { level, view, editable } = props;
  const { t } = useTranslation();
  const format = useFormat();
  const [open, setOpen] = useState(false);
  const source =
    view.max_workspaces === undefined ? t(`settings.workspaceLimit.from.${level}`) : t("settings.workspaceLimit.own");
  return (
    <>
      <Facts
        title={t(`settings.workspaceLimit.title.${level}`)}
        actions={
          editable ? (
            <ActionButton onClick={() => setOpen(true)} icon="pencil">
              {t("settings.workspaceLimit.edit")}
            </ActionButton>
          ) : undefined
        }
        rows={[
          [
            t("settings.workspaceLimit.field"),
            <span key="max">
              {format.number(view.effective)} <State tone="neutral">{source}</State>
            </span>,
          ],
        ]}
      />
      {open ? <EditWorkspaceLimit {...props} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function EditWorkspaceLimit({
  operation,
  level,
  view,
  send,
  onClose,
}: Omit<CardProps, "editable"> & { onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const invalid = t("settings.workspaceLimit.invalid", { min, max });
  return (
    <ChangeDialog
      operation={operation}
      title={t("settings.workspaceLimit.edit")}
      intro={<p>{t(`settings.workspaceLimit.intro.${level}`)}</p>}
      action={t("settings.workspaceLimit.save")}
      fields={(error) => (
        <Field
          label={t("settings.workspaceLimit.field")}
          hint={t(`settings.workspaceLimit.hint.${level}`, { min, max })}
          name="max_workspaces"
          inputMode="numeric"
          autoComplete="off"
          defaultValue={view.max_workspaces}
          error={errors.field(error, "max_workspaces")}
        />
      )}
      read={(values) => readBoundedInts(values, bounds, invalid)}
      send={(input, context) => send(input.max_workspaces, context)}
      done={() => <p>{t("settings.workspaceLimit.saved")}</p>}
      onClose={onClose}
    />
  );
}

export function SiteWorkspaceLimit({ siteId }: { siteId: AdminSchemas["SiteId"] }) {
  const limit = useQuery(siteWorkspaceLimitQuery(siteId));
  if (limit.error instanceof ApiError && limit.error.status === 404) {
    return <WorkspaceLimitUnavailable level="site" />;
  }
  return (
    <DataState query={limit}>
      {(view) => (
        <WorkspaceLimitCard
          operation={`workspace-limit:${siteId}`}
          level="site"
          view={view}
          editable
          send={(max_workspaces, { reason, key }) =>
            unwrapChange(
              adminApi.PUT("/api/admin/v1/sites/{site_id}/workspace-limit", {
                params: { path: { site_id: siteId }, header: { "Idempotency-Key": key } },
                body: { ...(max_workspaces !== undefined ? { max_workspaces } : {}), reason },
              }),
            )
          }
        />
      )}
    </DataState>
  );
}
