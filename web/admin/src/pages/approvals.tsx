import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import {
  ActionButton,
  Cluster,
  DataState,
  DataTable,
  DiffRows,
  EmptyState,
  List,
  ListItem,
  PageHead,
  ProposalCard,
  SegmentedControl,
  Text,
  TextLink,
  Tooltip,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Decision, DecisionDialog } from "../approval-decision";
import { diffOf, limitLine } from "../approval-diff";
import { ApprovalState, Minute, useNow } from "../approval-parts";
import { whyLines } from "../approval-why";
import { capOf, decidable, minutesLeft, targetName } from "../approvals";
import { PageBody } from "../parts";
import { approvalsQuery } from "../queries";

type Approval = AdminSchemas["OperationsApproval"];

// How long a decided card takes to fold away before the page lets go of it (the board's 260ms).
const foldMs = 260;

// 审批, the board's approvals: each change the operations agent asked for that waits for a decision is a card with its
// reason, its grounds, what it changes and how far its limits allow, so it can be decided without opening anything. A
// decided card folds away. The ones already decided are a table, each leading to the approval's own page, where
// everything the server read of it is.
export default function Approvals() {
  const { t } = useTranslation();
  const { state, view } = useSearch({ from: "/app/approvals" });
  const navigate = useNavigate();
  const done = view === "done" || (state !== undefined && state !== "pending");
  const waiting = useInfiniteQuery(approvalsQuery("pending"));
  const count = (waiting.data?.pages.flatMap((page) => page.data) ?? []).filter(decidable).length;
  return (
    <PageBody>
      <PageHead
        title={t("shell.nav.approvals")}
        lede={t("approvals.board.lede")}
        actions={
          <ActionButton asChild size="sm">
            <Link to="/operations-credentials">{t("approvals.board.agent")}</Link>
          </ActionButton>
        }
      />
      <Cluster>
        <SegmentedControl
          label={t("approvals.board.viewLabel")}
          value={done ? "done" : "waiting"}
          options={[
            { value: "waiting", label: t("approvals.board.waiting", { count }) },
            { value: "done", label: t("approvals.board.done") },
          ]}
          onChange={(next) =>
            navigate({ to: "/approvals", search: { state: undefined, view: next === "done" ? "done" : undefined } })
          }
        />
      </Cluster>
      {done ? <Decided state={state} /> : <Waiting />}
    </PageBody>
  );
}

// The cards decided here that are folding away or gone. A card stays where it was while it folds, though the list has
// already stopped saying it waits, and does not come back from a list that was read a moment too early.
function useFolding() {
  const [folding, setFolding] = useState<ReadonlyMap<string, { approval: Approval; at: number }>>(new Map());
  const [gone, setGone] = useState<ReadonlySet<string>>(new Set());
  const fold = (approval: Approval, at: number) => {
    setFolding((was) => new Map(was).set(approval.id, { approval, at }));
    setTimeout(() => {
      setFolding((was) => {
        const next = new Map(was);
        next.delete(approval.id);
        return next;
      });
      setGone((was) => new Set(was).add(approval.id));
    }, foldMs);
  };
  return { folding, gone, fold };
}

function Waiting() {
  const { t } = useTranslation();
  const query = useInfiniteQuery(approvalsQuery("pending"));
  const now = useNow();
  const { folding, gone, fold } = useFolding();
  const [deciding, setDeciding] = useState<{ kind: Decision; approval: Approval }>();
  const waiting = query.data?.pages.flatMap((page) => page.data) ?? [];
  const cards = waiting.filter((one) => decidable(one) && !gone.has(one.id));
  // A card that is folding and has left the list goes back in the place it had.
  for (const { approval, at } of folding.values()) {
    if (!cards.some((one) => one.id === approval.id)) cards.splice(Math.min(at, cards.length), 0, approval);
  }
  return (
    <>
      <DataState
        query={query}
        isEmpty={() => cards.length === 0}
        empty={<EmptyState title={t("approvals.board.noneTitle")}>{t("approvals.board.noneBody")}</EmptyState>}
      >
        {() =>
          cards.map((approval) => (
            <Proposal
              key={approval.id}
              approval={approval}
              now={now}
              leaving={folding.has(approval.id)}
              onDecide={(kind) => setDeciding({ kind, approval })}
            />
          ))
        }
      </DataState>
      {deciding ? (
        <DecisionDialog
          kind={deciding.kind}
          approval={deciding.approval}
          decided={() =>
            fold(
              deciding.approval,
              cards.findIndex((one) => one.id === deciding.approval.id),
            )
          }
          onClose={() => setDeciding(undefined)}
        />
      ) : null}
    </>
  );
}

