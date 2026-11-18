import { readForm } from "@axiom/app";
import { Button, Cluster, Dialog, Grid, SelectField, Stack, SubmitButton, TextField } from "@axiom/ui";
import { Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toInstant, toLocalInput } from "../parts";
import type { KeyFilters } from "../queries";
import { useWorkspaces } from "../workspaces";

// What the account's key list is narrowed by. The board's list has no filters, but an account that has keys already
// narrows them (L8 in /home/ai/axiom-ops/design/console-gap.md), so the page keeps them behind a button in the list's
// header and puts them in the address: a reload, the back button and a link keep them.

export const noFilters: KeyFilters = {
  name: undefined,
  prefix: undefined,
  status: undefined,
  created_after: undefined,
  created_before: undefined,
  workspace_id: undefined,
};

export const hasFilters = (filters: KeyFilters) => Object.values(filters).some(Boolean);

function statusOf(value: string | undefined): KeyFilters["status"] {
  return value === "active" || value === "disabled" || value === "expired" ? value : undefined;
}

function filtersOf(form: HTMLFormElement): KeyFilters {
  const values = readForm(form);
  return {
    name: values.name?.trim() || undefined,
    prefix: values.prefix?.trim() || undefined,
    status: statusOf(values.status),
    created_after: toInstant(values.created_after),
    created_before: toInstant(values.created_before),
    workspace_id: values.workspace_id || undefined,
  };
}

// KeyFiltersDialog asks for the filters and applies them to the address; the list reads them from there, so the dialog
// itself holds no state.
export function KeyFiltersDialog({ filters, onClose }: { filters: KeyFilters; onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Dialog title={t("keys.board.filterTitle")} onClose={onClose}>
      <form
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          const search = filtersOf(event.currentTarget);
          onClose();
          void navigate({ to: "/keys", search });
        }}
      >
        <Stack gap={16}>
          <FilterFields filters={filters} />
          <Cluster justify="end">
            <Button size="md" asChild>
              <Link to="/keys" search={noFilters} onClick={onClose}>
                {t("keys.filter.reset")}
              </Link>
            </Button>
            <SubmitButton pending={false}>{t("keys.filter.apply")}</SubmitButton>
          </Cluster>
        </Stack>
      </form>
    </Dialog>
  );
}

// The workspace filter is offered once there is a second workspace. A workspace in the address that the list no longer
// has stays chosen, so applying the other filters does not drop it.
function WorkspaceFilter({ value }: { value: string | undefined }) {
  const { t } = useTranslation();
  const { list, several, named } = useWorkspaces();
  if (!several) {
    return value ? <input type="hidden" name="workspace_id" value={value} /> : null;
  }
  return (
    <SelectField label={t("workspaces.key.filter")} name="workspace_id" defaultValue={value ?? ""}>
      <option value="">{t("workspaces.key.any")}</option>
      {value && !named(value) ? <option value={value}>{value}</option> : null}
      {list.map((workspace) => (
        <option key={workspace.id} value={workspace.id}>
          {workspace.name}
        </option>
      ))}
    </SelectField>
  );
}

// FilterFields is the filters themselves, as the console's form fields: what to match by, and the one the address may
// carry from a workspace the list no longer offers.
function FilterFields({ filters }: { filters: KeyFilters }) {
  const { t } = useTranslation();
  return (
    <Grid columns={2} gap={16}>
      <TextField
        label={t("keys.filter.name")}
        name="name"
        maxLength={100}
        defaultValue={filters.name}
        autoComplete="off"
      />
      <TextField
        label={t("keys.filter.prefix")}
        name="prefix"
        maxLength={16}
        defaultValue={filters.prefix}
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField label={t("keys.filter.status")} name="status" defaultValue={filters.status ?? ""}>
        <option value="">{t("keys.filter.anyStatus")}</option>
        <option value="active">{t("keys.enabled")}</option>
        <option value="disabled">{t("keys.disabled")}</option>
        <option value="expired">{t("keys.expired")}</option>
      </SelectField>
      <TextField
        label={t("keys.filter.createdAfter")}
        name="created_after"
        type="datetime-local"
        defaultValue={toLocalInput(filters.created_after)}
      />
      <TextField
        label={t("keys.filter.createdBefore")}
        name="created_before"
        type="datetime-local"
        defaultValue={toLocalInput(filters.created_before)}
      />
      <WorkspaceFilter value={filters.workspace_id} />
      <WorkspaceFilter value={filters.workspace_id} />
    </Grid>
  );
}
