import type { AdminSchemas } from "@axiom/api";
import { LoadMore } from "@axiom/app";
import { ActionButton, Card, DataState, EmptyState, Icon, PageHead, Table, Td, Text } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Actions, ChoiceFilter, Code, FilterBar, Head, NewButton, PageBody } from "../parts";
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
      <PageHead title={t("nav.suppliers")} lede={t("suppliers.description")} />
      <PageBody>
        <Actions>
          <NewButton permission="suppliers.manage" label={t("suppliers.connect.open")}>
            {(close) => <ConnectDialog onClose={close} />}
          </NewButton>
        </Actions>
        <Filter status={status} />
        <Card layout="flush">
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
        </Card>
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
      <Head
        cols={[
          [t("suppliers.col.name"), { main: true }],
          t("suppliers.col.status"),
          t("suppliers.col.balance"),
          t("suppliers.col.unit"),
        ]}
        action
      />
      <tbody>
        {rows.map((supplier) => (
          <tr key={supplier.id}>
            <Td main>
              <Text>{supplier.name}</Text>
              <Text size="sm" tone="faint">
                <Code>{supplier.origin}</Code>
                {" · "}
                {supplier.kind}
              </Text>
            </Td>
            <Td main>
              <SupplierState supplier={supplier} brief />
            </Td>
            <Td>
              <NativeBalance supplier={supplier} />
            </Td>
            <Td>
              <UnitValue supplier={supplier} />
            </Td>
            <Td shrink>
              <ActionButton variant="gho" size="sm" asChild>
                <Link
                  to="/suppliers/$supplierId"
                  params={{ supplierId: supplier.id }}
                  aria-label={t("suppliers.open", { name: supplier.name })}
                >
                  <Icon name="right" />
                </Link>
              </ActionButton>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

export { default as messages } from "../locales/suppliers";
