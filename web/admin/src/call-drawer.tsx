import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import {
  CopyButton,
  DataState,
  Disclosure,
  DrawerSection,
  FactList,
  Id,
  Mono,
  type PanelSteps,
  SidePanel,
  Tag,
  Text,
  Timeline,
  type TimelineStep,
  useFormat,
} from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Json, State } from "./parts";
import { adminCallQuery } from "./queries";

type Summary = AdminSchemas["AdminCallSummary"];
type Attempt = AdminSchemas["AdminAttempt"];

const missing = "—";

// A call's detail, opened from a list of calls (adP_call in admin_pages_c.js). Whoever may search calls reads the
// summary; the lines the request went through and what it cost upstream are the operator's trace, behind the inspect
// permission, so a session without it never asks for them.
export function CallDrawer({ call, steps, onClose }: { call: Summary; steps?: PanelSteps; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const inspect = can(useSession(), "calls.inspect");
  const id = call.id ?? "";
  return (
    <SidePanel
      drawer
      wide={inspect}
      title={id}
      subtitle={call.at ? format.dateTime(new Date(call.at)) : undefined}
      steps={steps}
      onClose={onClose}
      footer={<CopyButton text={id} label={t("calls.copyId")} />}
    >
      {inspect ? <Trace call={call} /> : <Summary call={call} note />}
    </SidePanel>
  );
}

function Trace({ call }: { call: Summary }) {
  const trace = useQuery(adminCallQuery(call.id ?? ""));
  return <DataState query={trace}>{(data) => <Traced call={call} data={data} />}</DataState>;
}

function Traced({ call, data }: { call: Summary; data: AdminSchemas["AdminCallTrace"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const steps = useRouteSteps(call, data.attempts ?? []);
  return (
    <>
      <DrawerSection title={t("calls.drawer.route")}>
        <Timeline label={t("calls.drawer.route")} steps={steps} />
      </DrawerSection>
      <DrawerSection title={t("calls.drawer.fee")}>
        <FactList
          rows={[
            [t("calls.drawer.charged"), call.price ? format.money(call.price) : missing],
            [t("calls.drawer.paid"), data.cost ? format.money(data.cost) : missing],
          ]}
        />
      </DrawerSection>
      <Summary call={call} />
      {data.route === undefined ? null : (
        <DrawerSection title={t("calls.routeTitle")}>
          <Disclosure summary={t("calls.drawer.routeJson")}>
            <Json value={data.route} label={t("calls.routeTitle")} />
          </Disclosure>
        </DrawerSection>
      )}
    </>
  );
}

// What the support view reads, and the operator's view keeps under the money: the request itself.
function Summary({ call, note = false }: { call: Summary; note?: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  const failed = Boolean(call.error_kind);
  return (
    <DrawerSection title={t("calls.drawer.request")}>
      <FactList
        rows={[
          [
            t("calls.col.result"),
            <State key="result" tone={failed ? "bad" : "good"}>
              {t(failed ? "calls.failed" : "calls.ok")}
            </State>,
          ],
          ...(call.error_kind ? [[t("calls.drawer.error"), <Mono key="error">{call.error_kind}</Mono>] as const] : []),
          [t("calls.col.account"), call.account_id ? <Id value={call.account_id} /> : missing],
          [t("calls.site"), call.site_id ? <Id value={call.site_id} /> : missing],
          [t("calls.col.model"), call.model ?? missing],
          [
            t("calls.col.latency"),
            call.latency_ms === undefined ? missing : t("calls.ms", { ms: format.number(call.latency_ms) }),
          ],
        ]}
      />
      <PluginRuns call={call} />
      {note ? (
        <Text size="sm" tone="faint">
          {t("calls.drawer.supportNote")}
        </Text>
      ) : null}
    </DrawerSection>
  );
}

// The plugins the call used, one line each: which, how it went, what handled it and for how long.
function PluginRuns({ call }: { call: Summary }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!call.plugins?.length) return null;
  return (
    <FactList
      rows={call.plugins.map((run) => [
        t(`calls.plugin.${run.plugin}`),
        <span key={run.plugin}>
          <Tag tone={run.ok ? "ok" : "bad"}>{t(run.ok ? "calls.ok" : "calls.failed")}</Tag>{" "}
          {[
            run.engine,
            run.ms === undefined ? undefined : t("calls.ms", { ms: format.number(run.ms) }),
            run.price ? format.money(run.price) : undefined,
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>,
      ])}
    />
  );
}

// The lines the request went through, in order: the one it went to first, each one it switched to after a failure, how
// each answered. A call the gateway turned away before any line has no attempt, so its error is the one step.
function useRouteSteps(call: Summary, attempts: readonly Attempt[]): TimelineStep[] {
  const { t } = useTranslation();
  const format = useFormat();
  if (attempts.length === 0) {
    return [
      {
        key: "none",
        title: t(call.error_kind ? "calls.step.rejected" : "calls.step.none"),
        detail: call.error_kind,
        state: call.error_kind ? "failed" : "done",
      },
    ];
  }
  return attempts.map((attempt, index) => {
    const outcome = attempt.outcome ?? "unknown";
    const detail = [
      t(`calls.outcome.${outcome === "committed" || outcome === "failed" ? outcome : "unknown"}`),
      attempt.error_kind,
      attempt.first_token_ms === undefined
        ? undefined
        : t("calls.step.firstToken", { ms: format.number(attempt.first_token_ms) }),
      attempt.source_class,
    ].filter(Boolean);
    return {
      key: attempt.seq ?? index,
      title: (
        <>
          {t(index === 0 ? "calls.step.first" : "calls.step.switch")} <Mono>{attempt.endpoint_id ?? missing}</Mono>
        </>
      ),
      detail: detail.join(" · "),
      state: outcome === "committed" ? "done" : outcome === "failed" ? "failed" : "current",
    };
  });
}
