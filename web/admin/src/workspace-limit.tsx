import type { AdminSchemas } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { Field } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { readBoundedInts } from "./key-limits";

type View = AdminSchemas["WorkspaceLimitView"];
type Level = "site" | "account";
type Send = (
  maxWorkspaces: number | undefined,
  context: { reason: string; key: string },
) => Promise<{ data: View; replayed: boolean }>;

type Props = {
  operation: string;
  level: Level;
  view: View;
  send: Send;
  onClose: () => void;
};

const bounds = [{ name: "max_workspaces", min: 1, max: 1_000 }] as const;
const [{ min, max }] = bounds;

export function EditWorkspaceLimit({ operation, level, view, send, onClose }: Props) {
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
