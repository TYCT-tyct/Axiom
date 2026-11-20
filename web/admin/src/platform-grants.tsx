import { type AdminSchemas, adminApi, unwrapEmptyChange } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Table, Td, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { platformGrantsQuery } from "./finance-queries";
import { Code, Head, State } from "./parts";

type Grant = AdminSchemas["PlatformGrant"];
type Change = { role: string; revoked: boolean };
type Owner = AdminSchemas["AccountDetail"]["owner"];

export function PlatformGrants({ owner }: { owner: Owner }) {
  const allowed = can(useSession(), "platform_grants.manage");
  return allowed ? <Grants owner={owner} /> : null;
}

function Grants({ owner }: { owner: Owner }) {
  const { t } = useTranslation();
  const query = useQuery(platformGrantsQuery(owner.id));
  const [selected, setSelected] = useState<Change>();
  return (
    <Card title={t("platformGrants.title")} lede={t("platformGrants.description")} layout="flush">
      <DataState
        query={query}
        isEmpty={(grant) => !grant.data?.length}
        empty={<EmptyState title={t("platformGrants.empty")} />}
      >
        {(grant) => <GrantsTable rows={grant.data ?? []} select={setSelected} />}
      </DataState>
      {selected ? <GrantDialog owner={owner} change={selected} onClose={() => setSelected(undefined)} /> : null}
    </Card>
  );
}

function GrantsTable({ rows, select }: { rows: Grant[]; select: (change: Change) => void }) {
  const { t } = useTranslation();
  return (
    <Table label={t("platformGrants.title")}>
      <Head cols={[[t("platformGrants.role"), { main: true }], t("platformGrants.status")]} action />
      <tbody>
        {rows.map(({ role, revoked }) => {
          if (!role) return null;
          const status = revoked ? t("platformGrants.revoked") : t("platformGrants.active");
          const action = revoked ? t("platformGrants.restore") : t("platformGrants.revoke");
          return (
            <tr key={role}>
              <Td main>
                <Code>{role}</Code>
              </Td>
              <Td>{revoked === undefined ? "—" : <State tone={revoked ? "neutral" : "good"}>{status}</State>}</Td>
              <Td actions>
                {revoked === undefined ? null : (
                  <ActionButton size="sm" onClick={() => select({ role, revoked: !revoked })}>
                    {action}
                  </ActionButton>
                )}
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

function GrantDialog({ owner, change, onClose }: { owner: Owner; change: Change; onClose: () => void }) {
  const { t } = useTranslation();
  const { role, revoked } = change;
  const label = t(revoked ? "platformGrants.revoke" : "platformGrants.restore");
  return (
    <ChangeDialog
      operation={`platform-grant:${owner.id}:${role}`}
      title={label}
      action={label}
      danger={revoked}
      intro={
        <Text>
          {t(revoked ? "platformGrants.revokeIntro" : "platformGrants.restoreIntro", { role, email: owner.email })}
        </Text>
      }
      read={() => revoked}
      send={(revoked, { key, reason }) =>
        unwrapEmptyChange(
          adminApi.PUT("/api/admin/v1/users/{user_id}/platform-grants/{role}", {
            params: { path: { user_id: owner.id, role }, header: { "Idempotency-Key": key } },
            body: { revoked, reason },
          }),
        )
      }
      done={() => <p>{t(revoked ? "platformGrants.revoked" : "platformGrants.active")}</p>}
      onClose={onClose}
    />
  );
}
