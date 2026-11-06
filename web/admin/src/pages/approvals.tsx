import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { DataState, EmptyState, PageHeader, Panel, Table, Td, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ApprovalState, Minute, Target, useNow } from "../approval-parts";
import { approvalState, approvalStates, capOf } from "../approvals";
import { ChoiceFilter, FilterBar, Head, mainCell, PageBody } from "../parts";
import { approvalsQuery } from "../queries";

type Approval = AdminSchemas["OperationsApproval"];

// The owner's approvals: what the operations agent asked to be decided, newest first, so the ones still waiting (30
// minutes at most) are at the top. Deciding is on an approval's own page, beside what the server read of its target.
export default function Approvals() {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/approvals" });
  const navigate = useNavigate();
  return (
    <>
      <PageHeader title={t("nav.approvals")} description={t("approvals.description")} />
      <PageBody>
        <FilterBar
          values={filters}
          onApply={(values) => navigate({ to: "/approvals", search: { state: approvalState(values.state) } })}
        >
          <ChoiceFilter
            label={t("approvals.filters.state")}
            name="state"
            value={filters.state}
            any={t("approvals.filters.stateAny")}
            options={approvalStates.map((state) => ({ value: state, label: t(`approvals.state.${state}`) }))}
          />
        </FilterBar>
        <Panel title={t("approvals.list")}>
          <List state={filters.state} />
        </Panel>
      </PageBody>
    </>
  );
}

function List({ state }: { state: AdminSchemas["OperationsApprovalState"] | undefined }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery(approvalsQuery(state));
  const rows = query.data?.pages.flatMap((page) => page.data) ?? [];
  const now = useNow();
  return (
    <>
      <DataState
        query={query}
        isEmpty={() => rows.length === 0}
        empty={
          <EmptyState title={t(state ? "approvals.noMatch.title" : "approvals.empty.title")}>
            {t(state ? "approvals.noMatch.body" : "approvals.empty.body")}
          </EmptyState>
        }
      >
        {() => (
          <Table label={t("approvals.list")}>
            <Head
              cols={[
                [t("approvals.col.operation")],
                [t("approvals.col.target"), "hidden lg:table-cell"],
                [t("approvals.col.amount"), "hidden text-right sm:table-cell"],
                [t("approvals.col.state")],
                [t("approvals.col.requested"), "hidden xl:table-cell"],
                [t("approvals.col.expires"), "hidden 2xl:table-cell"],
              ]}
            />
            <tbody>
              {rows.map((approval) => (
                <Row key={approval.id} approval={approval} now={now} />
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={query} />
    </>
  );
}

function Row({ approval, now }: { approval: Approval; now: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const amount = capOf(approval.summary)?.amount;
  return (
    <tr className="align-top hover:bg-s2">
      <Td className={mainCell}>
        <Link
          to="/approvals/$approvalId"
          params={{ approvalId: approval.id }}
          className="block truncate text-accent hover:underline"
        >
          {t(`approvals.risk.${approval.risk}`)}
        </Link>
        {amount ? (
          <span className="block text-label tabular-nums text-t3 sm:hidden">{format.money(amount)}</span>
        ) : null}
      </Td>
      <Td className="hidden max-w-80 lg:table-cell">
        <Target approval={approval} />
      </Td>
      <Td className="hidden whitespace-nowrap text-right tabular-nums sm:table-cell">
        {amount ? format.money(amount) : "—"}
      </Td>
      <Td>
        <ApprovalState approval={approval} now={now} />
      </Td>
      <Td className="hidden whitespace-nowrap xl:table-cell">
        <Minute at={approval.requested_at} />
      </Td>
      <Td className="hidden whitespace-nowrap 2xl:table-cell">
        <Minute at={approval.expires_at} />
      </Td>
    </tr>
  );
}
