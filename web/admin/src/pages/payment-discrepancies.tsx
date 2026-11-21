import type { AdminSchemas } from "@axiom/api";
import { ActionButton, Card, DataState, EmptyState, Icon, LoadMore, PageHead, Table, Td, Text } from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { discrepanciesQuery, discrepancyStates } from "../finance-queries";
import en from "../locales/en/payment-discrepancies";
import zh from "../locales/zh/payment-discrepancies";
import { ChoiceFilter, Code, FilterBar, Head, PageBody, State, Time } from "../parts";

export const messages = { zh: { discrepancies: zh }, en: { discrepancies: en } };

type Discrepancy = AdminSchemas["PaymentDiscrepancy"];

export default function PaymentDiscrepancies() {
  const { t } = useTranslation();
  const { state } = useSearch({ from: "/app/payment-discrepancies" });
  const navigate = useNavigate();
  const query = useInfiniteQuery(discrepanciesQuery(state));
  const rows = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <>
      <PageHead title={t("nav.discrepancies")} lede={t("discrepancies.description")} />
      <PageBody>
        <FilterBar
          values={{ state }}
          onApply={(values) =>
            navigate({
              to: "/payment-discrepancies",
              search: { state: discrepancyStates.find((item) => item === values.state) },
            })
          }
        >
          <ChoiceFilter
            name="state"
            label={t("discrepancies.status")}
            value={state}
            any={t("discrepancies.allStates")}
            options={discrepancyStates.map((value) => ({ value, label: t(`discrepancies.state.${value}`) }))}
          />
        </FilterBar>
        <Card layout="flush">
          <DataState
            query={query}
            isEmpty={() => rows.length === 0}
            empty={<EmptyState title={t("discrepancies.empty")} />}
          >
            {() => <Rows rows={rows} />}
          </DataState>
          <LoadMore list={query} />
        </Card>
      </PageBody>
    </>
  );
}

function Rows({ rows }: { rows: Discrepancy[] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("nav.discrepancies")}>
      <Head
        cols={[[t("discrepancies.kind"), { main: true }], t("discrepancies.status"), t("discrepancies.created")]}
        action
      />
      <tbody>
        {rows.map((row) =>
          row.id ? (
            <tr key={row.id}>
              <Td main>
                <Text medium>{row.kind}</Text>
                <Code>{row.order_id ?? row.id}</Code>
              </Td>
              <Td>
                {row.state ? (
                  <State tone={row.state === "open" ? "warn" : "good"}>{t(`discrepancies.state.${row.state}`)}</State>
                ) : (
                  "—"
                )}
              </Td>
              <Td>
                <Time at={row.created_at} />
              </Td>
              <Td actions>
                <ActionButton asChild size="sm" variant="gho">
                  <Link
                    to="/payment-discrepancies/$discrepancyId"
                    params={{ discrepancyId: row.id }}
                    aria-label={t("discrepancies.open")}
                  >
                    <Icon name="right" />
                  </Link>
                </ActionButton>
              </Td>
            </tr>
          ) : null,
        )}
      </tbody>
    </Table>
  );
}
