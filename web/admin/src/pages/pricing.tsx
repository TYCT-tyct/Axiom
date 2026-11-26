import { can, useSession } from "@axiom/app";
import { ActionButton } from "@axiom/ui";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import enRates from "../locales/en/exchange-rates";
import en from "../locales/en/price-policy";
import zhRates from "../locales/zh/exchange-rates";
import zh from "../locales/zh/price-policy";
import { PricePolicy } from "../pricing/price-policy";
import { PricesTab } from "../pricing/prices-tab";
import { newRule, RulesTab } from "../pricing/rules-tab";
import { TabbedPage, useTabs } from "../tabbed-page";
import ExchangeRates from "./exchange-rates";

export const messages = {
  zh: { pricePolicy: zh, exchangeRates: zhRates },
  en: { pricePolicy: en, exchangeRates: enRates },
};

// Pricing in the admin's one page (adPg_pricing in admin_pages_b.js): the sale prices and what is waiting to be
// published, the rules they are made by, the exchange rates costs are turned into the site's currency with, and the
// policy every publication is held to.
export default function Pricing() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: "/app/pricing" });
  const tab: "prices" | "rules" | "rates" | "policy" = search.tab ?? "prices";
  const manage = can(useSession(), "pricing.manage");
  // What is being edited is the rule with this ID as the page has it now; the dialog is the rules tab's.
  const [editing, setEditing] = useState<string | null>(null);
  const tabs = useTabs(
    "/pricing",
    [
      { key: "prices", label: t("pricing.tabs.prices") },
      { key: "rules", label: t("pricing.tabs.rules") },
      { key: "rates", label: t("pricing.tabs.rates") },
      { key: "policy", label: t("pricing.tabs.policy") },
    ],
    tab,
  );
  return (
    <TabbedPage
      title={t("nav.pricing")}
      lede={tab === "prices" ? undefined : t(`pricing.lede.${tab}`)}
      label={t("pricing.tabs.label")}
      tabs={tabs}
      actions={
        tab === "rules" && manage ? (
          <ActionButton variant="pri" icon="plus" onClick={() => setEditing(newRule)}>
            {t("pricing.rule.new")}
          </ActionButton>
        ) : undefined
      }
    >
      {tab === "prices" ? (
        <PricesTab
          selected={search.price}
          onRules={() => void navigate({ to: "/pricing", search: { tab: "rules" } })}
        />
      ) : null}
      {tab === "rules" ? <RulesTab editing={editing} onEdit={setEditing} /> : null}
      {tab === "rates" ? <ExchangeRates /> : null}
      {tab === "policy" ? <PricePolicy /> : null}
    </TabbedPage>
  );
}
