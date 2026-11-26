import type { AdminSchemas } from "@axiom/api";
import { DataTable, PaceBar, Stack, Tag, Text, useFormat } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useLines } from "../lines";
import { suppliersQuery } from "../queries";
import { checkText } from "./credential-dialogs";
import { ModeTag } from "./parts";
import { credentialName, modeOf } from "./upstream";

type Credential = AdminSchemas["SupplierCredential"];
type Supplier = AdminSchemas["Supplier"];

type Standing = { word: Credential["status"] | "full" | "minuteFull"; tone: "ok" | "warn" | "bad" | "neutral" };

// Where a credential stands (adUpkSt): stopped by a person, refused by the supplier, at a ceiling, or fine.
export function standingOf(credential: Credential): Standing {
  const { status } = credential;
  if (status === "disabled" || status === "erased") return { word: status, tone: "neutral" };
  if (status === "rejected" || status === "expired") return { word: status, tone: "bad" };
  if (status !== "active") return { word: status, tone: "warn" };
  if ((credential.in_flight ?? 0) >= credential.max_concurrency) return { word: "full", tone: "warn" };
  const rpm = credential.requests_per_minute;
  if (rpm !== undefined && (credential.requests_last_minute ?? 0) >= rpm) return { word: "minuteFull", tone: "warn" };
  return { word: "active", tone: "ok" };
}

export function CredentialState({ credential }: { credential: Credential }) {
  const { t } = useTranslation();
  const { word, tone } = standingOf(credential);
  const refused = credential.status === "rejected" && credential.last_check;
  return (
    <Stack gap={4}>
      <div>
        <Tag tone={tone} dot>
          {t(`upstream.keys.state.${word}`)}
        </Tag>
      </div>
      {refused ? (
        <Text size="sm" tone="bad">
          {checkText(t, refused)}
        </Text>
      ) : null}
    </Stack>
  );
}

// A credential's use against a ceiling, as a short bar and the two numbers; a ceiling not set is said in words.
export function Use({ label, used, ceiling }: { label: string; used: number; ceiling: number | undefined }) {
  const { t } = useTranslation();
  if (ceiling === undefined) {
    return (
      <Text as="span" tone="muted" numeric>
        {`${used} / ${t("upstream.credential.noCeiling")}`}
      </Text>
    );
  }
  return (
    <span>
      <PaceBar label={label} value={used} max={ceiling} width={72} />
      <Text as="span" size="sm" tone="muted" numeric>
        {` ${used} / ${ceiling}`}
      </Text>
    </span>
  );
}

// The credentials table (adPg_upkeys' table, and a channel's Key tab): what each is, its line, how much of its
// ceilings it holds now, and where it stands; a row opens its drawer. The way in is a column only across suppliers.
export function CredentialTable({
  rows,
  total,
  suppliers,
  lineOf,
  across,
  onOpen,
  current,
}: {
  rows: readonly Credential[];
  total: number;
  suppliers: ReadonlyMap<string, Supplier>;
  lineOf: (credential: Credential) => string;
  across: boolean;
  onOpen: (credential: Credential) => void;
  current?: string;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <DataTable
      label={t("upstream.tabs.keys")}
      rows={rows}
      total={total}
      rowKey={(row) => row.id}
      onOpen={onOpen}
      current={current}
      columns={credentialColumns(t, format, (row) => suppliers.get(row.supplier_id ?? ""), lineOf, across)}
    />
  );
}

// The credentials table's columns: what each is, its way in (only across suppliers), its line, its ceilings, where it
// stands and when it was added.
// A credential's name, with its channel's under it in a list across channels.
function Account({ credential, supplier }: { credential: Credential; supplier?: Supplier }) {
  return (
    <>
      <Text as="span" medium>
        {credentialName(credential)}
      </Text>
      {supplier ? (
        <Text size="sm" tone="muted">
          {supplier.name}
        </Text>
      ) : null}
    </>
  );
}

function credentialColumns(
  t: ReturnType<typeof useTranslation>["t"],
  format: ReturnType<typeof useFormat>,
  supplier: (row: Credential) => Supplier | undefined,
  lineOf: (credential: Credential) => string,
  across: boolean,
) {
  return [
    {
      key: "account",
      header: t("upstream.keys.col.account"),
      cell: (row: Credential) => <Account credential={row} supplier={across ? supplier(row) : undefined} />,
    },
    ...(across
      ? [
          {
            key: "mode",
            header: t("upstream.keys.col.mode"),
            from: "md" as const,
            cell: (row: Credential) => {
              const of = supplier(row);
              return of ? <ModeTag mode={modeOf(of)} /> : "—";
            },
          },
        ]
      : []),
    { key: "line", header: t("upstream.keys.col.line"), cell: (row: Credential) => lineOf(row) },
    {
      key: "concurrency",
      header: t("upstream.keys.col.concurrency"),
      from: "lg",
      sort: (row: Credential) => (row.in_flight ?? 0) / row.max_concurrency,
      cell: (row: Credential) => (
        <Use label={t("upstream.keys.col.concurrency")} used={row.in_flight ?? 0} ceiling={row.max_concurrency} />
      ),
    },
    {
      key: "minute",
      header: t("upstream.keys.col.minute"),
      from: "xl",
      cell: (row: Credential) => (
        <Use
          label={t("upstream.keys.col.minute")}
          used={row.requests_last_minute ?? 0}
          ceiling={row.requests_per_minute}
        />
      ),
    },
    {
      key: "status",
      header: t("upstream.keys.col.status"),
      cell: (row: Credential) => <CredentialState credential={row} />,
    },
    {
      key: "added",
      header: t("upstream.keys.col.added"),
      from: "lg",
      muted: true,
      cell: (row: Credential) => (row.created_at ? format.dateTimeMinute(new Date(row.created_at)) : "—"),
    },
  ] as const;
}

// Every supplier, read once for naming the credentials: the list the lines read (useLines reads it to the end).
export function useSuppliers() {
  const lines = useLines();
  const list = useInfiniteQuery(suppliersQuery());
  const suppliers = new Map((list.data?.pages.flatMap((page) => page.data) ?? []).map((s) => [s.id, s] as const));
  const lineOf = (credential: Credential) =>
    lines.byGroup.get(credential.group_id)?.name ?? suppliers.get(credential.supplier_id ?? "")?.name ?? "—";
  return { suppliers, lineOf };
}
