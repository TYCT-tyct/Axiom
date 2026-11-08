import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { Button, DataState, Icon, Id, PageHeader, Panel, useFormat } from "@axiom/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApprovalState, CapTable, EnvelopeTable, Minute, Target, useNow } from "../approval-parts";
import { PriceTable, RawData, RequestFacts, WhyAsked } from "../approval-request";
import { capOf, capOver, decidable, envelopeOf, envelopeOver, outcomeOf } from "../approvals";
import { ChangeDialog } from "../change";
import { Actions, Code, Facts, PageBody, State } from "../parts";
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
            <WhyAsked approval={approval} />
            <ApprovalFacts approval={approval} />
            <RequestFacts approval={approval} />
            <PriceTable approval={approval} />
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
      <Button asChild>
        <Link to="/approvals">
          <Icon name="left" />
          {t("approvals.back")}
        </Link>
      </Button>
      {decidable(approval) ? (
        <>
          <Button variant="primary" size="md" onClick={() => setDecision("approve")}>
            {t("approvals.approve")}
          </Button>
          <Button size="md" onClick={() => setDecision("reject")}>
            {t("approvals.reject")}
          </Button>
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

// DecisionDialog approves or rejects one approval. The answer, not the dialog's snapshot, says what happened: an approval
// can be recorded while its operation does not run.
function DecisionDialog({ kind, approval, onClose }: { kind: Decision; approval: Approval; onClose: () => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const approve = kind === "approve";
  const operation = t(`approvals.risk.${approval.risk}`);
  const amount = capOf(approval.summary)?.amount;
  const retire = approve && approval.risk === "delisting";
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
      // As the admin's own changes: retiring a model is the destructive one; a grant, a top-up and a price publication
      // are confirmed like the credit and price dialogs, which are not.
      danger={retire}
      // Retiring is high-risk (docs/frontend.md): the model's name is typed before it is sent, as on the model page.
      confirm={retire ? retiredName(approval) : undefined}
      // The answer says for itself whether anything ran: an approval whose operation did not run is no "Done".
      verdict
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
      done={(answer) => <Outcome answer={answer} />}
      onClose={onClose}
    />
  );
}

// retiredName is what the owner types to retire a model: its slug as the server read it, or its ID for an approval
// asked before the summary named it.
function retiredName(approval: Approval) {
  const slug = (approval.summary as { slug?: unknown } | undefined)?.slug;
  return typeof slug === "string" && slug !== "" ? slug : approval.target.id;
}

const outcomeTones = {
  done: "good",
  refused: "warn",
  canceled: "warn",
  rejected: "neutral",
  other: "neutral",
} as const;

// Outcome is what the decision did, with a tone that does not call an operation that did not run a success.
function Outcome({ answer }: { answer: Approval }) {
  const { t } = useTranslation();
  const outcome = outcomeOf(answer);
  return (
    <p role="status" className="space-y-2">
      <State tone={outcomeTones[outcome]}>{t(`approvals.outcome.${outcome}`)}</State>
      <span className="block text-t1">{outcomeText(t, answer)}</span>
    </p>
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
