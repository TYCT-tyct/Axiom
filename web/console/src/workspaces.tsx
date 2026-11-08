import { can, useSession } from "@axiom/app";
import { SelectField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { workspacesQuery } from "./queries";

// useWorkspaces is the account's workspaces and whether there is more than one. An account with one sees no workspace
// anywhere but settings (docs/domains/accounts.md「账户、成员、工作区」), and so does one whose list cannot be read: a
// node before workspaces answers 404, and a session without api_keys.view is not asked.
export function useWorkspaces() {
  const view = can(useSession(), "api_keys.view");
  const query = useQuery({ ...workspacesQuery, enabled: view });
  const list = query.data?.data ?? [];
  const named = (id: string) => list.find((workspace) => workspace.id === id)?.name;
  return { query, list, several: list.length > 1, named };
}

// WorkspaceField chooses the workspace a new key belongs to, among those keys can be made in, starting on the one the
// page was looking at, or the default when keys cannot be made there. It is there only when the account has several;
// with one, the field is left out and the key goes to the default workspace, as before.
export function WorkspaceField({ wanted }: { wanted: string | undefined }) {
  const { t } = useTranslation();
  const { list, several } = useWorkspaces();
  if (!several) {
    return null;
  }
  const usable = list.filter((workspace) => !workspace.disabled);
  const start = usable.find((workspace) => workspace.id === wanted) ?? usable.find((workspace) => workspace.is_default);
  return (
    <SelectField
      label={t("workspaces.key.field")}
      hint={t("workspaces.key.hint")}
      name="workspace_id"
      defaultValue={start?.id}
    >
      {usable.map((workspace) => (
        <option key={workspace.id} value={workspace.id}>
          {workspace.name}
        </option>
      ))}
    </SelectField>
  );
}
