import type { AdminSchemas } from "@axiom/api";
import { ActionButton, Card, DataState, Grid, Icon, PageHead, Table, Td, Text, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Actions, Code, Facts, Head, Json, PageBody, State, Time } from "../parts";
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
          <PageHead
            title={t("calls.traceTitle", { id: data.summary?.id ?? callId })}
            lede={t("calls.traceDescription")}
          />
          <PageBody>
            <Actions>
              <ActionButton asChild>
                <Link to="/calls">
                  <Icon name="left" />
                  {t("calls.back")}
                </Link>
              </ActionButton>
            </Actions>
            <Summary summary={data.summary} />
            <Grid columns={2}>
              <Cost cost={data.cost} />
              <Route route={data.route} />
            </Grid>
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
    <Card title={t("calls.routeTitle")}>
      {route === undefined ? (
        <Text tone="faint">{t("calls.unavailable")}</Text>
      ) : (
        <Json value={route} label={t("calls.routeTitle")} />
      )}
    </Card>
  );
}

function Attempts({ attempts }: { attempts?: Attempt[] }) {
  const { t } = useTranslation();
  if (attempts === undefined) {
    return (
      <Card title={t("calls.attemptsTitle")}>
        <Text tone="faint">{t("calls.unavailable")}</Text>
      </Card>
    );
  }
  if (attempts.length === 0) {
    return (
      <Card title={t("calls.attemptsTitle")}>
        <Text tone="faint">{t("calls.attemptsNone")}</Text>
      </Card>
    );
  }
  return (
    <Card title={t("calls.attemptsTitle")} layout="flush">
      <Table label={t("calls.attemptsTitle")}>
        <Head
          cols={[
            t("calls.attempt.seq"),
            [t("calls.attempt.endpoint"), { main: true }],
            t("calls.attempt.supplier"),
            t("calls.attempt.source"),
            t("calls.attempt.outcome"),
            t("calls.attempt.started"),
            t("calls.attempt.firstToken"),
          ]}
        />
        <tbody>
          {attempts.map((attempt, index) => (
            <AttemptRow key={attempt.seq ?? index} attempt={attempt} index={index} />
          ))}
        </tbody>
      </Table>
    </Card>
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
      <Td>{attempt.seq ?? index + 1}</Td>
      <Td main>
        <Code>{attempt.endpoint_id ?? missing}</Code>
        {attempt.error_kind ? (
          <Text as="div" size="sm" tone="bad">
            {attempt.error_kind}
          </Text>
        ) : null}
      </Td>
      <Td>
        <Code>{attempt.supplier_id ?? missing}</Code>
      </Td>
      <Td>
        {attempt.source_class ?? missing}
        {attempt.egress ? (
          <Text as="div" size="sm" tone="faint">
            {attempt.egress}
          </Text>
        ) : null}
      </Td>
      <Td>
        <Outcome outcome={attempt.outcome} />
      </Td>
      <Td>
        <Time at={attempt.started_at} />
        {attempt.finished_at ? (
          <Text as="div" size="sm" tone="faint">
            <Time at={attempt.finished_at} />
          </Text>
        ) : null}
      </Td>
      <Td>
        {attempt.first_token_ms === undefined ? missing : t("calls.ms", { ms: format.number(attempt.first_token_ms) })}
      </Td>
    </tr>
  );
}
