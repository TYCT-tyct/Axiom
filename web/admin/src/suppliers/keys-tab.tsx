import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import { ActionButton, Card, Cluster, EmptyState, LoadingState, MetricStrip, Text } from "@axiom/ui";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PurgeDialog, TestAllDialog } from "./credential-dialogs";
import { CredentialDrawer } from "./credential-drawer";
import { CredentialTable, standingOf, useSuppliers } from "./credentials";
import { BuiltState, ModeFilter, type ModeFilters, stepsOf } from "./parts";
import { credentialsListQuery } from "./queries";
import { credentialName, type Mode, modeOf } from "./upstream";

type Credential = AdminSchemas["SupplierCredential"];
type Supplier = AdminSchemas["Supplier"];

// The rows that hold what was typed in a key's name, its channel or its line, ignoring case.
function matching(
  rows: readonly Credential[],
  typed: string,
  suppliers: ReadonlyMap<string, Supplier>,
  lineOf: (row: Credential) => string,
) {
  const q = typed.trim().toLowerCase();
  return rows.filter((row) =>
    [credentialName(row), suppliers.get(row.supplier_id ?? "")?.name ?? "", lineOf(row)].some((word) =>
      word.toLowerCase().includes(q),
    ),
  );
}

// 全部 Key (adPg_upkeys): every credential of every supplier, found by name, channel or line and narrowed to a way in,
// with how full each is now. 全部检测 checks them all; the ones the supplier refused can be erased together. The tab
// waits for its first load whole, as the strip drawn from the list would otherwise push the filter and the table down
// (docs/frontend.md「工程要求」); a narrowed list keeps the rows it had until the new ones arrive.
export default function KeysTab({ credential }: { credential: string | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [filters, setFilters] = useState<ModeFilters>({ q: "", mode: "all" });
  const list = useInfiniteQuery({
    ...credentialsListQuery({ mode: filters.mode === "all" ? undefined : filters.mode }),
    placeholderData: keepPreviousData,
  });
  const { suppliers, lineOf } = useSuppliers();
  const rows = list.data?.pages.flatMap((page) => page.data) ?? [];
  const shown = matching(rows, filters.q, suppliers, lineOf);
  const open = (id: string | undefined, replace = false) =>
    void navigate({ to: "/suppliers", search: { tab: "keys", credential: id }, replace });
  const count = { shown: shown.length, total: rows.length };
  const index = shown.findIndex((row) => row.id === credential);
  const opened = shown[index];
  if (list.isPending) return <LoadingState />;
  return (
    <>
      {rows.length > 0 ? <Strip rows={rows} suppliers={suppliers} /> : null}
      <Cluster justify="between">
        <ModeFilter search={t("upstream.keys.search")} values={filters} onChange={setFilters} count={count} />
        <TestAll />
      </Cluster>
      <Card layout="flush">
        <BuiltState
          query={list}
          isEmpty={() => shown.length === 0}
          empty={<Empty none={rows.length === 0 && filters.mode === "all"} />}
        >
          {() => (
            <>
              <CredentialTable
                rows={shown}
                suppliers={suppliers}
                lineOf={lineOf}
                across
                onOpen={(row) => open(row.id)}
                current={credential}
              />
              <Foot rows={rows} />
            </>
          )}
        </BuiltState>
        <LoadMore list={list} />
      </Card>
      {opened ? (
        <CredentialDrawer
          credential={opened}
          supplier={suppliers.get(opened.supplier_id ?? "")}
          line={lineOf(opened)}
          all={rows}
          steps={stepsOf(t, index, shown, (id) => open(id, true))}
          onClose={() => open(undefined)}
        />
      ) : null}
    </>
  );
}

// No credentials at all, or none the filters leave.
function Empty({ none }: { none: boolean }) {
  const { t } = useTranslation();
  const said = none ? "empty" : "noMatch";
  return <EmptyState title={t(`upstream.keys.${said}.title`)}>{t(`upstream.keys.${said}.body`)}</EmptyState>;
}

function TestAll() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  if (!can(useSession(), "suppliers.manage")) return null;
  return (
    <>
      <ActionButton icon="status" onClick={() => setOpen(true)}>
        {t("upstream.keys.testAll")}
      </ActionButton>
      {open ? <TestAllDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}

// Under the table: what a key shows once stored, or how many the supplier refused, with the way to erase them.
function Foot({ rows }: { rows: readonly Credential[] }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "suppliers.manage");
  const [open, setOpen] = useState(false);
  const rejected = rows.filter((row) => row.status === "rejected");
  return (
    <Cluster justify="between">
      <Text size="sm" tone={rejected.length > 0 ? "warn" : "muted"}>
        {rejected.length > 0 ? t("upstream.keys.deadFoot", { count: rejected.length }) : t("upstream.keys.foot")}
      </Text>
      {manage && rejected.length > 0 ? (
        <ActionButton variant="gho" size="sm" onClick={() => setOpen(true)}>
          {t("upstream.keys.purge")}
        </ActionButton>
      ) : null}
      {open ? <PurgeDialog rejected={rejected} onClose={() => setOpen(false)} /> : null}
    </Cluster>
  );
}

const percent = (used: number, of: number) => (of > 0 ? Math.round((used / of) * 100) : 0);

function Strip({ rows, suppliers }: { rows: readonly Credential[]; suppliers: ReadonlyMap<string, Supplier> }) {
  const { t } = useTranslation();
  const by = (mode: Mode) =>
    rows.filter((row) => {
      const of = suppliers.get(row.supplier_id ?? "");
      return of !== undefined && modeOf(of) === mode;
    }).length;
  const used = rows.reduce((sum, row) => sum + (row.in_flight ?? 0), 0);
  const ceiling = rows.reduce((sum, row) => sum + row.max_concurrency, 0);
  const limited = rows.filter((row) => row.requests_per_minute !== undefined);
  const minute = limited.reduce((sum, row) => sum + (row.requests_last_minute ?? 0), 0);
  const rpm = limited.reduce((sum, row) => sum + (row.requests_per_minute ?? 0), 0);
  const bad = rows.filter((row) => row.status === "rejected").length;
  const full = rows.filter((row) => ["full", "minuteFull"].includes(standingOf(row).word)).length;
  return (
    <MetricStrip
      label={t("upstream.keys.strip")}
      items={[
        {
          key: "accounts",
          label: t("upstream.keys.accounts"),
          value: String(rows.length),
          suffix: t("upstream.keys.unit"),
          note: t("upstream.keys.accountsNote", { relay: by("relay"), direct: by("direct"), cloud: by("cloud") }),
        },
        {
          key: "flight",
          label: t("upstream.keys.inFlight"),
          value: String(used),
          suffix: `/ ${ceiling}`,
          note: t("upstream.keys.inFlightNote", { percent: percent(used, ceiling) }),
        },
        {
          key: "minute",
          label: t("upstream.keys.minute"),
          value: String(minute),
          suffix: rpm > 0 ? `/ ${rpm}` : undefined,
          note:
            rpm > 0 ? t("upstream.keys.minuteNote", { percent: percent(minute, rpm) }) : t("upstream.keys.noCeiling"),
        },
        {
          key: "todo",
          label: t("upstream.keys.todo"),
          value: String(bad + full),
          suffix: t("upstream.keys.unit"),
          note:
            bad + full > 0 ? (
              <Text as="span" tone={bad > 0 ? "warn" : "muted"}>
                {t("upstream.keys.todoNote", { bad, full })}
              </Text>
            ) : (
              t("upstream.keys.allFine")
            ),
        },
      ]}
    />
  );
}
