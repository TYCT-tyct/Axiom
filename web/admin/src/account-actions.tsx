import { type AdminSchemas, adminApi, unwrapEmptyChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { ActionButton, type DiffRow, Select, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "./change";
import { tiersQuery } from "./queries";

type Account = AdminSchemas["AccountDetail"];
type Action = "suspension" | "signIn" | "tier";

// AccountActions holds the three switches the account page can throw. Each is its own audited state change; the calls
// switch and the sign-in switch are separate because pausing calls keeps the owner able to sign in and recharge.
export function AccountActions({ account }: { account: Account }) {
  const allowed = can(useSession(), "users.manage");
  if (!allowed) {
    return null;
  }
  return <ManageActions account={account} />;
}

function ManageActions({ account }: { account: Account }) {
  const { t } = useTranslation();
  const [action, setAction] = useState<Action | null>(null);
  const calls = account.calls_suspended;
  const signIn = account.sign_in_disabled;
  return (
    <>
      {calls === undefined ? null : (
        <ActionButton variant={calls ? "pri" : "sec"} onClick={() => setAction("suspension")}>
          {calls ? t("users.account.resume") : t("users.account.suspend")}
        </ActionButton>
      )}
      {signIn === undefined ? null : (
        <ActionButton variant={signIn ? "pri" : "sec"} onClick={() => setAction("signIn")}>
          {signIn ? t("users.account.enableSignIn") : t("users.account.disableSignIn")}
        </ActionButton>
      )}
      <ActionButton onClick={() => setAction("tier")}>{t("users.account.changeTier")}</ActionButton>
      {action === "suspension" && calls !== undefined ? (
        <SuspensionAction account={account} suspended={calls} onClose={() => setAction(null)} />
      ) : null}
      {action === "signIn" && signIn !== undefined ? (
        <SignInAction owner={account.owner} accountId={account.id} disabled={signIn} onClose={() => setAction(null)} />
      ) : null}
      {action === "tier" ? <TierDialog account={account} onClose={() => setAction(null)} /> : null}
    </>
  );
}

function SuspensionAction({
  account,
  suspended,
  onClose,
}: {
  account: Account;
  suspended: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const state = (off: boolean) => t(off ? "users.account.callsSuspended" : "users.account.callsActive");
  return (
    <StateDialog
      title={t(suspended ? "users.account.suspension.resumeTitle" : "users.account.suspension.title", {
        email: account.owner.email,
      })}
      intro={<p>{t(suspended ? "users.account.suspension.resumeIntro" : "users.account.suspension.intro")}</p>}
      operation={`account-suspension:${account.id}`}
      action={t(suspended ? "users.account.resume" : "users.account.suspend")}
      danger={!suspended}
      payload={{ suspended: !suspended }}
      diff={[{ label: t("users.account.calls"), before: state(suspended), after: state(!suspended) }]}
      toast={t(suspended ? "users.account.suspension.resumed" : "users.account.suspension.suspended", {
        email: account.owner.email,
      })}
      send={(body, reason, key) =>
        unwrapEmptyChange(
          adminApi.PUT("/api/admin/v1/accounts/{account_id}/suspension", {
            params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      onClose={onClose}
    />
  );
}

// Whether a user may sign in is the user's, so the dialog needs only the owner and the account's id (which names the
// operation): a user's drawer opens it without reading the account.
export function SignInAction({
  owner,
  accountId,
  disabled,
  onClose,
}: {
  owner: Account["owner"];
  accountId: AdminSchemas["AccountId"];
  disabled: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const state = (off: boolean) => t(off ? "users.account.signInDisabled" : "users.account.signInActive");
  return (
    <StateDialog
      title={t(disabled ? "users.account.signInChange.enableTitle" : "users.account.signInChange.title", {
        email: owner.email,
      })}
      intro={<p>{t(disabled ? "users.account.signInChange.enableIntro" : "users.account.signInChange.intro")}</p>}
      operation={`account-sign-in:${accountId}`}
      action={t(disabled ? "users.account.enableSignIn" : "users.account.disableSignIn")}
      danger={!disabled}
      payload={{ disabled: !disabled }}
      diff={[{ label: t("users.account.signIn"), before: state(disabled), after: state(!disabled) }]}
      toast={t(disabled ? "users.account.signInChange.enabled" : "users.account.signInChange.disabled", {
        email: owner.email,
      })}
      send={(body, reason, key) =>
        unwrapEmptyChange(
          adminApi.PUT("/api/admin/v1/users/{user_id}/sign-in", {
            params: { path: { user_id: owner.id }, header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      onClose={onClose}
    />
  );
}

// A switch is its own inverse: the toast's undo sends it back to the state it was read in.
function StateDialog<Input extends Record<string, boolean>>({
  title,
  intro,
  operation,
  action,
  danger,
  payload,
  diff,
  toast,
  send,
  onClose,
}: {
  title: string;
  intro: ReactNode;
  operation: string;
  action: string;
  danger: boolean;
  payload: Input;
  diff: readonly DiffRow[];
  toast: string;
  send: (body: Input, reason: string, key: string) => Promise<{ data: null; replayed: boolean }>;
  onClose: () => void;
}) {
  const inverse = Object.fromEntries(Object.entries(payload).map(([name, on]) => [name, !on])) as Input;
  return (
    <ChangeDialog
      title={title}
      intro={intro}
      operation={operation}
      action={action}
      danger={danger}
      diff={diff}
      read={() => payload}
      send={(body, { reason, key }) => send(body, reason, key)}
      toast={() => toast}
      undo={(_, { reason, key }) => send(inverse, reason, key)}
      done={() => null}
      onClose={onClose}
    />
  );
}

function TierDialog({ account, onClose }: { account: Account; onClose: () => void }) {
  const { t } = useTranslation();
  const tiers = useQuery(tiersQuery(account.site_id));
  const putTier = (body: { tier_id: string }, reason: string, key: string) =>
    unwrapEmptyChange(
      adminApi.PUT("/api/admin/v1/accounts/{account_id}/tier", {
        params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
        body: { ...body, reason },
      }),
    );
  return (
    <ChangeDialog
      title={t("users.account.tierChange.title", { email: account.owner.email })}
      intro={<p>{t("users.account.tierChange.intro")}</p>}
      operation={`account-tier:${account.id}`}
      action={t("users.account.changeTier")}
      fields={(error) => (
        <TierField current={account.tier.id} tiers={tiers.data?.data} loading={tiers.isPending} error={error} />
      )}
      read={(values): { tier_id: string } | Problem =>
        values.tier_id && values.tier_id !== account.tier.id
          ? { tier_id: values.tier_id }
          : { problem: t("users.account.tierChange.choose") }
      }
      send={(body, { reason, key }) => putTier(body, reason, key)}
      toast={() => t("users.account.tierChange.done", { email: account.owner.email })}
      undo={(_, { reason, key }) => putTier({ tier_id: account.tier.id }, reason, key)}
      done={() => null}
      onClose={onClose}
    />
  );
}

function TierField({
  current,
  tiers,
  loading,
  error,
}: {
  current: AdminSchemas["TierId"];
  tiers: AdminSchemas["TierRef"][] | undefined;
  loading: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const errors = useErrors();
  if (loading) {
    return <Text tone="faint">{t("users.account.tierChange.loading")}</Text>;
  }
  if (tiers === undefined) {
    return <Text tone="faint">{t("users.account.tierChange.unavailable")}</Text>;
  }
  return (
    <Select
      label={t("users.account.tierChange.field")}
      name="tier_id"
      defaultValue={current}
      error={errors.field(error, "tier_id")}
    >
      {tiers.map((tier) => (
        <option key={tier.id} value={tier.id}>
          {tier.name}
        </option>
      ))}
    </Select>
  );
}
