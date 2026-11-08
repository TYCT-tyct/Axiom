import { can, useSession } from "@axiom/app";
import { SelectField } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { workspacesQuery } from "./queries";

// useWorkspaces is the account's workspaces and whether there is more than one. An account with one sees no workspace
// anywhere but settings (docs/domains/accounts.md「账户、成员、工作区」), and so does one whose list cannot be read: a
// node before workspaces answers 404, and a session without api_keys.view is not asked. scopeOf names the workspace
// whose presets a key of workspace id chooses from: none for the default one, which the account-wide reads answer, nor
// for one the list does not have.
export function useWorkspaces() {
  const view = can(useSession(), "api_keys.view");
  const query = useQuery({ ...workspacesQuery, enabled: view });
  const list = query.data?.data ?? [];
  const named = (id: string) => list.find((workspace) => workspace.id === id)?.name;
  const scopeOf = (id: string | undefined) =>
    list.some((workspace) => workspace.id === id && !workspace.is_default) ? id : undefined;
  return { view, query, list, several: list.length > 1, named, scopeOf };
}

export type WorkspaceChoice = ReturnType<typeof useWorkspaceChoice>;

// useWorkspaceChoice holds the workspace a new key is made in. It starts, once the list is read, on the one the page
// was looking at, or the default when keys cannot be made there; from then on only the user moves it. A choice that a
// later read finds disabled stays chosen and is lost: the form says so and refuses to submit, rather than making the key
// in another workspace, against another budget, without being asked. With one workspace there is no choice.
export function useWorkspaceChoice(wanted: string | undefined) {
  const { list, several, named, scopeOf } = useWorkspaces();
  const [chosen, choose] = useState<string>();
  const usable = list.filter((workspace) => !workspace.disabled);
  if (several && chosen === undefined) {
    const start =
      usable.find((workspace) => workspace.id === wanted) ?? usable.find((workspace) => workspace.is_default);
    if (start) {
      choose(start.id);
    }
  }
  const id = several ? chosen : undefined;
  const lost = id !== undefined && !usable.some((workspace) => workspace.id === id);
  return { id, choose, usable, lost, name: id ? named(id) : undefined, scope: scopeOf(id) };
}

// WorkspaceField chooses the workspace a new key belongs to, among those keys can be made in, and keeps a choice that
// became unusable in view with the reason. It is there only when the account has several.
export function WorkspaceField({ choice }: { choice: WorkspaceChoice }) {
  const { t } = useTranslation();
  if (choice.id === undefined) {
    return null;
  }
  return (
    <SelectField
      label={t("workspaces.key.field")}
      hint={t("workspaces.key.hint")}
      name="workspace_id"
      value={choice.id}
      onChange={(event) => choice.choose(event.currentTarget.value)}
      error={choice.lost ? t("workspaces.key.lost", { name: choice.name ?? choice.id }) : undefined}
    >
      {choice.lost ? <option value={choice.id}>{choice.name ?? choice.id}</option> : null}
      {choice.usable.map((workspace) => (
        <option key={workspace.id} value={workspace.id}>
          {workspace.name}
        </option>
      ))}
    </SelectField>
  );
}
