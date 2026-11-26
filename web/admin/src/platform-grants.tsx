import { type AdminSchemas, adminApi, unwrapEmptyChange } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Cluster, DataState, DrawerFacts, EmptyState, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { platformGrantsQuery } from "./finance-queries";
import { State } from "./parts";

type Change = { role: string; revoked: boolean };
type Owner = { id: AdminSchemas["UserId"]; email: string };

// What a person holds as platform grants, each role revoked or held, with the change that flips it. A session without
// platform_grants.manage may not read them, so it is not shown the part at all.
export function PlatformGrants({ owner }: { owner: Owner }) {
  const allowed = can(useSession(), "platform_grants.manage");
  return allowed ? <Grants owner={owner} /> : null;
}

function Grants({ owner }: { owner: Owner }) {
  const { t } = useTranslation();
  const query = useQuery(platformGrantsQuery(owner.id));
  const [selected, setSelected] = useState<Change>();
  return (
    <>
      <DataState
        query={query}
        isEmpty={(grant) => !grant.data?.length}
        empty={<EmptyState compact title={t("platformGrants.empty")} />}
      >
        {(grant) => (
          <DrawerFacts
            rows={(grant.data ?? []).flatMap(({ role, revoked }) =>
              role && revoked !== undefined
                ? [[role, <GrantState key={role} role={role} revoked={revoked} select={setSelected} />] as const]
                : [],
            )}
          />
        )}
      </DataState>
      {selected ? <GrantDialog owner={owner} change={selected} onClose={() => setSelected(undefined)} /> : null}
    </>
  );
}

function GrantState({ role, revoked, select }: Change & { select: (change: Change) => void }) {
  const { t } = useTranslation();
  return (
    <Cluster justify="end">
      <State tone={revoked ? "neutral" : "good"}>
        {t(revoked ? "platformGrants.revoked" : "platformGrants.active")}
      </State>
      <ActionButton
        size="sm"
        aria-label={`${t(revoked ? "platformGrants.restore" : "platformGrants.revoke")}: ${role}`}
        onClick={() => select({ role, revoked: !revoked })}
      >
        {t(revoked ? "platformGrants.restore" : "platformGrants.revoke")}
      </ActionButton>
    </Cluster>
  );
}

function GrantDialog({ owner, change, onClose }: { owner: Owner; change: Change; onClose: () => void }) {
  const { t } = useTranslation();
  const { role, revoked } = change;
  const label = t(revoked ? "platformGrants.revoke" : "platformGrants.restore");
  const state = (off: boolean) => t(off ? "platformGrants.revoked" : "platformGrants.active");
  // The grant is a state that is revoked or held, so its inverse is the other state of the same role.
  const put = (revoked: boolean, reason: string, key: string) =>
    unwrapEmptyChange(
      adminApi.PUT("/api/admin/v1/users/{user_id}/platform-grants/{role}", {
        params: { path: { user_id: owner.id, role }, header: { "Idempotency-Key": key } },
        body: { revoked, reason },
      }),
    );
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
      diff={[{ label: role, before: state(!revoked), after: state(revoked) }]}
      read={() => revoked}
      send={(revoked, { key, reason }) => put(revoked, reason, key)}
      toast={() => t("platformGrants.changed", { role, email: owner.email })}
      undo={(_, { reason, key }) => put(!revoked, reason, key)}
      done={() => null}
      onClose={onClose}
    />
  );
}
