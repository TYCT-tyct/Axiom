import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, Stat, useSession } from "@axiom/app";
import { Button, DataState, Icon, PageHeader, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AccountActions } from "../account-actions";
import { CreditDialog } from "../credit-dialog";
import { KeyLimitsCard, KeyLimitsUnavailable } from "../key-limits";
import { Actions, Facts, PageBody, State, Time } from "../parts";
import { accountQuery } from "../queries";

type Account = AdminSchemas["AccountDetail"];

export default function AccountPage() {
  const { t } = useTranslation();
  const { accountId } = useParams({ from: "/app/accounts/$accountId" });
  const account = useQuery(accountQuery(accountId as AdminSchemas["AccountId"]));
  return (
    <DataState query={account} page>
      {(data) => (
        <>
          <PageHeader title={data.owner.email} description={t("users.account.description")} />
          <PageBody>
            <Actions>
              <Button asChild>
                <Link to="/users">
                  <Icon name="left" />
                  {t("users.account.back")}
                </Link>
              </Button>
              <GrantButton account={data} />
              <AccountActions account={data} />
            </Actions>
            <Figures account={data} />
            <Details account={data} />
            <AccountKeyLimits account={data} />
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function GrantButton({ account }: { account: Account }) {
  const { t } = useTranslation();
  const allowed = can(useSession(), "credits.grant");
  const [open, setOpen] = useState(false);
  if (!allowed) {
    return null;
  }
  return (
    <>
      <Button variant="primary" onClick={() => setOpen(true)}>
        <Icon name="plus" />
        {t("users.credit.open")}
      </Button>
      {open ? <CreditDialog account={account} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function Figures({ account }: { account: Account }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { balance } = account;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Stat
        label={t("users.account.available")}
        value={format.money(balance.available)}
        note={t("users.account.availableNote")}
      />
      <Stat
        label={t("users.account.spent")}
        value={format.money(balance.spent_this_month)}
        note={t("users.account.spentNote")}
      />
    </div>
  );
}

function Details({ account }: { account: Account }) {
  const { t } = useTranslation();
  const format = useFormat();
  const { balance } = account;
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Facts
        title={t("users.account.balanceTitle")}
        rows={[
          [t("users.account.balance"), format.money(balance.balance)],
          [t("users.account.reserved"), format.money(balance.reserved)],
          [t("users.account.floor"), format.money(balance.floor)],
        ]}
      />
      <Facts
        title={t("users.account.detailsTitle")}
        rows={[
          [t("users.account.owner"), account.owner.email],
          [t("users.account.name"), account.name],
          [t("users.account.kindLabel"), t(`users.account.kind.${account.kind}`)],
          [
            t("users.col.status"),
            <State key="status" tone={account.status === "active" ? "good" : "warn"}>
              {account.status === "active" ? t("users.active") : t("users.disabled")}
            </State>,
          ],
          [
            t("users.account.calls"),
            account.calls_suspended === undefined ? (
              t("users.account.unavailable")
            ) : (
              <State key="calls" tone={account.calls_suspended ? "warn" : "good"}>
                {account.calls_suspended ? t("users.account.callsSuspended") : t("users.account.callsActive")}
              </State>
            ),
          ],
          [
            t("users.account.signIn"),
            account.sign_in_disabled === undefined ? (
              t("users.account.unavailable")
            ) : (
              <State key="sign-in" tone={account.sign_in_disabled ? "warn" : "good"}>
                {account.sign_in_disabled ? t("users.account.signInDisabled") : t("users.account.signInActive")}
              </State>
            ),
          ],
          [t("users.account.tier"), account.tier.name],
          [t("users.account.siteId"), account.site_id],
          [t("users.col.created"), <Time key="created" at={account.created_at} />],
        ]}
      />
    </div>
  );
}

// What the account is held to on its keys: its own limits over the site's.
function AccountKeyLimits({ account }: { account: Account }) {
  const editable = can(useSession(), "sites.manage");
  // The contract always gives it; a node of the release before does not, and the rest of the page must still show.
  const view: AdminSchemas["KeyLimitsView"] | undefined = account.key_limits;
  if (view === undefined) {
    return <KeyLimitsUnavailable level="account" />;
  }
  return (
    <KeyLimitsCard
      operation={`key-limits:${account.id}`}
      level="account"
      view={view}
      editable={editable}
      send={(limits, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/accounts/{account_id}/key-limits", {
            params: { path: { account_id: account.id }, header: { "Idempotency-Key": key } },
            body: { limits, reason },
          }),
        )
      }
    />
  );
}
