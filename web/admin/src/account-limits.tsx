import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Cluster, DrawerFacts, DrawerSection, Text, useFormat } from "@axiom/ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyLimitsDialog } from "./key-limits";
import { State } from "./parts";
import { EditWorkspaceLimit } from "./workspace-limit";

type Account = AdminSchemas["AccountDetail"];
type Editing = "keys" | "workspaces";

// The three caps an account is held to (the account drawer's 「这个账户的上限」): how many keys it may keep, how many it may
// make in an hour, and how many workspaces. Each shows where it comes from, its own or the site's; the first two are one
// change, as the contract has them, and the workspaces' another. A node of the release before has no workspace cap, and
// the row says so while the rest works.
export function AccountLimits({ account }: { account: Account }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState<Editing>();
  return (
    <DrawerSection
      title={t("users.account.limitsTitle")}
      aside={
        <Text tone="faint" size="sm">
          {t("users.account.limitsAside")}
        </Text>
      }
    >
      <LimitRows account={account} edit={setEditing} />
      <LimitDialogs account={account} editing={editing} onClose={() => setEditing(undefined)} />
    </DrawerSection>
  );
}

function LimitRows({ account, edit }: { account: Account; edit: (what: Editing) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const editable = can(useSession(), "sites.manage");
  // The contract always gives them; a node of the release before does not, and the rest must still show.
  const keys: AdminSchemas["KeyLimitsView"] | undefined = account.key_limits;
  const workspaces: AdminSchemas["WorkspaceLimitView"] | undefined = account.workspace_limit;
  const row = (label: string, effective: number | undefined, own: boolean, what: Editing) =>
    [
      label,
      effective === undefined ? (
        t("users.account.unavailable")
      ) : (
        <Cluster key={label} justify="end">
          {format.number(effective)}
          <State tone="neutral">{t(own ? "settings.keyLimits.own" : "settings.keyLimits.from.account")}</State>
          {editable ? (
            <ActionButton size="sm" variant="gho" onClick={() => edit(what)}>
              {t("users.account.editLimit")}
            </ActionButton>
          ) : null}
        </Cluster>
      ),
    ] as const;
  return (
    <DrawerFacts
      rows={[
        row(
          t("settings.keyLimits.field.max_keys"),
          keys?.effective.max_keys,
          keys?.limits.max_keys !== undefined,
          "keys",
        ),
        row(
          t("settings.keyLimits.field.creations_per_hour"),
          keys?.effective.creations_per_hour,
          keys?.limits.creations_per_hour !== undefined,
          "keys",
        ),
        row(
          t("settings.workspaceLimit.field"),
          workspaces?.effective,
          workspaces?.max_workspaces !== undefined,
          "workspaces",
        ),
      ]}
    />
  );
}

// The dialog of the change being made, with its operation named for the account so an unsettled try is met again.
function LimitDialogs({
  account,
  editing,
  onClose,
}: {
  account: Account;
  editing: Editing | undefined;
  onClose: () => void;
}) {
  const keys: AdminSchemas["KeyLimitsView"] | undefined = account.key_limits;
  const workspaces: AdminSchemas["WorkspaceLimitView"] | undefined = account.workspace_limit;
  if (editing === "keys" && keys) {
    return (
      <KeyLimitsDialog
        operation={`key-limits:${account.id}`}
        level="account"
        view={keys}
        send={(limits, { reason, key }) =>
          unwrapChange(
            adminApi.PUT("/api/admin/v1/accounts/{account_id}/key-limits", {
              params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
              body: { limits, reason },
            }),
          )
        }
        onClose={onClose}
      />
    );
  }
  if (editing === "workspaces" && workspaces) {
    return (
      <EditWorkspaceLimit
        operation={`workspace-limit:${account.id}`}
        level="account"
        view={workspaces}
        send={(max_workspaces, { reason, key }) =>
          unwrapChange(
            adminApi.PUT("/api/admin/v1/accounts/{account_id}/workspace-limit", {
              params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
              body: { ...(max_workspaces !== undefined ? { max_workspaces } : {}), reason },
            }),
          )
        }
        onClose={onClose}
      />
    );
  }
  return null;
}
