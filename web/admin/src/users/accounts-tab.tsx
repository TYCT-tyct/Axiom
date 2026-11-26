import { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { Columns, Format } from "../list-table";
import { AccountDrawer } from "./account-drawer";
import { accountColumn, createdColumn, personColumn, standingColumn, tierColumn } from "./user-columns";
import type { User } from "./user-list";
import { UserTable } from "./user-table";

function accountColumns(t: TFunction, format: Format): Columns<User> {
  return [
    personColumn(t, t("users.col.owner")),
    accountColumn(t),
    tierColumn(t),
    standingColumn(t),
    createdColumn(t, format),
  ];
}

// The accounts tab. No operation lists accounts, so an account is reached through its owner: the table is the users a
// search by email found, read as the accounts they own, and a row opens the account's drawer. A link to an account that
// is not among them (one from an order, or a bookmark) opens its drawer all the same.
export function AccountsTab() {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <UserTable
      label={t("users.tabs.accounts")}
      unit={t("users.unitAccount")}
      keyOf={(user) => user.account_id}
      columns={accountColumns(t, format)}
      lede={t("users.accountsLede")}
    >
      {({ selected, keys, select }) => (selected ? <AccountDrawer id={selected} keys={keys} select={select} /> : null)}
    </UserTable>
  );
}
