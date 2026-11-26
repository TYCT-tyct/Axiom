import { can, useSession } from "@axiom/app";
import { PageBody, PageHead } from "@axiom/ui";
import { useLocation } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { RouteTabs } from "../row-drawer";
import { PlansTab } from "../subscriptions/plans-tab";
import { SubsTab } from "../subscriptions/subs-tab";

const plans = "/subscriptions/plans";

// 订阅管理: the subscriptions accounts hold, and the plans they were sold as. Subscriptions were a card on the account
// page and the plans a page of their own under pricing; here they are two tabs. /subscription-plans leads to the second
// (router.tsx).
export default function Subscriptions() {
  const { t } = useTranslation();
  const path = useLocation({ select: (location) => location.pathname.replace(/\/$/, "") });
  // The plans are the pricing page's to read.
  const showPlans = can(useSession(), "pricing.view");
  const current = path === plans && showPlans ? plans : "/subscriptions";
  return (
    <>
      <PageHead title={t("shell.nav.subs")} lede={current === plans ? t("subscriptions.plansLede") : undefined} />
      <PageBody>
        <RouteTabs
          label={t("subscriptions.tabs.label")}
          current={current}
          tabs={[
            { key: "/subscriptions", to: "/subscriptions", label: t("subscriptions.tabs.subs") },
            ...(showPlans ? [{ key: plans, to: plans, label: t("subscriptions.tabs.plans") }] : []),
          ]}
        />
        {current === plans ? <PlansTab /> : <SubsTab />}
      </PageBody>
    </>
  );
}

export { default as messages } from "../locales/subscriptions";
