import type { AdminSchemas } from "@axiom/api";
import { DataState, DrawerFacts, Figure, TextLink, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AccountActions } from "../account-actions";
import { AccountLimits } from "../account-limits";
import { AccountSubscriptions } from "../account-subscriptions";
import { GrantButton } from "../credit-dialog";
import { State } from "../parts";
import { accountQuery } from "../queries";
import { type IdDrawer, RowDrawer } from "../row-drawer";

type Account = AdminSchemas["AccountDetail"];

// One account in a drawer: its balance and how much of it is held for calls in flight, its tier, who owns it and where
// it stands, its three caps and subscriptions; and below, what an operator may do to it: pause its calls, stop its
// owner signing in, change its tier, give it credit (no undo: nothing takes credit back). The account is read by its ID,
// so a drawer opened from a link needs no list.
export function AccountDrawer({ id, keys, select }: IdDrawer) {
  const { t } = useTranslation();
  const query = useQuery(accountQuery(id as AdminSchemas["AccountId"]));
  const account = query.data;
  if (!account) {
    return (
      <RowDrawer title={t("users.account.title")} keys={keys} selected={id} select={select}>
        <DataState query={query}>{() => null}</DataState>
      </RowDrawer>
    );
  }
  return (
    <RowDrawer
      title={account.owner.email}
      subtitle={t("users.account.subtitle", { kind: t(`users.account.kind.${account.kind}`), tier: account.tier.name })}
      keys={keys}
      selected={id}
      select={select}
      footer={
        <>
          <AccountActions account={account} />
          <GrantButton account={account} />
        </>
      }
    >
      <AccountBody account={account} />
    </RowDrawer>
  );
}

function AccountBody({ account }: { account: Account }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { balance } = account;
  const held = BigInt(balance.reserved.amount) > 0n;
  return (
    <>
      <Figure
        value={format.money(balance.balance)}
        caption={
          held
            ? t("users.account.balanceHeld", { reserved: format.money(balance.reserved) })
            : t("users.account.balanceOnly")
        }
      />
      <DrawerFacts
        rows={[
          [t("users.account.spent"), format.money(balance.spent_this_month)],
          [t("users.account.available"), format.money(balance.available)],
          [t("users.account.tier"), account.tier.name],
          [
            t("users.account.owner"),
            <TextLink key="owner">
              <Link to="/users" search={{ q: account.owner.email }}>
                {account.owner.email}
              </Link>
            </TextLink>,
          ],
          [
            t("users.col.status"),
            <State key="status" tone={account.status === "active" ? "good" : "warn"}>
              {t(account.status === "active" ? "users.active" : "users.disabled")}
            </State>,
          ],
          [t("users.account.calls"), <Calls key="calls" account={account} />],
          [t("users.account.signIn"), <SignIn key="sign-in" account={account} />],
          [t("users.col.created"), format.dateTime(new Date(account.created_at))],
        ]}
      />
      <AccountLimits account={account} />
      <AccountSubscriptions account={account} />
    </>
  );
}

function Calls({ account }: { account: Account }) {
  const { t } = useTranslation();
  if (account.calls_suspended === undefined) return t("users.account.unavailable");
  return (
    <State tone={account.calls_suspended ? "warn" : "good"}>
      {t(account.calls_suspended ? "users.account.callsSuspended" : "users.account.callsActive")}
    </State>
  );
}

function SignIn({ account }: { account: Account }) {
  const { t } = useTranslation();
  if (account.sign_in_disabled === undefined) return t("users.account.unavailable");
  return (
    <State tone={account.sign_in_disabled ? "warn" : "good"}>
      {t(account.sign_in_disabled ? "users.account.signInDisabled" : "users.account.signInActive")}
    </State>
  );
}
