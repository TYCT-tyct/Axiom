import { type AdminSchemas, ApiError } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, DataState, DrawerFacts, DrawerSection, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { GrantSubscriptionDialog } from "./account-subscription-dialogs";
import { State } from "./parts";
import { accountSubscriptionsQuery } from "./subscription-queries";

type Account = AdminSchemas["AccountDetail"];

// An account's subscriptions in its drawer (the design's 「订阅」 line): each plan with where it stands and until when, and,
// with credits.grant, the grant. The window usage and the two-person refund are on 订阅管理, which the section links to.
// A node with no subscriptions leaves the section out.
export function AccountSubscriptions({ account }: { account: Account }) {
  const { t } = useTranslation();
  const format = useFormat();
  const canGrant = can(useSession(), "credits.grant");
  const query = useQuery(accountSubscriptionsQuery(account.id));
  const [granting, setGranting] = useState(false);
  if (query.error instanceof ApiError && query.error.status === 404) {
    return null;
  }
  return (
    <DrawerSection
      title={t("subscriptions.account.title")}
      aside={
        canGrant ? (
          <ActionButton size="sm" variant="gho" icon="plus" onClick={() => setGranting(true)}>
            {t("subscriptions.account.grant")}
          </ActionButton>
        ) : null
      }
    >
      <DataState
        query={query}
        isEmpty={(data) => data.data.length === 0}
        empty={<Text tone="muted">{t("subscriptions.account.emptyBody")}</Text>}
      >
        {(data) => (
          <>
            <DrawerFacts
              rows={data.data.map((sub) => [
                sub.plan_name,
                <span>
                  <State tone={sub.state === "active" ? "good" : "neutral"}>
                    {t(`subscriptions.account.state.${sub.state}`)}
                  </State>{" "}
                  {t("subscriptions.account.termUntil", { ends: format.dayFull(new Date(sub.ends_at)) })}
                </span>,
                sub.id,
              ])}
            />
            <Text size="sm" tone="muted">
              <Link to="/subscriptions" search={{ q: account.id }}>
                {t("subscriptions.account.manage")}
              </Link>
            </Text>
          </>
        )}
      </DataState>
      {granting ? <GrantSubscriptionDialog account={account} onClose={() => setGranting(false)} /> : null}
    </DrawerSection>
  );
}
