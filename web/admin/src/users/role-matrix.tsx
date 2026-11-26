import { Card, Table, Td, Th, Tick } from "@axiom/ui";
import { useTranslation } from "react-i18next";

// What each built-in platform role may do, as the baseline migration of the accounts module gives them
// (internal/accounts/migrations/00049_baseline.sql, the rows of accounts.roles): the permission names each role holds.
// No operation reads the roles, and a platform can edit a role or add one, so this is the roles as they are made, and the
// page says so. A line of the table is a capability and the permissions that grant it; a role has the capability when it
// holds all of them. The names match the contract's x-axiom-permission.
const roles = {
  admin: [
    "users.view",
    "users.manage",
    "credits.grant",
    "calls.search",
    "audit.view",
    "suppliers.view",
    "suppliers.manage",
    "catalog.view",
    "catalog.manage",
    "pricing.view",
    "pricing.manage",
    "operations.approve",
    "operations.manage",
    "sites.manage",
    "platform_grants.manage",
  ],
  finance: ["users.view"],
  support: ["users.view", "calls.search"],
  viewer: ["users.view", "suppliers.view", "catalog.view", "pricing.view"],
} as const;

type Role = keyof typeof roles;
const columns = Object.keys(roles) as Role[];

const capabilities = [
  { key: "users", needs: ["users.view"] },
  { key: "calls", needs: ["calls.search"] },
  { key: "audit", needs: ["audit.view"] },
  { key: "supply", needs: ["suppliers.view", "catalog.view", "pricing.view"] },
  { key: "manageUsers", needs: ["users.manage", "credits.grant"] },
  { key: "manageSupply", needs: ["suppliers.manage", "catalog.manage", "pricing.manage"] },
  { key: "approve", needs: ["operations.approve", "operations.manage"] },
  { key: "settings", needs: ["sites.manage", "platform_grants.manage"] },
] as const;

export const holds = (role: Role, needs: readonly string[]) =>
  needs.every((need) => (roles[role] as readonly string[]).includes(need));

// The bottom half of the platform roles tab, 「每个角色能做什么」.
export function RoleMatrix() {
  const { t } = useTranslation();
  return (
    <Card title={t("users.roles.matrixTitle")} lede={t("users.roles.matrixLede")} layout="flush">
      <Table label={t("users.roles.matrixTitle")}>
        <thead>
          <tr>
            <Th />
            {columns.map((role) => (
              <Th key={role}>{t(`users.roles.names.${role}`)}</Th>
            ))}
          </tr>
        </thead>
        <tbody>
          {capabilities.map((capability) => (
            <tr key={capability.key}>
              <Td>{t(`users.roles.can.${capability.key}`)}</Td>
              {columns.map((role) => (
                <Td key={role}>{holds(role, capability.needs) ? <Tick label={t("users.roles.yes")} /> : null}</Td>
              ))}
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}
