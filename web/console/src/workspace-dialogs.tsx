import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, readForm, useErrors } from "@axiom/app";
import {
  Button,
  CheckField,
  DataState,
  Dialog,
  FormError,
  moneyInput,
  parseMoney,
  SubmitButton,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ResetField, useCurrency } from "./key-form";
import { workspaceBudgetQuery } from "./queries";

type Workspace = Schemas["Workspace"];

// A change to a workspace makes the list, the menu, the key form and its budget ask again: they share the key prefix.
function useRefreshWorkspaces() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: [accountKey, "workspaces"] });
}

// Actions are a dialog form's cancel and submit buttons.
export function Actions({ pending, submit, onCancel }: { pending: boolean; submit: string; onCancel: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex justify-end gap-2 pt-2">
      <Button size="md" onClick={onCancel}>
        {t("cancel", { ns: "common" })}
      </Button>
      <SubmitButton pending={pending}>{submit}</SubmitButton>
    </div>
  );
}

// WorkspaceForm is a workspace's name and, for one that is not the default, whether it is disabled. The server checks
// the name is free; the form only refuses an empty one.
function WorkspaceForm({
  workspace,
  pending,
  error,
  onSubmit,
  onCancel,
}: {
  workspace?: Workspace;
  pending: boolean;
  error: unknown;
  onSubmit: (values: Schemas["WorkspaceSettings"]) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const [missing, setMissing] = useState(false);
  return (
    <form
      noValidate
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        const values = readForm(event.currentTarget);
        const name = (values.name ?? "").trim();
        setMissing(!name);
        if (name && !pending) {
          onSubmit({ name, disabled: values.disabled === "on" });
        }
      }}
    >
      {error ? <FormError>{errors.message(error)}</FormError> : null}
      <TextField
        label={t("workspaces.form.name")}
        name="name"
        maxLength={100}
        defaultValue={workspace?.name}
        autoComplete="off"
        error={missing ? t("workspaces.form.nameMissing") : errors.field(error, "name")}
      />
      {workspace?.is_default ? <p className="text-label text-t3">{t("workspaces.form.defaultHint")}</p> : null}
      {workspace && !workspace.is_default ? (
        <CheckField
          label={t("workspaces.form.disable")}
          hint={t("workspaces.form.disableHint")}
          name="disabled"
          defaultChecked={workspace.disabled}
        />
      ) : null}
      <Actions
        pending={pending}
        submit={workspace ? t("workspaces.form.save") : t("workspaces.form.create")}
        onCancel={onCancel}
      />
    </form>
  );
}

export function CreateWorkspaceDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefreshWorkspaces();
  const create = useMutation({
    mutationFn: (body: Schemas["WorkspaceCreate"]) => unwrap(consoleApi.POST("/api/console/v1/workspaces", { body })),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
  });
  return (
    <Dialog title={t("workspaces.create")} onClose={onClose}>
      <WorkspaceForm
        pending={create.isPending}
        error={create.error}
        onCancel={onClose}
        onSubmit={({ name }) => create.mutate({ name })}
      />
    </Dialog>
  );
}

export function EditWorkspaceDialog({ workspace, onClose }: { workspace: Workspace; onClose: () => void }) {
  const { t } = useTranslation();
  const refresh = useRefreshWorkspaces();
  const update = useMutation({
    mutationFn: (body: Schemas["WorkspaceSettings"]) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/workspaces/{workspace_id}", {
          params: { path: { workspace_id: workspace.id } },
          body,
        }),
      ),
    onSuccess: async () => {
      await refresh();
      onClose();
    },
  });
  return (
    <Dialog title={t("workspaces.edit")} onClose={onClose}>
      <WorkspaceForm
        workspace={workspace}
        pending={update.isPending}
        error={update.error}
        onCancel={onClose}
        onSubmit={(body) => update.mutate(body)}
      />
    </Dialog>
  );
}

