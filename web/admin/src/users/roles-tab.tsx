import { can, useSession } from "@axiom/app";
import { DrawerSection, ForbiddenState, Text, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { Columns, Format } from "../list-table";
import { PlatformGrants } from "../platform-grants";
import { RowDrawer } from "../row-drawer";
import { RoleMatrix } from "./role-matrix";
import { createdColumn, personColumn, standingColumn } from "./user-columns";
import type { User } from "./user-list";
import { UserTable } from "./user-table";

function roleColumns(t: TFunction, format: Format): Columns<User> {
  return [personColumn(t, t("users.col.user")), standingColumn(t), createdColumn(t, format)];
}

// The platform roles tab: who may enter the admin and what they may do. No operation lists who holds a role, so a person
// is found by email and the roles they hold are in their drawer, where each can be revoked or restored; a new grant is
// made by the operator on the command line (docs/domains/accounts.md). Below the people, what each role may do.
export function RolesTab() {
  const { t } = useTranslation();
  const format = useFormat();
  if (!can(useSession(), "platform_grants.manage")) {
    return <ForbiddenState />;
  }
  return (
    <UserTable
      label={t("users.tabs.roles")}
      unit={t("users.unit")}
      keyOf={(user) => user.id}
      columns={roleColumns(t, format)}
      lede={t("users.roles.lede")}
      after={<RoleMatrix />}
    >
      {({ open, keys, select }) =>
        open ? (
          <RowDrawer
            title={open.email}
            subtitle={t("users.roles.drawerSubtitle")}
            keys={keys}
            selected={open.id}
            select={select}
          >
            <DrawerSection title={t("users.tabs.roles")}>
              <PlatformGrants owner={{ id: open.id, email: open.email }} />
            </DrawerSection>
            <Text size="sm" tone="muted">
              {t("users.roles.grantNote")}
            </Text>
          </RowDrawer>
        ) : null
      }
    </UserTable>
  );
}
