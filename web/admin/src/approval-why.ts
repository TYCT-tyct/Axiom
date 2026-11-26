import type { AdminSchemas } from "@axiom/api";
import type { useFormat } from "@axiom/ui";
import type { TFunction } from "i18next";
import { percentOf } from "./approval-parts";
import { envelopeMaximaOf } from "./approval-rule";
import { type CapLimit, capOf, type EnvelopeLimit, type Reason, reasonsOf } from "./approvals";

type Approval = AdminSchemas["OperationsApproval"];
type Format = ReturnType<typeof useFormat>;

// whyLines is the sentences of why the approval needed a person; none when an older node asked for it without reasons.
export function whyLines(t: TFunction, format: Format, approval: Approval): string[] | undefined {
  const reasons = reasonsOf(approval);
  // Two codes a later server added read the same here, so each line is shown once.
  return reasons && [...new Set(reasons.map((reason) => sentenceOf(t, format, approval, reason)))];
}

// unknown stands for a limit the summary did not keep, so a sentence never shows a placeholder.
export const unknown = "—";

function sentenceOf(t: TFunction, format: Format, approval: Approval, reason: Reason): string {
  switch (reason.kind) {
    case "always":
      return approval.risk === "gift" || approval.risk === "delisting" || approval.risk === "refund"
        ? t(`approvals.why.always.${approval.risk}`)
        : t("approvals.why.always.other");
    case "paused":
      return t("approvals.why.paused");
    case "cap":
      return capSentence(t, format, approval, reason.limit);
    case "envelope":
      return envelopeSentence(t, format, approval, reason.limit);
    default:
      return t("approvals.why.unknown");
  }
}

function capSentence(t: TFunction, format: Format, approval: Approval, limit: CapLimit): string {
  const cap = capOf(approval.summary);
  if (limit === "count") {
    return t("approvals.why.count", { max: cap ? format.number(cap.count.max) : unknown });
  }
  const max = cap?.[limit].max;
  return t(`approvals.why.${limit}`, { max: max ? format.money(max) : unknown });
}

function envelopeSentence(t: TFunction, format: Format, approval: Approval, limit: EnvelopeLimit): string {
  const envelope = envelopeMaximaOf(approval.summary);
  const percent = percentOf(format.decimal);
  return t(`approvals.why.envelope.${limit}`, {
    min: envelope ? percent(envelope.minMargin) : unknown,
    max: envelope ? percent(envelope.modelChange) : unknown,
    changes: envelope ? format.number(envelope.changes) : unknown,
    models: envelope ? format.number(envelope.models) : unknown,
  });
}
