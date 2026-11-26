import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { Cluster, Text, useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { capOf, outcomeOf } from "./approvals";
import { ChangeDialog } from "./change";
import { State } from "./parts";

type Approval = AdminSchemas["OperationsApproval"];
export type Decision = "approve" | "reject";

// DecisionDialog approves or rejects one approval. The answer, not the dialog's snapshot, says what happened: an approval
// can be recorded while its operation does not run. On an approval's own page the answer is read in the dialog; on a card
// of the approvals board (decided) the dialog closes with a toast that says it, and the card folds away once the server
// has taken the decision.
export function DecisionDialog({
  kind,
  approval,
  onClose,
  decided,
}: {
  kind: Decision;
  approval: Approval;
  onClose: () => void;
  decided?: (answer: Approval) => void;
}) {
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
        ).then((sent) => {
          decided?.(sent.data);
          return sent;
        })
      }
      toast={decided ? (answer) => outcomeText(t, answer) : undefined}
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
    <div role="status">
      <Cluster>
        <State tone={outcomeTones[outcome]}>{t(`approvals.outcome.${outcome}`)}</State>
        <Text as="span">{outcomeText(t, answer)}</Text>
      </Cluster>
    </div>
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
