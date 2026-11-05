import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Button } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "./change";
import { Facts, State, Time } from "./parts";
import { routingSuspensionQuery } from "./queries";
import { NodeState } from "./routing-limits";

type Suspension = AdminSchemas["RoutingPresetSuspension"];

// RoutingSuspension is the platform's kill switch on routing presets (docs/domains/routing.md「滚动发布、失败与性能」):
// whether they are suspended, and the change to the other state.
export function RoutingSuspension() {
  return <SuspensionState query={useQuery(routingSuspensionQuery)} />;
}

// SuspensionState shows the card, or for a node of a release without the switch says it is not there yet.
export function SuspensionState({ query }: { query: UseQueryResult<Suspension> }) {
  const { t } = useTranslation();
  return (
    <NodeState
      query={query}
      title={t("routingPresets.suspension.title")}
      unavailable={t("routingPresets.suspension.unavailable")}
    >
      {(suspension) => <SuspensionCard suspension={suspension} />}
    </NodeState>
  );
}

// SuspensionCard is the switch's state, which a refetch keeps current, and the change to the other state. The dialog
// takes the state it changes to when it opens: another administrator's change seen meanwhile must not turn a
// suspension the person is writing a reason for into a resumption, or the other way round.
export function SuspensionCard({ suspension }: { suspension: Suspension }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "sites.manage");
  const [target, setTarget] = useState<boolean>();
  const { suspended } = suspension;
  return (
    <>
      <Facts
        title={t("routingPresets.suspension.title")}
        action={
          manage ? (
            <Button size="md" onClick={() => setTarget(!suspended)}>
              {t(suspended ? "routingPresets.suspension.resume" : "routingPresets.suspension.suspend")}
            </Button>
          ) : undefined
        }
        rows={[
          [
            t("routingPresets.suspension.state"),
            suspended ? (
              <span className="space-y-1">
                <State tone="bad">{t("routingPresets.suspension.suspended")}</State>
                <span className="block text-t2">
                  {t("routingPresets.suspension.since")} <Time at={suspension.since} />
                </span>
              </span>
            ) : (
              <State tone="good">{t("routingPresets.suspension.running")}</State>
            ),
          ],
        ]}
      />
      {target === undefined ? null : <SuspensionDialog suspend={target} onClose={() => setTarget(undefined)} />}
    </>
  );
}

// SuspensionDialog suspends presets, or resumes them when suspend is false, with the person's reason.
function SuspensionDialog({ suspend, onClose }: { suspend: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const action = suspend ? "suspend" : "resume";
  return (
    <ChangeDialog<{ suspended: boolean }, Suspension>
      operation="routing-suspension"
      title={t(`routingPresets.suspension.${action}Title`)}
      intro={<p>{t(`routingPresets.suspension.${action}Intro`)}</p>}
      action={t(`routingPresets.suspension.${action}`)}
      danger={suspend}
      read={() => ({ suspended: suspend })}
      send={(body, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/routing-suspension", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      done={(now) => (
        <p>{t(now.suspended ? "routingPresets.suspension.suspendedDone" : "routingPresets.suspension.resumedDone")}</p>
      )}
      onClose={onClose}
    />
  );
}
