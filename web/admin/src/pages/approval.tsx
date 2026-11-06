import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { Button, DataState, Icon, Id, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApprovalState, CapTable, EnvelopeTable } from "../approval-parts";
import { capOf, envelopeOf, open, outcomeOf } from "../approvals";
import { ChangeDialog } from "../change";
import { Actions, Code, Facts, Json, PageBody, Time } from "../parts";
import { approvalQuery } from "../queries";

type Approval = AdminSchemas["OperationsApproval"];
type Decision = "approve" | "reject";

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
          <PageHeader title={t(`approvals.risk.${approval.risk}`)} description={t("approvals.description")} />
          <PageBody>
            <Decide approval={approval} />
            <ApprovalFacts approval={approval} />
            <AgentReason text={approval.agent_reason} />
            <Limits summary={approval.summary} />
            <Panel title={t("approvals.summary")}>
              <div className="space-y-2 p-4">
                <p className="text-t2">{t("approvals.summaryHint")}</p>
                <Json value={approval.summary} />
              </div>
            </Panel>
            <Panel title={t("approvals.params")}>
              <div className="space-y-2 p-4">
                <p className="text-t2">{t("approvals.paramsHint")}</p>
                <Json value={approval.params} />
              </div>
            </Panel>
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function Decide({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const [decision, setDecision] = useState<Decision | null>(null);
  return (
    <Actions>
      <Button asChild>
        <Link to="/approvals">
          <Icon name="left" />
          {t("approvals.back")}
        </Link>
      </Button>
      {open(approval, Date.now()) ? (
        <>
          <Button variant="primary" size="md" onClick={() => setDecision("approve")}>
            {t("approvals.approve")}
          </Button>
          <Button size="md" onClick={() => setDecision("reject")}>
            {t("approvals.reject")}
          </Button>
        </>
      ) : null}
      {decision ? <DecisionDialog kind={decision} approval={approval} onClose={() => setDecision(null)} /> : null}
    </Actions>
  );
}

function ApprovalFacts({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const rows: [string, ReactNode][] = [
    [t("approvals.fact.state"), <ApprovalState key="state" approval={approval} />],
    [t("approvals.fact.operation"), t(`approvals.operationState.${approval.operation_state}`)],
    [t("approvals.fact.tool"), <Code key="tool">{approval.tool}</Code>],
    [
      t("approvals.fact.target"),
      <span key="target">
        <span className="block text-label text-t3">{approval.target.kind}</span>
        <Id value={approval.target.id} />
      </span>,
    ],
    [t("approvals.fact.requested"), <Time key="requested" at={approval.requested_at} />],
    [t("approvals.fact.expires"), <Time key="expires" at={approval.expires_at} />],
    [t("approvals.fact.credential"), <Id key="credential" value={approval.credential_id} />],
  ];
  if (approval.run_id) {
    rows.push([t("approvals.fact.run"), <Code key="run">{approval.run_id}</Code>]);
  }
  if (approval.decided_at) {
    rows.push(
      [t("approvals.fact.decided"), <Time key="decided" at={approval.decided_at} />],
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
    <Panel title={t("approvals.agentReason")}>
      <div className="space-y-2 p-4">
        <p className="text-label text-t3">{t("approvals.agentReasonHint")}</p>
        <blockquote className="whitespace-pre-wrap border-b1 border-l-2 pl-3 text-t2 [overflow-wrap:anywhere]">
          {text}
        </blockquote>
      </div>
    </Panel>
  );
}

function Limits({ summary }: { summary: unknown }) {
  const cap = capOf(summary);
  const envelope = envelopeOf(summary);
  return (
    <>
      {cap ? <CapTable cap={cap} /> : null}
      {envelope ? <EnvelopeTable envelope={envelope} /> : null}
    </>
  );
}

// DecisionDialog approves or rejects one approval. The answer, not the dialog's snapshot, says what happened: an approval
// can be recorded while its operation does not run.
function DecisionDialog({ kind, approval, onClose }: { kind: Decision; approval: Approval; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const approve = kind === "approve";
  const operation = t(`approvals.risk.${approval.risk}`);
  const amount = capOf(approval.summary)?.amount;
  return (
    <ChangeDialog
      title={t(approve ? "approvals.approveTitle" : "approvals.rejectTitle", { operation })}
      operation={`${kind}-operations-approval:${approval.id}`}
      intro={
        <>
          <p>{t(approve ? "approvals.approveIntro" : "approvals.rejectIntro")}</p>
          {approve && amount ? <p>{t("approvals.approveMoney", { amount: format.money(amount) })}</p> : null}
        </>
      }
      action={t(approve ? "approvals.approveAction" : "approvals.rejectAction")}
      danger={approve}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST(
            approve
              ? "/api/admin/v1/operations/approvals/{approval_id}/approve"
              : "/api/admin/v1/operations/approvals/{approval_id}/reject",
            { params: { path: { approval_id: approval.id }, header: { "Idempotency-Key": key } }, body: { reason } },
          ),
        )
      }
      explain={(error) =>
        error instanceof ApiError && error.code === "conflict" ? t("approvals.conflict") : undefined
      }
      done={(answer) => <p>{outcomeText(t, answer)}</p>}
      onClose={onClose}
    />
  );
}

function outcomeText(t: TFunction, answer: Approval): string {
  switch (outcomeOf(answer)) {
    case "done":
      return t(`approvals.done.${answer.risk}`);
    case "refused":
      return t("approvals.refused", { code: answer.error_code ?? "—" });
    case "canceled":
      return t("approvals.canceled");
    case "rejected":
      return t("approvals.rejected");
    default:
      return t("approvals.other", { state: t(`approvals.operationState.${answer.operation_state}`) });
  }
}
