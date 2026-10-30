import type { AdminSchemas } from "@axiom/api";
import { Button, DataState, Icon, PageHeader, Panel, Table, Td, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Actions, Code, Facts, Head, Json, mainCell, PageBody, State, Time } from "../parts";
import { adminCallQuery } from "../queries";

type Trace = AdminSchemas["AdminCallTrace"];
type Attempt = AdminSchemas["AdminAttempt"];

// The operator's trace: the support summary plus what the support page must not show, the cost and the route the call
// took, attempt by attempt. It needs its own permission, so a link from the support list is shown only to a session that
// may open it.
export default function Call() {
  const { t } = useTranslation();
  const { callId } = useParams({ from: "/app/calls/$callId" });
  const call = useQuery(adminCallQuery(callId));
  return (
    <DataState query={call} page>
      {(data) => (
        <>
          <PageHeader
            title={t("calls.traceTitle", { id: data.summary?.id ?? callId })}
            description={t("calls.traceDescription")}
          />
          <PageBody>
            <Actions>
              <Button asChild>
                <Link to="/calls">
                  <Icon name="left" />
                  {t("calls.back")}
                </Link>
              </Button>
            </Actions>
            <Summary summary={data.summary} />
            <div className="grid gap-4 xl:grid-cols-2">
              <Cost cost={data.cost} />
              <Route route={data.route} />
            </div>
            <Attempts attempts={data.attempts} />
          </PageBody>
        </>
      )}
    </DataState>
  );
}

const missing = "—";

function Summary({ summary }: { summary: Trace["summary"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  if (!summary) {
    return <Facts title={t("calls.summaryTitle")} rows={[[t("calls.unavailable"), missing]]} />;
  }
  return (
    <Facts
      title={t("calls.summaryTitle")}
      rows={[
        [t("calls.col.at"), <Time key="at" at={summary.at} />],
        [t("calls.col.account"), <Code key="account">{summary.account_id ?? missing}</Code>],
        [t("calls.site"), <Code key="site">{summary.site_id ?? missing}</Code>],
        [t("calls.col.model"), summary.model ?? missing],
        [
          t("calls.col.result"),
          <State key="result" tone={summary.error_kind ? "bad" : "good"}>
            {summary.error_kind ? t("calls.failed") : t("calls.ok")}
          </State>,
        ],
        [
          t("calls.col.latency"),
          summary.latency_ms === undefined ? missing : t("calls.ms", { ms: format.number(summary.latency_ms) }),
        ],
        [t("calls.col.price"), summary.price ? format.money(summary.price) : missing],
        [t("calls.requestId"), <Code key="request">{summary.id ?? missing}</Code>],
      ]}
    />
  );
}

function Cost({ cost }: { cost: Trace["cost"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Facts
      title={t("calls.costTitle")}
      rows={[[t("calls.cost"), cost ? format.money(cost) : t("calls.unavailable")]]}
    />
  );
}

function Route({ route }: { route: Trace["route"] }) {
  const { t } = useTranslation();
  return (
    <Panel title={t("calls.routeTitle")}>
      <div className="p-4">
        {route === undefined ? <p className="text-t3">{t("calls.unavailable")}</p> : <Json value={route} />}
      </div>
    </Panel>
  );
}

function Attempts({ attempts }: { attempts?: Attempt[] }) {
  const { t } = useTranslation();
  if (attempts === undefined) {
    return (
      <Panel title={t("calls.attemptsTitle")}>
        <p className="p-4 text-t3">{t("calls.unavailable")}</p>
      </Panel>
    );
  }
  if (attempts.length === 0) {
    return (
      <Panel title={t("calls.attemptsTitle")}>
        <p className="p-4 text-t3">{t("calls.attemptsNone")}</p>
      </Panel>
    );
  }
  return (
    <Panel title={t("calls.attemptsTitle")}>
      <Table label={t("calls.attemptsTitle")}>
        <Head
          cols={[
            [t("calls.attempt.seq"), "w-0"],
            [t("calls.attempt.endpoint")],
            [t("calls.attempt.supplier"), "hidden md:table-cell"],
            [t("calls.attempt.source"), "hidden lg:table-cell"],
            [t("calls.attempt.outcome")],
            [t("calls.attempt.started"), "hidden xl:table-cell"],
            [t("calls.attempt.firstToken"), "hidden lg:table-cell"],
          ]}
        />
        <tbody>
          {attempts.map((attempt, index) => (
            <AttemptRow key={attempt.seq ?? index} attempt={attempt} index={index} />
          ))}
        </tbody>
      </Table>
    </Panel>
  );
}

// An outcome is the server's word, not an enum: a code a newer node adds is shown as it arrived.
function Outcome({ outcome }: { outcome?: string }) {
  const { t } = useTranslation();
  const value = outcome ?? "unknown";
  const outcomes: Record<string, string> = {
    committed: t("calls.outcome.committed"),
    failed: t("calls.outcome.failed"),
    unknown: t("calls.outcome.unknown"),
  };
  return (
    <State tone={value === "committed" ? "good" : value === "unknown" ? "warn" : "bad"}>
      {outcomes[value] ?? value}
    </State>
  );
}

function AttemptRow({ attempt, index }: { attempt: Attempt; index: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <tr>
      <Td className="tabular-nums">{attempt.seq ?? index + 1}</Td>
      <Td className={mainCell}>
        <span className="block truncate" title={attempt.endpoint_id}>
          <Code>{attempt.endpoint_id ?? missing}</Code>
        </span>
        {attempt.error_kind ? <span className="block text-label text-bad">{attempt.error_kind}</span> : null}
      </Td>
      <Td className="hidden md:table-cell">
        <Code>{attempt.supplier_id ?? missing}</Code>
      </Td>
      <Td className="hidden lg:table-cell">
        {attempt.source_class ?? missing}
        {attempt.egress ? <span className="block text-label text-t3">{attempt.egress}</span> : null}
      </Td>
      <Td>
        <Outcome outcome={attempt.outcome} />
      </Td>
      <Td className="hidden xl:table-cell">
        <Time at={attempt.started_at} />
        {attempt.finished_at ? (
          <span className="block text-label text-t3">
            <Time at={attempt.finished_at} />
          </span>
        ) : null}
      </Td>
      <Td className="hidden tabular-nums lg:table-cell">
        {attempt.first_token_ms === undefined ? missing : t("calls.ms", { ms: format.number(attempt.first_token_ms) })}
      </Td>
    </tr>
  );
}
