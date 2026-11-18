import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import { ActionButton, Card, CheckField, DataState, EmptyState, PageHead, Select, Table, Td, Text } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem } from "../change";
import { ModelField } from "../models/model-field";
import { ChoiceFilter, Code, FilterBar, Head, PageBody, text } from "../parts";
import { pendingMappingsQuery, suppliersQuery } from "../queries";

type Pending = AdminSchemas["PendingMapping"];

export default function Mappings() {
  const { t } = useTranslation();
  const { supplier } = useSearch({ from: "/app/mappings" });
  const pending = useInfiniteQuery(pendingMappingsQuery(supplier));
  const suppliers = useInfiniteQuery(suppliersQuery());
  const names = new Map(suppliers.data?.pages.flatMap((page) => page.data).map((s) => [s.id, s.name]));
  const rows = pending.data?.pages.flatMap((page) => page.data) ?? [];
  const manage = can(useSession(), "suppliers.manage");
  const [mapping, setMapping] = useState<Pending | null>(null);
  return (
    <>
      <PageHead title={t("nav.mappings")} lede={t("mappings.description")} />
      <PageBody>
        <Filter supplier={supplier} suppliers={[...names]} />
        <Card layout="flush">
          <DataState
            query={pending}
            isEmpty={() => rows.length === 0}
            empty={<EmptyState title={t("mappings.empty.title")}>{t("mappings.empty.body")}</EmptyState>}
          >
            {() => <PendingTable rows={rows} names={names} onMap={manage ? setMapping : undefined} />}
          </DataState>
          <LoadMore list={pending} />
        </Card>
      </PageBody>
      {mapping ? <MapDialog pending={mapping} onClose={() => setMapping(null)} /> : null}
    </>
  );
}

function PendingTable({
  rows,
  names,
  onMap,
}: {
  rows: Pending[];
  names: Map<string, string>;
  onMap: ((row: Pending) => void) | undefined;
}) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.mappings")}>
      <Head
        cols={[[t("mappings.col.name"), { main: true }], t("mappings.col.supplier"), t("mappings.col.formats")]}
        action={onMap !== undefined}
      />
      <tbody>
        {rows.map((row) => (
          <tr key={`${row.supplier_id}/${row.group_id}/${row.upstream_name}`}>
            <Td main>
              <Text>
                <Code>{row.upstream_name}</Code>
              </Text>
              <Text size="sm" tone="faint">
                {row.group_codename}
              </Text>
            </Td>
            <Td>{names.get(row.supplier_id) ?? row.supplier_id}</Td>
            <Td>{row.formats.join(" · ")}</Td>
            {onMap ? (
              <Td shrink>
                <ActionButton size="sm" onClick={() => onMap(row)}>
                  {t("mappings.map")}
                </ActionButton>
              </Td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function Filter({ supplier, suppliers }: { supplier: string | undefined; suppliers: [string, string][] }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={{ supplier }}
      onApply={(values) => navigate({ to: "/mappings", search: { supplier: text(values.supplier) } })}
    >
      <ChoiceFilter
        label={t("mappings.col.supplier")}
        name="supplier"
        value={supplier}
        any={t("mappings.anySupplier")}
        options={suppliers.map(([value, label]) => ({ value, label }))}
      />
    </FilterBar>
  );
}

type Alias = Omit<AdminSchemas["ModelMappingRequest"], "reason">;

// An alias says what a name that a supplier lists means: this model, in one group or in the whole supplier. It is never
// changed afterwards, so the scope is chosen with care: a wrong alias is corrected by a narrower one.
function MapDialog({ pending, onClose }: { pending: Pending; onClose: () => void }) {
  const { t } = useTranslation();
  const read = (values: Record<string, string>): Alias | Problem => {
    if (!values.model_id) {
      return { problem: t("mappings.dialog.modelMissing") };
    }
    return {
      upstream_name: pending.upstream_name,
      model_id: values.model_id as AdminSchemas["Uuid"],
      group_id: values.scope === "supplier" ? undefined : pending.group_id,
      // Only the format no adapter can report is offered here; the API takes the others. The projection unions the
      // declaration with what the adapter finds, so a name that already serves chat does not lose it.
      formats: values.completions ? ["completions"] : undefined,
    };
  };
  return (
    <ChangeDialog
      operation={`mapping:${pending.group_id}:${pending.upstream_name}`}
      title={t("mappings.dialog.title")}
      intro={
        <p>
          {t("mappings.dialog.body")} <Code>{pending.upstream_name}</Code>
        </p>
      }
      action={t("mappings.dialog.action")}
      fields={() => (
        <>
          <ModelField label={t("mappings.dialog.model")} />
          <Select label={t("mappings.dialog.scope")} name="scope" defaultValue="group">
            <option value="group">{t("mappings.dialog.scopeGroup", { group: pending.group_codename ?? "" })}</option>
            <option value="supplier">{t("mappings.dialog.scopeSupplier")}</option>
          </Select>
          <CheckField
            name="completions"
            label={t("mappings.dialog.completions")}
            hint={t("mappings.dialog.completionsHint")}
          />
        </>
      )}
      read={read}
      send={(alias, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/suppliers/{supplier_id}/model-mappings", {
            params: { path: { supplier_id: pending.supplier_id }, header: { "Idempotency-Key": key } },
            body: { ...alias, reason },
          }),
        )
      }
      done={(mapping) => (
        <>
          <p>{t("mappings.dialog.done", { name: mapping.upstream_name })}</p>
          <Text size="sm" tone="faint">
            {t("mappings.dialog.wait")}
          </Text>
        </>
      )}
      onClose={onClose}
    />
  );
}