// The agent's own words, as plain text, and under them the grounds the server gives for needing a person.
function Why({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const format = useFormat();
  const grounds = whyLines(t, format, approval) ?? [];
  return (
    <>
      <Text breaks>{approval.agent_reason}</Text>
      {grounds.length > 0 ? (
        <List>
          {grounds.map((line) => (
            <ListItem key={line}>{line}</ListItem>
          ))}
        </List>
      ) : null}
    </>
  );
}

// What the approval changes, from what the server read of its target, and how far its limits allow.
function Change({ approval }: { approval: Approval }) {
  const { t } = useTranslation();
  const format = useFormat();
  const rows = diffOf(t, format, approval);
  const limit = limitLine(t, format, approval);
  return (
    <>
      {rows.length > 0 ? <DiffRows rows={rows} /> : null}
      {limit ? (
        <Text size="sm" tone="faint">
          {limit}
        </Text>
      ) : null}
    </>
  );
}

// One change waiting for a decision, with everything a decision needs on it.
function Proposal({
  approval,
  now,
  leaving,
  onDecide,
}: {
  approval: Approval;
  now: number;
  leaving: boolean;
  onDecide: (kind: Decision) => void;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const name = targetName(approval)?.name ?? approval.target.id.slice(0, 8);
  const amount = capOf(approval.summary)?.amount;
  const open = decidable(approval);
  return (
    <ProposalCard
      kind={t(`approvals.risk.${approval.risk}`)}
      title={amount ? t("approvals.board.titleAmount", { target: name, amount: format.money(amount) }) : name}
      expiry={open ? t("approvals.expiresIn", { count: minutesLeft(approval, now) }) : t("approvals.lapsed")}
      meta={
        <>
          <span>{t("approvals.board.agent")}</span>
          <span>
            <Minute at={approval.requested_at} /> {t("approvals.board.asked")}
          </span>
          <TextLink>
            <Link to="/approvals/$approvalId" params={{ approvalId: approval.id }}>
              {t("approvals.board.detail")}
            </Link>
          </TextLink>
        </>
      }
      why={<Why approval={approval} />}
      change={<Change approval={approval} />}
      note={open ? undefined : t("approvals.board.lapsedNote")}
      actions={
        <>
          <ActionButton variant="gho" disabled={!open} onClick={() => onDecide("reject")}>
            {t("approvals.reject")}
          </ActionButton>
          <ActionButton variant="pri" disabled={!open} onClick={() => onDecide("approve")}>
            {t("approvals.approve")}
          </ActionButton>
        </>
      }
      leaving={leaving}
    />
  );
}

// The columns of the decided approvals: the proposal leads to its own page, the result is where it stands now.
function useColumns(now: number) {
  const { t } = useTranslation();
  return [
    {
      key: "proposal",
      header: t("approvals.board.col.proposal"),
      cell: (approval: Approval) => (
        <TextLink>
          <Link to="/approvals/$approvalId" params={{ approvalId: approval.id }}>
            {targetName(approval)?.name ?? approval.target.id.slice(0, 8)}
          </Link>
        </TextLink>
      ),
    },
    {
      key: "kind",
      header: t("approvals.board.col.kind"),
      cell: (approval: Approval) => t(`approvals.risk.${approval.risk}`),
    },
    {
      key: "asked",
      header: t("approvals.board.col.asked"),
      muted: true,
      cell: (approval: Approval) => <Minute at={approval.requested_at} />,
    },
    {
      key: "result",
      header: t("approvals.board.col.result"),
      cell: (approval: Approval) => <ApprovalState approval={approval} now={now} />,
    },
    {
      key: "by",
      header: t("approvals.board.col.by"),
      muted: true,
      cell: (approval: Approval) => approval.decided_by ?? "—",
    },
    {
      key: "note",
      header: t("approvals.board.col.note"),
      muted: true,
      cell: (approval: Approval) => (approval.decision_reason ? <Tooltip text={approval.decision_reason} /> : "—"),
    },
  ];
}

// The approvals already decided, or lapsed, newest first, narrowed to one state when the address names it.
function Decided({ state }: { state: Approval["state"] | undefined }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery(approvalsQuery(state === "pending" ? undefined : state));
  const columns = useColumns(useNow());
  const rows = (query.data?.pages.flatMap((page) => page.data) ?? []).filter(
    (one) => one.state !== "pending" || !decidable(one),
  );
  return (
    <>
      <DataState
        query={query}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("approvals.board.noneDoneTitle")}>{t("approvals.board.noneDoneBody")}</EmptyState>}
      >
        {() => (
          <DataTable
            label={t("approvals.board.done")}
            rows={rows}
            rowKey={(approval) => approval.id}
            columns={columns}
          />
        )}
      </DataState>
      <LoadMore list={query} />
    </>
  );
}
