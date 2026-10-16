import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { Button, DataState, EmptyState, Icon, PageHeader, Panel, Table, Td, Th } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Actions, ChoiceFilter, Code, FilterBar, NewButton, PageBody } from "../parts";
import { suppliersQuery } from "../queries";
import { ConnectDialog } from "../suppliers/connect";
import { NativeBalance, SupplierState, UnitValue } from "../suppliers/parts";
import { supplierStatus, supplierStatuses } from "../suppliers/statuses";

export default function Suppliers() {
  const { t } = useTranslation();
  const { status } = useSearch({ from: "/app/suppliers" });
  const suppliers = useInfiniteQuery(suppliersQuery(status));
  const rows = suppliers.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <>
      <PageHeader title={t("nav.suppliers")} description={t("suppliers.description")} />
      <PageBody>
        <Actions>
          <NewButton permission="suppliers.manage" label={t("suppliers.connect.open")}>
            {(close) => <ConnectDialog onClose={close} />}
          </NewButton>
        </Actions>
        <Filter status={status} />
        <Panel>
          <DataState
            query={suppliers}
            isEmpty={() => rows.length === 0}
            empty={
              <EmptyState title={status ? t("suppliers.noMatch.title") : t("suppliers.empty.title")}>
                {status ? t("suppliers.noMatch.body") : t("suppliers.empty.body")}
              </EmptyState>
            }
          >
            {() => <SuppliersTable rows={rows} />}
          </DataState>
          <LoadMore list={suppliers} />
        </Panel>
      </PageBody>
    </>
  );
}

function Filter({ status }: { status: AdminSchemas["SupplierStatus"] | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={{ status }}
      onApply={(values) =>
        navigate({
          to: "/suppliers",
          search: { status: supplierStatus(values.status) },
        })
      }
    >
      <ChoiceFilter
        label={t("suppliers.col.status")}
        name="status"
        value={status}
        any={t("suppliers.anyStatus")}
        options={supplierStatuses.map((value) => ({ value, label: t(`suppliers.state.${value}`) }))}
      />
    </FilterBar>
  );
}

function SuppliersTable({ rows }: { rows: AdminSchemas["Supplier"][] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.suppliers")}>
      <thead>
        <tr>
          <Th>{t("suppliers.col.name")}</Th>
          <Th>{t("suppliers.col.status")}</Th>
          <Th className="hidden md:table-cell">{t("suppliers.col.balance")}</Th>
          <Th className="hidden lg:table-cell">{t("suppliers.col.unit")}</Th>
          <Th className="w-0" />
        </tr>
      </thead>
      <tbody>
        {rows.map((supplier) => (
          <tr key={supplier.id}>
            <Td className="w-full max-w-0 sm:w-auto sm:max-w-72 sm:min-w-40">
              <p className="truncate text-t1" title={supplier.name}>
                {supplier.name}
              </p>
              <p className="truncate text-label text-t4" title={supplier.origin}>
                <Code>{supplier.origin}</Code>
                {" · "}
                {supplier.kind}
              </p>
            </Td>
            <Td>
              <SupplierState supplier={supplier} />
            </Td>
            <Td className="hidden whitespace-nowrap tabular-nums md:table-cell">
              <NativeBalance supplier={supplier} />
            </Td>
            <Td className="hidden lg:table-cell">
              <UnitValue supplier={supplier} />
            </Td>
            <Td className="whitespace-nowrap">
              <Button variant="ghost" size="icon" asChild>
                <Link
                  to="/suppliers/$supplierId"
                  params={{ supplierId: supplier.id }}
                  aria-label={t("suppliers.open", { name: supplier.name })}
                >
                  <Icon name="right" />
                </Link>
              </Button>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
