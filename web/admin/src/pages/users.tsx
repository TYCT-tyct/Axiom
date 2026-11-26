import { can, useSession } from "@axiom/app";
import { PageBody, PageHead } from "@axiom/ui";
import { useLocation } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import enGrants from "../locales/en/platform-grants";
import enUsers from "../locales/en/users";
import settingsMessages from "../locales/settings";
import subscriptionMessages from "../locales/subscriptions";
import zhGrants from "../locales/zh/platform-grants";
import zhUsers from "../locales/zh/users";
import { RouteTabs } from "../row-drawer";
import { AccountsTab } from "../users/accounts-tab";
import { RolesTab } from "../users/roles-tab";
import { UsersTab } from "../users/users-tab";

const tabs = { "/users": UsersTab, "/users/accounts": AccountsTab, "/users/roles": RolesTab } as const;
type Tab = keyof typeof tabs;

// 用户管理: the people who signed up, the accounts they own and the platform roles that let some of them into the admin,
// as three tabs of one page. Users and accounts are apart: an account is the money and the limits, a user is a person
// who signs in. The old /accounts/{id} address leads to the account's drawer (router.tsx).
export default function Users() {
  const { t } = useTranslation();
  const path = useLocation({ select: (location) => location.pathname.replace(/\/$/, "") || "/" }) as Tab;
  const roles = can(useSession(), "platform_grants.manage");
  const Current = tabs[path] ?? UsersTab;
  return (
    <>
      <PageHead title={t("shell.nav.users")} />
      <PageBody>
        <RouteTabs
          label={t("users.tabs.label")}
          current={path}
          tabs={[
            { key: "/users", to: "/users", label: t("users.tabs.users") },
            { key: "/users/accounts", to: "/users/accounts", label: t("users.tabs.accounts") },
            ...(roles ? [{ key: "/users/roles" as const, to: "/users/roles", label: t("users.tabs.roles") }] : []),
          ]}
        />
        <Current />
      </PageBody>
    </>
  );
}

export const messages = {
  en: { ...settingsMessages.en, ...subscriptionMessages.en, platformGrants: enGrants, users: enUsers },
  zh: { ...settingsMessages.zh, ...subscriptionMessages.zh, platformGrants: zhGrants, users: zhUsers },
};