// BudgetDialog shows a workspace's budget and the current window's use, and lets a manager replace it. A node before
// workspace budgets answers 404, which says the budget is unavailable rather than that the workspace is gone.
export function BudgetDialog({
  workspace,
  manage,
  onClose,
}: {
  workspace: Workspace;
  manage: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const budget = useQuery(workspaceBudgetQuery(workspace.id));
  const unavailable = budget.error instanceof ApiError && budget.error.status === 404;
  return (
    <Dialog title={t("workspaces.budgetNamed", { name: workspace.name })} onClose={onClose}>
      {unavailable ? (
        <p className="text-t2">{t("workspaces.budget.unavailable")}</p>
      ) : (
        <DataState query={budget}>
          {(data) => (
            <div className="space-y-4">
              <Usage budget={data} />
              {manage ? <BudgetForm workspace={workspace} budget={data} onDone={onClose} /> : null}
            </div>
          )}
        </DataState>
      )}
    </Dialog>
  );
}

function Usage({ budget }: { budget: Schemas["WorkspaceBudget"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!budget.limit) {
    return <p className="text-t2">{t("workspaces.budget.none")}</p>;
  }
  const money = (value: Schemas["Money"] | undefined) => (value ? format.money(value) : "—");
  const lines = [
    t("workspaces.budget.used", { used: money(budget.used), limit: format.money(budget.limit) }),
    t("workspaces.budget.left", { left: money(budget.limit_remaining) }),
    budget.resets_at
      ? t("workspaces.budget.resets", { time: format.dateTime(new Date(budget.resets_at)) })
      : t("workspaces.budget.never"),
  ];
  return (
    <div className="space-y-1 tabular-nums">
      {lines.map((line) => (
        <p key={line} className="text-t1">
          {line}
        </p>
      ))}
      <p className="text-label text-t3">{t("workspaces.budget.usedHint")}</p>
    </div>
  );
}

// The budget's amount: nothing typed removes it, and an amount needs the currency it is in.
function readBudget(form: HTMLFormElement, currency: string | undefined): Schemas["WorkspaceBudgetSettings"] | null {
  const values = readForm(form);
  const text = (values.limit ?? "").trim();
  if (!text) {
    return {};
  }
  const amount = parseMoney(text);
  if (amount === null || !currency) {
    return null;
  }
  const reset = values.limit_reset ? (values.limit_reset as Schemas["LimitReset"]) : undefined;
  return { limit: { amount, currency }, limit_reset: reset };
}

function BudgetForm({
  workspace,
  budget,
  onDone,
}: {
  workspace: Workspace;
  budget: Schemas["WorkspaceBudget"];
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const queryClient = useQueryClient();
  const currency = useCurrency(budget.limit);
  const [invalid, setInvalid] = useState(false);
  const save = useMutation({
    mutationFn: (body: Schemas["WorkspaceBudgetSettings"]) =>
      unwrap(
        consoleApi.PUT("/api/console/v1/workspaces/{workspace_id}/budget", {
          params: { path: { workspace_id: workspace.id } },
          body,
        }),
      ),
    onSuccess: (next) => {
      queryClient.setQueryData(workspaceBudgetQuery(workspace.id).queryKey, next);
      onDone();
    },
  });
  return (
    <form
      noValidate
      className="space-y-4 border-t border-b1 pt-4"
      onSubmit={(event) => {
        event.preventDefault();
        const body = readBudget(event.currentTarget, currency.unit);
        setInvalid(body === null);
        if (body && !save.isPending) {
          save.mutate(body);
        }
      }}
    >
      {save.error ? <FormError>{errors.message(save.error)}</FormError> : null}
      <BudgetFields budget={budget} error={save.error} invalid={invalid} {...currency} />
      <Actions pending={save.isPending} submit={t("workspaces.budget.save")} onCancel={onDone} />
    </form>
  );
}

function BudgetFields({
  budget,
  error,
  invalid,
  unit,
  unavailable,
}: {
  budget: Schemas["WorkspaceBudget"];
  error: unknown;
  invalid: boolean;
  unit: string | undefined;
  unavailable: boolean;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  const waiting = unavailable ? t("keys.form.limitCurrencyFailed") : t("keys.form.limitCurrencyLoading");
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <TextField
        label={t("workspaces.budget.limit", { currency: unit ?? "" })}
        hint={unit ? t("workspaces.budget.limitHint") : waiting}
        name="limit"
        inputMode="decimal"
        autoComplete="off"
        disabled={!unit}
        defaultValue={budget.limit ? moneyInput(budget.limit.amount) : ""}
        error={invalid ? t("workspaces.budget.limitInvalid") : errors.field(error, "limit")}
      />
      <ResetField value={budget.limit_reset} />
    </div>
  );
}
