import { can, useSession } from "@axiom/app";
import { ActionButton, DrawerFacts, DrawerSection, TextLink, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { SignInAction } from "../account-actions";
import { GrantButton } from "../credit-dialog";
import type { Columns, Format } from "../list-table";
import { shortId } from "../orders/states";
import { State } from "../parts";
import { PlatformGrants } from "../platform-grants";
import { accountQuery } from "../queries";
import { RowDrawer } from "../row-drawer";
import { accountColumn, createdColumn, personColumn, tierColumn } from "./user-columns";
import type { User } from "./user-list";
import { UserTable } from "./user-table";

function userColumns(t: TFunction, format: Format): Columns<User> {
  return [
    personColumn(t, t("users.col.user"), true),
    accountColumn(t),
    tierColumn(t),
    {
      key: "mfa",
      header: t("users.col.mfa"),
      cell: (user) => (
        <State tone={user.mfa_enabled ? "good" : "neutral"}>
          {user.mfa_enabled ? t("users.mfaOn") : t("users.mfaOff")}
        </State>
      ),
    },
    createdColumn(t, format),
  ];
}

// The users tab: who has signed up, found by the start of their email, and a drawer for one of them: which account is
// theirs, whether they may sign in, the platform roles they hold, and the two things a support person does for them,
// stop their sign-in and give credit.
export function UsersTab() {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <UserTable
      label={t("users.tabs.users")}
      unit={t("users.unit")}
      keyOf={(user) => user.id}
      columns={userColumns(t, format)}
    >
      {({ open, keys, select }) =>
        open ? (
          <RowDrawer
            title={open.email}
            subtitle={t("users.drawer.subtitle", { tier: open.tier.name })}
            keys={keys}
            selected={open.id}
            select={select}
            footer={<UserActions user={open} />}
          >
            <UserBody user={open} />
          </RowDrawer>
        ) : null
      }
    </UserTable>
  );
}

function UserBody({ user }: { user: User }) {
  const { t } = useTranslation();
  const format = useFormat();
  const session = useSession();
  return (
    <>
      <DrawerFacts
        rows={[
          [
            t("users.col.account"),
            <TextLink key="account">
              <Link to="/users/accounts" search={{ selected: user.account_id }}>
                {t("users.drawer.openAccount")} {shortId(user.account_id)}
              </Link>
            </TextLink>,
          ],
          [
            t("users.col.status"),
            <State key="status" tone={user.status === "active" ? "good" : "warn"}>
              {t(user.status === "active" ? "users.active" : "users.disabled")}
            </State>,
          ],
          [
            t("users.account.signIn"),
            user.sign_in_disabled === undefined ? (
              t("users.account.unavailable")
            ) : (
              <State key="sign-in" tone={user.sign_in_disabled ? "warn" : "good"}>
                {t(user.sign_in_disabled ? "users.account.signInDisabled" : "users.account.signInActive")}
              </State>
            ),
          ],
          [t("users.col.tier"), user.tier.name],
          [
            t("users.col.mfa"),
            <State key="mfa" tone={user.mfa_enabled ? "good" : "neutral"}>
              {user.mfa_enabled ? t("users.mfaOn") : t("users.mfaOff")}
            </State>,
          ],
          [t("users.col.created"), format.dateTime(new Date(user.created_at))],
        ]}
      />
      {can(session, "platform_grants.manage") ? (
        <DrawerSection title={t("users.tabs.roles")}>
          <PlatformGrants owner={{ id: user.id, email: user.email }} />
        </DrawerSection>
      ) : null}
      {can(session, "calls.search") ? (
        <DrawerSection title={t("users.drawer.calls")}>
          <TextLink>
            <Link to="/calls" search={{ account_id: user.account_id }}>
              {t("users.drawer.openCalls")}
            </Link>
          </TextLink>
        </DrawerSection>
      ) : null}
    </>
  );
}

// What a person with users.manage does to the user (stop or allow their sign-in) and with credits.grant to the account
// (give credit, which needs the account read: its balance and currency).
function UserActions({ user }: { user: User }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "users.manage");
  const [signIn, setSignIn] = useState(false);
  const account = useQuery(accountQuery(user.account_id));
  return (
    <>
      {manage && user.sign_in_disabled !== undefined ? (
        <ActionButton variant="gho" onClick={() => setSignIn(true)}>
          {t(user.sign_in_disabled ? "users.account.enableSignIn" : "users.account.disableSignIn")}
        </ActionButton>
      ) : null}
      {account.data ? <GrantButton account={account.data} /> : null}
      {signIn && user.sign_in_disabled !== undefined ? (
        <SignInAction
          owner={{ id: user.id, email: user.email }}
          accountId={user.account_id}
          disabled={user.sign_in_disabled}
          onClose={() => setSignIn(false)}
        />
      ) : null}
    </>
  );
}
