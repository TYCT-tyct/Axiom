import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, useErrors, useSession } from "@axiom/app";
import { Dialog } from "@axiom/boards/ds";
import {
  ActionButton,
  Card,
  DataState,
  EmptyState,
  Field,
  LoadMore,
  PageHead,
  Select,
  Stack,
  Table,
  Td,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "../change";
import { paymentCurrencies } from "../credit-read";
import { exchangeHistoryQuery, exchangeRatesQuery } from "../finance-queries";
import en from "../locales/en/exchange-rates";
import zh from "../locales/zh/exchange-rates";
import { Actions, Code, Head, PageBody, Time } from "../parts";

export const messages = { zh: { exchangeRates: zh }, en: { exchangeRates: en } };

type Rate = AdminSchemas["ExchangeRate"];
type Input = Omit<AdminSchemas["ExchangeRateInput"], "reason">;
type Pair = Pick<Rate, "base" | "quote">;
type Panel = { kind: "add" } | { kind: "edit" | "history"; pair: Pair };

const samePair = (a: Pair, b: Pair) => a.base === b.base && a.quote === b.quote;
const kinds = ["initial", "adjustment", "correction"] as const;
const pairName = ({ base, quote }: Pair) => `${base} / ${quote}`;

export default function ExchangeRates() {
  const { t } = useTranslation();
  const manage = can(useSession(), "pricing.manage");
  const query = useQuery(exchangeRatesQuery);
  const [panel, setPanel] = useState<Panel>();
  const close = () => setPanel(undefined);
  const current = panel?.kind === "edit" ? query.data?.data.find((rate) => samePair(rate, panel.pair)) : undefined;
  return (
    <>
      <PageHead title={t("nav.exchangeRates")} lede={t("exchangeRates.description")} />
      <PageBody>
        {manage && query.isSuccess ? (
          <Actions>
            <ActionButton variant="pri" icon="plus" onClick={() => setPanel({ kind: "add" })}>
              {t("exchangeRates.add")}
            </ActionButton>
          </Actions>
        ) : null}
        <Card layout="flush">
          <DataState
            query={query}
            isEmpty={(list) => list.data.length === 0}
            empty={<EmptyState title={t("exchangeRates.empty")} />}
          >
            {({ data }) => <Rates rows={data} manage={manage} open={setPanel} />}
          </DataState>
        </Card>
      </PageBody>
      {panel?.kind === "history" ? <History pair={panel.pair} onClose={close} /> : null}
      {panel?.kind === "add" ? <EditRate onClose={close} /> : null}
      {current ? <EditRate live={current} onClose={close} /> : null}
    </>
  );
}

function Rates({ rows, manage, open }: { rows: Rate[]; manage: boolean; open: (panel: Panel) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  return (
    <Table label={t("nav.exchangeRates")}>
      <Head
        cols={[
          [t("exchangeRates.pair"), { main: true }],
          t("exchangeRates.rate"),
          t("exchangeRates.version"),
          t("exchangeRates.source"),
        ]}
        action
      />
      <tbody>
        {rows.map((row) => (
          <tr key={row.id}>
            <Td main>
              <Code>{pairName(row)}</Code>
            </Td>
            <Td>
              <RateValue rate={row} />
            </Td>
            <Td>{format.number(row.version)}</Td>
            <Td>
              {row.source === "manual" || row.source === "ecb" ? t(`exchangeRates.sources.${row.source}`) : row.source}
            </Td>
            <Td actions>
              <ActionButton size="sm" onClick={() => open({ kind: "history", pair: row })}>
                {t("exchangeRates.history")}
              </ActionButton>
              {manage ? (
                <ActionButton size="sm" onClick={() => open({ kind: "edit", pair: row })}>
                  {t("exchangeRates.edit")}
                </ActionButton>
              ) : null}
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function RateValue({ rate }: { rate: Rate }) {
  const { t } = useTranslation();
  return <Code>{t("exchangeRates.rateValue", rate)}</Code>;
}

function History({ pair, onClose }: { pair: Pair; onClose: () => void }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery(exchangeHistoryQuery({ base: pair.base, quote: pair.quote }));
  const rows = query.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <Dialog
      title={t("exchangeRates.historyTitle", { pair: pairName(pair) })}
      closeLabel={t("dialog.close", { ns: "common" })}
      onClose={onClose}
      primary={{ label: t("change.close"), onClick: onClose }}
    >
      <DataState
        query={query}
        isEmpty={() => rows.length === 0}
        empty={<EmptyState title={t("exchangeRates.emptyHistory")} />}
      >
        {() => <HistoryTable rows={rows} />}
      </DataState>
      <LoadMore list={query} />
    </Dialog>
  );
}

function HistoryTable({ rows }: { rows: Rate[] }) {
  const { t } = useTranslation();
  return (
    <Table label={t("exchangeRates.history")}>
      <Head cols={[[t("exchangeRates.version"), { main: true }], t("exchangeRates.rate"), t("exchangeRates.change")]} />
      <tbody>
        {rows.map((row) => {
          const kind = kinds.find((candidate) => candidate === row.kind);
          return (
            <tr key={row.id}>
              <Td main>
                <Text medium>{row.version}</Text>
                <Time at={row.created_at} />
              </Td>
              <Td>
                <RateValue rate={row} />
              </Td>
              <Td>
                <Text medium>{kind ? t(`exchangeRates.kinds.${kind}`) : row.kind}</Text>
                <Text size="sm" tone="faint">
                  {row.created_by}
                </Text>
                <Text breaks>{row.reason}</Text>
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

function validTerm(raw: string | undefined): raw is string {
  if (!raw || !/^\d{1,19}$/.test(raw)) return false;
  const term = BigInt(raw);
  return term > 0n && term <= 10n ** 18n;
}

function EditRate({ live, onClose }: { live?: Rate; onClose: () => void }) {
  const { t } = useTranslation();
  const [rate, reload] = useSnapshot(live);
  const label = t(rate ? "exchangeRates.edit" : "exchangeRates.add");
  return (
    <ChangeDialog
      operation={`exchange-rate:${rate ? pairName(rate) : "add"}`}
      title={label}
      action={t("exchangeRates.save")}
      intro={
        <p>
          {rate
            ? t("exchangeRates.editIntro", { pair: pairName(rate), version: rate.version })
            : t("exchangeRates.addIntro")}
        </p>
      }
      fields={(error) => <RateFields rate={rate} error={error} />}
      read={(values): Input | Problem => {
        const base = rate?.base ?? values.base;
        const quote = rate?.quote ?? values.quote;
        const numerator = values.numerator?.trim();
        const denominator = values.denominator?.trim();
        if (!base || !quote || base === quote || !validTerm(numerator) || !validTerm(denominator)) {
          return { problem: t("exchangeRates.invalid") };
        }
        const kind = !rate ? "initial" : values.kind === "correction" ? "correction" : "adjustment";
        return { base, quote, numerator, denominator, kind, expected_version: rate?.version ?? 0 };
      }}
      send={(body, { key, reason }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/pricing/exchange-rates", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...body, reason },
          }),
        )
      }
      done={(next) => <RateValue rate={next} />}
      reload={rate ? reload : undefined}
      onClose={onClose}
    />
  );
}

const currencies = paymentCurrencies.map((value) => ({ value, label: value }));

function RateFields({ rate, error }: { rate?: Rate; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const options = [{ value: "", label: t("exchangeRates.chooseCurrency") }, ...currencies];
  return (
    <Stack>
      {!rate ? (
        <>
          <Select name="base" label={t("exchangeRates.base")} options={options} error={errors.field(error, "base")} />
          <Select
            name="quote"
            label={t("exchangeRates.quote")}
            options={options}
            error={errors.field(error, "quote")}
          />
        </>
      ) : null}
      <Field
        name="numerator"
        label={t("exchangeRates.numerator")}
        inputMode="numeric"
        maxLength={19}
        defaultValue={rate?.numerator}
        error={errors.field(error, "numerator")}
      />
      <Field
        name="denominator"
        label={t("exchangeRates.denominator")}
        inputMode="numeric"
        maxLength={19}
        defaultValue={rate?.denominator}
        error={errors.field(error, "denominator")}
      />
      {rate ? (
        <Select
          name="kind"
          label={t("exchangeRates.change")}
          hint={t("exchangeRates.kindHint")}
          defaultValue="adjustment"
          options={kinds.slice(1).map((value) => ({ value, label: t(`exchangeRates.kinds.${value}`) }))}
          error={errors.field(error, "kind")}
        />
      ) : null}
    </Stack>
  );
}
