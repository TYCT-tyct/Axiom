import type { AdminSchemas } from "@axiom/api";
import { Id, Panel, Table, Td, useFormat } from "@axiom/ui";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  type CapLimit,
  type CapLimits,
  capOver,
  type EnvelopeLimit,
  type EnvelopeLimits,
  envelopeOver,
  minutesLeft,
  open,
  targetName,
} from "./approvals";
import { Head, State } from "./parts";

type Approval = AdminSchemas["OperationsApproval"];

// useNow is the time, again every 15 seconds: an approval's minutes left and whether it can still be decided change
// while the page is open.
export function useNow() {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 15_000);
    return () => clearInterval(timer);
  }, []);
  return now;
}

// ApprovalState is where an approval is, in words: one still pending past its expiry can no longer be decided, though
// the sweep has not marked it yet; one still open says how long it has left, which matters more than the clock time.
export function ApprovalState({ approval, now }: { approval: Approval; now: number }) {
  const { t } = useTranslation();
  if (approval.state === "pending") {
    return open(approval, now) ? (
      <span className="inline-flex flex-col items-start gap-1">
        <State tone="warn">{t("approvals.state.pending")}</State>
        <span className="text-label text-t3">{t("approvals.expiresIn", { count: minutesLeft(approval, now) })}</span>
      </span>
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

// Minute is a moment to the minute in the viewer's zone, with the exact time in its title.
export function Minute({ at }: { at: string | undefined }) {
  const format = useFormat();
  if (!at) {
    return "—";
  }
  const date = new Date(at);
  return <span title={format.dateTime(date)}>{format.dateTimeMinute(date)}</span>;
}

// Target names what an approval acts on as a person knows it, with the ID below to copy; an approval from an older
// node that read no name has the ID alone.
const kinds = ["account", "supplier", "model"] as const;
const isKind = (kind: string): kind is (typeof kinds)[number] => (kinds as readonly string[]).includes(kind);

export function Target({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const named = targetName(approval);
  const kind = approval.target.kind;
  return (
    <span className="block min-w-0">
      {named ? <span className="block font-medium text-t1 [overflow-wrap:anywhere]">{named.name}</span> : null}
      {named?.detail ? <span className="block text-label text-t2 [overflow-wrap:anywhere]">{named.detail}</span> : null}
      <span className="flex min-w-0 items-center gap-1 text-label text-t3">
        <span className="shrink-0">{isKind(kind) ? t(`approvals.kind.${kind}`) : kind}</span>
        <Id value={approval.target.id} />
      </span>
    </span>
  );
}

type Row = { key: string; label: string; values: (ReactNode | undefined)[]; flag?: string };

// Limits is a table of what a proposal was weighed against: a row per limit, its values under the columns after its
// label. A row the proposal does not keep within is marked in words as well as by a bar.
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
  rows: Row[];
  after?: ReactNode;
}) {
  return (
    <Panel title={title}>
      <p className="px-4 pt-4 text-t2">{intro}</p>
      {lead}
      <Table label={title}>
        <Head cols={cols.map((col, i) => [col, i === 0 ? undefined : "text-right"])} />
        <tbody>
          {rows.map((row) => (
            <tr key={row.key}>
              {/* A warning badge on a warning tint falls under the contrast floor, so the row is marked by a bar. */}
              <Td className={row.flag ? "border-l-4 border-l-warn" : undefined}>
                <span className="block">{row.label}</span>
                {row.flag ? (
                  <span className="mt-1 block">
                    <State tone="warn">{row.flag}</State>
                  </span>
                ) : null}
              </Td>
              {cols.slice(1).map((col, i) => (
                <Td key={col} className="whitespace-nowrap text-right tabular-nums">
                  {row.values[i] ?? "—"}
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
// asked, and marks the autonomous limits it goes over.
export function CapTable({ cap }: { cap: CapLimits }) {
  const { t } = useTranslation();
  const format = useFormat();
  const money = format.money;
  const over = new Set<CapLimit>(capOver(cap));
  const flag = (limit: CapLimit) => (over.has(limit) ? t("approvals.cap.over") : undefined);
  return (
    <Limits
      title={t("approvals.cap.title")}
      intro={t("approvals.cap.intro")}
      lead={
        <p className="px-4 pt-2 text-t1">
          {t("approvals.cap.amount")} <span className="font-medium tabular-nums">{money(cap.amount)}</span>
          {cap.rev === undefined ? null : (
            <span className="ml-2 text-label text-t3">{t("approvals.cap.rev", { rev: cap.rev })}</span>
          )}
        </p>
      }
      cols={[t("approvals.cap.window"), t("approvals.cap.used"), t("approvals.cap.max"), t("approvals.cap.hard")]}
      rows={[
        {
          key: "single",
          label: t("approvals.cap.single"),
          values: [undefined, money(cap.single.max), money(cap.single.hard)],
          flag: flag("single"),
        },
        {
          key: "daily",
          label: t("approvals.cap.daily"),
          values: [money(cap.daily.used), money(cap.daily.max), money(cap.daily.hard)],
          flag: flag("daily"),
        },
        {
          key: "weekly",
          label: t("approvals.cap.weekly"),
          values: [money(cap.weekly.used), money(cap.weekly.max), money(cap.weekly.hard)],
          flag: flag("weekly"),
        },
        {
          key: "objectDaily",
          label: t("approvals.cap.objectDaily"),
          values: [money(cap.objectDaily.used), money(cap.objectDaily.max)],
          flag: flag("objectDaily"),
        },
        {
          key: "count",
          label: t("approvals.cap.count"),
          values: [format.number(cap.count.used), format.number(cap.count.max)],
          flag: flag("count"),
        },
      ]}
      after={cap.paused ? <p className="px-4 pt-3 pb-4 text-warn">{t("approvals.cap.paused")}</p> : null}
    />
  );
}

export const percentOf = (decimal: (text: string) => string) => (bps: number) => `${decimal((bps / 100).toString())}%`;

// EnvelopeTable shows a sale price's move and margin against the site's envelope, and marks what the envelope does
// not cover: a limit it goes over, and one it cannot check because the move or the cost is unknown.
export function EnvelopeTable({ envelope }: { envelope: EnvelopeLimits }) {
  const { t } = useTranslation();
  const format = useFormat();
  const percent = percentOf(format.decimal);
  const over = new Set<EnvelopeLimit>(envelopeOver(envelope));
  const flag = (limit: EnvelopeLimit) => (over.has(limit) ? t(`approvals.envelope.flag.${limit}`) : undefined);
  return (
    <Limits
      title={t("approvals.envelope.title")}
      intro={t("approvals.envelope.intro")}
      lead={
        envelope.rev === undefined ? null : (
          <p className="px-4 pt-2 text-label text-t3">{t("approvals.envelope.rev", { rev: envelope.rev })}</p>
        )
      }
      cols={[t("approvals.envelope.item"), t("approvals.envelope.value"), t("approvals.envelope.limit")]}
      rows={[
        {
          key: "change",
          label: t("approvals.envelope.change"),
          values: [envelope.change === null ? t("approvals.envelope.unmeasured") : percent(envelope.change)],
          flag: flag("unmeasured"),
        },
        {
          key: "margin",
          label: t("approvals.envelope.margin"),
          values: [
            envelope.margin === null ? t("approvals.envelope.unknownCost") : percent(envelope.margin),
            t("approvals.envelope.atLeast", { value: percent(envelope.minMargin) }),
          ],
          flag: flag("unknownCost") ?? flag("margin"),
        },
        {
          key: "modelChange",
          label: t("approvals.envelope.modelChange"),
          values: [percent(envelope.modelChange.used), percent(envelope.modelChange.max)],
          flag: flag("modelChange"),
        },
        {
          key: "changes",
          label: t("approvals.envelope.changes"),
          values: [format.number(envelope.changes.used), format.number(envelope.changes.max)],
          flag: flag("changes"),
        },
        {
          key: "models",
          label: t("approvals.envelope.models"),
          values: [format.number(envelope.models.used), format.number(envelope.models.max)],
          flag: flag("models"),
        },
      ]}
      after={
        envelope.movedToday ? <p className="px-4 pt-3 pb-4 text-t3">{t("approvals.envelope.movedToday")}</p> : null
      }
    />
  );
}
