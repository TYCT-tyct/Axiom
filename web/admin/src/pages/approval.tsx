import type { AdminSchemas } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { CodeBlock } from "@axiom/boards/ds";
import { ActionButton, Card, DataState, Icon, Id, PageHead, Stack, Text } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { type Decision, DecisionDialog } from "../approval-decision";
import { ApprovalState, CapTable, EnvelopeTable, Minute, Target, useNow } from "../approval-parts";
import { PriceTable, RawData, RequestFacts, RuleChangePanel, WhyAsked } from "../approval-request";
import { capOf, capOver, decidable, envelopeOf, envelopeOver } from "../approvals";
import { Actions, Code, Facts, PageBody } from "../parts";
import { approvalQuery } from "../queries";

type Approval = AdminSchemas["OperationsApproval"];

// One approval: what the agent asked for, everything the server read of its target and of the caps when it asked, and
// the agent's own words apart, as untrusted text. The owner approves or rejects it here; the notification of a pending
// approval points at this page.
export default function ApprovalPage() {
  const { t } = useTranslation();
  const { approvalId } = useParams({ from: "/app/approvals/$approvalId" });
  const query = useQuery(approvalQuery(approvalId));
  return (
    <DataState query={query} page>
      {(approval) => (
        <>
          <PageBody>
            <PageHead title={t(`approvals.risk.${approval.risk}`)} lede={t("approvals.description")} />
            <Decide approval={approval} />
            <WhyAsked approval={approval} />
            <ApprovalFacts approval={approval} />
            <RequestFacts approval={approval} />
            <PriceTable approval={approval} />
            <RuleChangePanel approval={approval} />
            <Limits approval={approval} />
            <AgentReason text={approval.agent_reason} />
            <RawData approval={approval} />
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function Decide({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const [decision, setDecision] = useState<Decision | null>(null);
  const queryClient = useQueryClient();
  // A decision the server refused (a conflict: it lapsed or was decided elsewhere) leaves the page showing what it read;
  // closing the dialog reads it again, so the page says what the server says now.
  const close = () => {
    setDecision(null);
    void queryClient.invalidateQueries({ queryKey: approvalQuery(approval.id).queryKey });
    void queryClient.invalidateQueries({ queryKey: [accountKey, "operations-approvals"] });
  };
  return (
    <Actions>
      <ActionButton asChild>
        <Link to="/approvals">
          <Icon name="left" />
          {t("approvals.back")}
        </Link>
      </ActionButton>
      {decidable(approval) ? (
        <>
          <ActionButton variant="pri" onClick={() => setDecision("approve")}>
            {t("approvals.approve")}
          </ActionButton>
          <ActionButton onClick={() => setDecision("reject")}>{t("approvals.reject")}</ActionButton>
        </>
      ) : null}
      {decision ? <DecisionDialog kind={decision} approval={approval} onClose={close} /> : null}
    </Actions>
  );
}

function ApprovalFacts({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const now = useNow();
  const rows: [string, ReactNode][] = [
    [t("approvals.fact.target"), <Target key="target" approval={approval} />],
    [t("approvals.fact.state"), <ApprovalState key="state" approval={approval} now={now} />],
    [t("approvals.fact.operation"), t(`approvals.operationState.${approval.operation_state}`)],
    [t("approvals.fact.requested"), <Minute key="requested" at={approval.requested_at} />],
    [t("approvals.fact.expires"), <Minute key="expires" at={approval.expires_at} />],
    [t("approvals.fact.tool"), <Code key="tool">{approval.tool}</Code>],
    [t("approvals.fact.credential"), <Id key="credential" value={approval.credential_id} />],
  ];
  if (approval.run_id) {
    rows.push([t("approvals.fact.run"), <Code key="run">{approval.run_id}</Code>]);
  }
  if (approval.decided_at) {
    rows.push(
      [t("approvals.fact.decided"), <Minute key="decided" at={approval.decided_at} />],
      [t("approvals.fact.decidedBy"), <Code key="by">{approval.decided_by ?? "—"}</Code>],
      [t("approvals.fact.decisionReason"), approval.decision_reason ?? "—"],
    );
  }
  if (approval.error_code) {
    rows.push([t("approvals.fact.errorCode"), <Code key="error">{approval.error_code}</Code>]);
  }
  return <Facts title={t("approvals.facts")} rows={rows} />;
}

// The agent's reason is shown as a quotation, apart from what the server read: it is the agent's to write, and a
// prompt-injected agent could write anything in it.
function AgentReason({ text }: { text: string }) {
  const { t } = useTranslation();
  return (
    <Card title={t("approvals.agentReason")}>
      <Stack gap={8}>
        <Text size="sm" tone="faint">
          {t("approvals.agentReasonHint")}
        </Text>
        <CodeBlock label={t("approvals.agentReason")}>{text}</CodeBlock>
      </Stack>
    </Card>
  );
}

function Limits({ approval }: { approval: Approval }) {
  const cap = capOf(approval.summary);
  const envelope = envelopeOf(approval.summary);
  return (
    <>
      {cap ? <CapTable cap={cap} over={capOver(approval)} /> : null}
      {envelope ? <EnvelopeTable envelope={envelope} over={envelopeOver(approval)} /> : null}
    </>
  );
}
