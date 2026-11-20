import { ApiError, type Schemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  Badge,
  Button,
  Cluster,
  DataState,
  EmptyState,
  Icon,
  PageBody,
  PageHeader,
  Panel,
  Table,
  Td,
  Text,
  Th,
} from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { BudgetDialog, CreateWorkspaceDialog, EditWorkspaceDialog } from "../workspace-dialogs";
import { useWorkspaces } from "../workspaces";

type Workspace = Schemas["Workspace"];
type Dialog = { kind: "create" } | { kind: "edit"; workspace: Workspace } | { kind: "budget"; workspace: Workspace };

// Workspaces lists the account's workspaces and lets a manager make, rename, disable and budget them. It is in the menu
// once there are two; settings links here so the second can be made.
export default function Workspaces() {
  const { t } = useTranslation();
  const manage = can(useSession(), "workspaces.manage");
  const { view, query, list } = useWorkspaces();
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const close = () => setDialog(null);
  // A node before workspaces answers 404 to the list; that is not a missing page but a feature not there yet.
  const unavailable = query.error instanceof ApiError && query.error.status === 404;
  return (
    <>
      <PageHeader title={t("workspaces.title")} description={t("workspaces.description")} />
      <PageBody>
        {manage && view && !unavailable ? (
          <Button variant="primary" size="md" onClick={() => setDialog({ kind: "create" })}>
            <Icon name="plus" />
            {t("workspaces.create")}
          </Button>
        ) : null}
        <Panel>
          {/* Without api_keys.view the list is never asked for, so it would wait forever: say why instead. */}
          {!view ? (
            <EmptyState title={t("workspaces.noPermission.title")}>{t("workspaces.noPermission.body")}</EmptyState>
          ) : unavailable ? (
            <EmptyState title={t("workspaces.unavailable.title")}>{t("workspaces.unavailable.body")}</EmptyState>
          ) : (
            <DataState query={query}>
              {() => (
                <WorkspaceTable
                  workspaces={list}
                  manage={manage}
                  onEdit={(workspace) => setDialog({ kind: "edit", workspace })}
                  onBudget={(workspace) => setDialog({ kind: "budget", workspace })}
                />
              )}
            </DataState>
          )}
        </Panel>
      </PageBody>
      {dialog?.kind === "create" ? <CreateWorkspaceDialog onClose={close} /> : null}
      {dialog?.kind === "edit" ? <EditWorkspaceDialog workspace={dialog.workspace} onClose={close} /> : null}
      {dialog?.kind === "budget" ? <BudgetDialog workspace={dialog.workspace} manage={manage} onClose={close} /> : null}
    </>
  );
}

function WorkspaceTable({
  workspaces,
  manage,
  onEdit,
  onBudget,
}: {
  workspaces: Workspace[];
  manage: boolean;
  onEdit: (workspace: Workspace) => void;
  onBudget: (workspace: Workspace) => void;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("workspaces.title")}>
      <thead>
        <tr>
          <Th>{t("workspaces.col.name")}</Th>
          <Th>{t("workspaces.col.status")}</Th>
          <Th shrink />
        </tr>
      </thead>
      <tbody>
        {workspaces.map((workspace) => (
          <tr key={workspace.id}>
            <Td main>
              <Text>{workspace.name}</Text>
              {workspace.is_default ? <Badge>{t("workspaces.default")}</Badge> : null}
            </Td>
            <Td>
              <Badge tone={workspace.disabled ? "warn" : "ok"} icon={workspace.disabled ? "alert" : "check"}>
                {workspace.disabled ? t("keys.disabled") : t("keys.enabled")}
              </Badge>
            </Td>
            <Td shrink>
              <Actions workspace={workspace} manage={manage} onEdit={onEdit} onBudget={onBudget} />
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

// A row's ways on: its keys, its budget (read by anyone who sees the list, changed by a manager), and its settings.
function Actions({
  workspace,
  manage,
  onEdit,
  onBudget,
}: {
  workspace: Workspace;
  manage: boolean;
  onEdit: (workspace: Workspace) => void;
  onBudget: (workspace: Workspace) => void;
}) {
  const { t } = useTranslation();
  return (
    <Cluster justify="end">
      <Button size="sm" asChild>
        <Link
          to="/keys"
          search={{
            name: undefined,
            prefix: undefined,
            status: undefined,
            created_after: undefined,
            created_before: undefined,
            workspace_id: workspace.id,
          }}
          aria-label={t("workspaces.keysNamed", { name: workspace.name })}
        >
          {t("workspaces.keys")}
        </Link>
      </Button>
      <Button
        size="sm"
        aria-label={t("workspaces.budgetNamed", { name: workspace.name })}
        onClick={() => onBudget(workspace)}
      >
        {t("workspaces.budget.title")}
      </Button>
      {manage ? (
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("workspaces.editNamed", { name: workspace.name })}
          onClick={() => onEdit(workspace)}
        >
          <Icon name="pencil" />
        </Button>
      ) : null}
    </Cluster>
  );
}
