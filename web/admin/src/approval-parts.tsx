import type { AdminSchemas } from "@axiom/api";
import { Panel, Table, Td, useFormat } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type CapLimits, type EnvelopeLimits, open } from "./approvals";
import { Head, State } from "./parts";

type Approval = AdminSchemas["OperationsApproval"];

// ApprovalState is where an approval is, in words: one still pending past its expiry can no longer be decided, though
// the sweep has not marked it yet.
export function ApprovalState({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  if (approval.state === "pending") {
    return open(approval, Date.now()) ? (
      <State tone="warn">{t("approvals.state.pending")}</State>
    ) : (
      <State tone="neutral">{t("approvals.lapsed")}</State>
    );
  }
  // An approval stands even when its operation then did not run (the target changed): that is not a success.
  if (approval.state === "approved" && approval.operation_state !== "succeeded") {
    return <State tone="warn">{t("approvals.approvedNotRun")}</State>;
  }
  return (
    <State tone={approval.state === "approved" ? "good" : "neutral"}>{t(`approvals.state.${approval.state}`)}</State>
  );
}

// Limits is a table of what a proposal was weighed against: a row per limit, its values under the columns after its
// label; a limit without a value in a column shows a dash there.
function Limits({
  title,
  intro,
  lead,
  cols,
  rows,
  after,
}: {
  title: string;
  intro: string;
  lead?: ReactNode;
  cols: string[];
  rows: [string, ...(ReactNode | undefined)[]][];
  after?: ReactNode;
}) {
  return (
    <Panel title={title}>
      <p className="px-4 pt-4 text-t2">{intro}</p>
      {lead}
      <Table label={title}>
        <Head cols={cols.map((col, i) => [col, i === 0 ? undefined : "text-right"])} />
        <tbody>
          {rows.map(([label, ...values]) => (
            <tr key={label}>
              <Td>{label}</Td>
              {cols.slice(1).map((col, i) => (
                <Td key={col} className="whitespace-nowrap text-right tabular-nums">
                  {values[i] ?? "—"}
                </Td>
              ))}
            </tr>
          ))}
        </tbody>
      </Table>
      {after}
    </Panel>
  );
}

// CapTable shows a transfer's amount against each window of its cap policy, as the server read them when the agent
// asked.
export function CapTable({ cap }: { cap: CapLimits }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = format.money;
  return (
    <Limits
      title={t("approvals.cap.title")}
      intro={t("approvals.cap.intro")}
      lead={
        <p className="px-4 pt-2 text-t1">
          {t("approvals.cap.amount")} <span className="font-medium tabular-nums">{format.money(cap.amount)}</span>
        </p>
      }
      cols={[t("approvals.cap.window"), t("approvals.cap.used"), t("approvals.cap.max"), t("approvals.cap.hard")]}
      rows={[
        [t("approvals.cap.single"), undefined, money(cap.single.max), money(cap.single.hard)],
        [t("approvals.cap.daily"), money(cap.daily.used), money(cap.daily.max), money(cap.daily.hard)],
        [t("approvals.cap.weekly"), money(cap.weekly.used), money(cap.weekly.max), money(cap.weekly.hard)],
        [t("approvals.cap.objectDaily"), money(cap.objectDaily.used), money(cap.objectDaily.max)],
        [t("approvals.cap.count"), format.number(cap.count.used), format.number(cap.count.max)],
      ]}
      after={cap.paused ? <p className="px-4 pb-4 text-warn">{t("approvals.cap.paused")}</p> : null}
    />
  );
}

// EnvelopeTable shows a sale price's move and margin against the site's envelope; a ratio is in basis points.
export function EnvelopeTable({ envelope }: { envelope: EnvelopeLimits }) {
  const { t } = useTranslation();
  const format = useFormat();
  const percent = (bps: number) => `${format.decimal((bps / 100).toString())}%`;
  return (
    <Limits
      title={t("approvals.envelope.title")}
      intro={t("approvals.envelope.intro")}
      cols={[t("approvals.envelope.item"), t("approvals.envelope.value"), t("approvals.envelope.limit")]}
      rows={[
        [
          t("approvals.envelope.change"),
          envelope.change === null ? t("approvals.envelope.unmeasured") : percent(envelope.change),
        ],
        [
          t("approvals.envelope.margin"),
          envelope.margin === null ? t("approvals.envelope.unknownCost") : percent(envelope.margin),
          t("approvals.envelope.atLeast", { value: percent(envelope.minMargin) }),
        ],
        [t("approvals.envelope.modelChange"), percent(envelope.modelChange.used), percent(envelope.modelChange.max)],
        [t("approvals.envelope.changes"), format.number(envelope.changes.used), format.number(envelope.changes.max)],
        [t("approvals.envelope.models"), format.number(envelope.models.used), format.number(envelope.models.max)],
      ]}
      after={envelope.movedToday ? <p className="px-4 pb-4 text-t3">{t("approvals.envelope.movedToday")}</p> : null}
    />
  );
}
