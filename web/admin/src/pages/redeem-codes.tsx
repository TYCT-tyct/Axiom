import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useErrors, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  DataState,
  EmptyState,
  Field,
  PageHead,
  Stack,
  Table,
  Td,
  Text,
  TextArea,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { ChoiceFilter, Code, FilterBar, Head, PageBody, State, Time, text } from "../parts";
import { accountQuery, redeemCodesQuery } from "../queries";
import { readBatch, redeemState, redeemStates } from "../redeem";

type RedeemCode = AdminSchemas["RedeemCode"];

type FiltersValue = {
  state?: AdminSchemas["RedeemCodeState"];
  note?: string;
};

// The admin's redeem codes: a batch is generated for the site with its amount, count, expiry and note, and the codes are
// shown in that answer only. The list never carries a code, just its last four characters; an unused code can be
// disabled, which keeps it from being redeemed.
export default function RedeemCodes() {
  const { t } = useTranslation();
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <>
      <PageHead title={t("nav.redeemCodes")} lede={t("redeem.description")} />
      <PageBody>
        <DataState query={owner}>
          {(account) => <Current siteId={account.site_id} currency={account.balance.balance.currency} />}
        </DataState>
      </PageBody>
    </>
  );
}

function Current({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const { t } = useTranslation();
  const filters = useSearch({ from: "/app/redeem-codes" });
  const [generating, setGenerating] = useState(false);
  return (
    <>
      <Filters filters={filters} />
      <Card
        title={t("redeem.codes")}
        layout="flush"
        actions={
          <ActionButton variant="pri" onClick={() => setGenerating(true)}>
            {t("redeem.generate")}
          </ActionButton>
        }
      >
        <Codes siteId={siteId} filters={filters} />
      </Card>
      {generating ? <GenerateDialog siteId={siteId} currency={currency} onClose={() => setGenerating(false)} /> : null}
    </>
  );
}

// redeemSearch is what the address keeps of the fields: anything not chosen does not narrow.
function redeemSearch(values: Record<string, string>): FiltersValue {
  return { state: redeemState(values.state), note: text(values.note) };
}

function Filters({ filters }: { filters: FiltersValue }) {
  const navigate = useNavigate();
  return (
    <FilterBar values={filters} onApply={(values) => navigate({ to: "/redeem-codes", search: redeemSearch(values) })}>
      <RedeemFields filters={filters} />
    </FilterBar>
  );
}

function RedeemFields({ filters }: { filters: FiltersValue }) {
  const { t } = useTranslation();
  return (
    <>
      <ChoiceFilter
        label={t("redeem.filters.state")}
        name="state"
        value={filters.state}
        any={t("redeem.filters.stateAny")}
        options={redeemStates.map((state) => ({ value: state, label: t(`redeem.state.${state}`) }))}
      />
      <Field
        label={t("redeem.filters.note")}
        hint={t("redeem.filters.noteHint")}
        name="note"
        defaultValue={filters.note}
        maxLength={200}
        autoComplete="off"
      />
    </>
  );
}

function Codes({ siteId, filters }: { siteId: AdminSchemas["SiteId"]; filters: FiltersValue }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery(redeemCodesQuery(siteId, filters));
  const rows = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const narrowed = Object.values(filters).some(Boolean);
  return (
    <>
      <DataState
        query={query}
        isEmpty={() => rows.length === 0}
        empty={
          <EmptyState title={narrowed ? t("redeem.noMatch.title") : t("redeem.empty.title")}>
            {narrowed ? t("redeem.noMatch.body") : t("redeem.empty.body")}
          </EmptyState>
        }
      >
        {() => (
          <Table label={t("redeem.codes")}>
            <Head
              cols={[
                t("redeem.col.code"),
                [t("redeem.col.amount"), { align: "end" }],
                [t("redeem.col.note"), { main: true }],
                t("redeem.col.state"),
                t("redeem.col.created"),
                t("redeem.col.expires"),
              ]}
              action
            />
            <tbody>
              {rows.map((code) => (
                <Row key={code.id} code={code} />
              ))}
            </tbody>
          </Table>
        )}
      </DataState>
      <LoadMore list={query} />
    </>
  );
}

function Row({ code }: { code: RedeemCode }) {
  const { t } = useTranslation();
  const format = useFormat();
  const [disabling, setDisabling] = useState(false);
  const tone = code.state === "enabled" ? "neutral" : code.state === "used" ? "good" : "warn";
  return (
    <tr>
      <Td>
        <Code title={code.id}>
          {"…"}
          {code.hint}
        </Code>
        <Text as="div" size="sm" tone="faint">
          {code.id.slice(0, 12)}
          {"…"}
        </Text>
      </Td>
      <Td align="end">{format.money(code.amount)}</Td>
      <Td main>{code.note || "—"}</Td>
      <Td>
        <State tone={tone}>{t(`redeem.state.${code.state}`)}</State>
        {code.redeemed_at ? (
          <Text as="div" size="sm" tone="faint">
            {format.dateTime(new Date(code.redeemed_at))}
          </Text>
        ) : null}
      </Td>
      <Td>
        <Time at={code.created_at} />
      </Td>
      <Td>
        <Time at={code.expires_at} />
      </Td>
      <Td align="end" shrink>
        {code.state === "enabled" ? (
          <ActionButton size="sm" onClick={() => setDisabling(true)}>
            {t("redeem.disable")}
          </ActionButton>
        ) : null}
        {disabling ? <DisableDialog code={code} onClose={() => setDisabling(false)} /> : null}
      </Td>
    </tr>
  );
}

// DisableDialog stops one code; the key makes a retry the same disable, and the reason goes to the audit log.
function DisableDialog({ code, onClose }: { code: RedeemCode; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`disable-redeem-code:${code.id}`}
      title={t("redeem.disableTitle", { hint: code.hint })}
      intro={<p>{t("redeem.disableIntro")}</p>}
      action={t("redeem.disable")}
      read={() => ({})}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/redeem-codes/{code_id}/disable", {
            params: { path: { code_id: code.id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => <Text tone="muted">{t("redeem.disabled")}</Text>}
      onClose={onClose}
    />
  );
}

// GenerateDialog makes one batch. The answer carries the codes; they are not read back by any later request, so the
// result is the only place they are shown.
function GenerateDialog({
  siteId,
  currency,
  onClose,
}: {
  siteId: AdminSchemas["SiteId"];
  currency: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const words = {
    amountInvalid: t("redeem.amountInvalid"),
    countInvalid: t("redeem.countInvalid"),
    noteInvalid: t("redeem.noteInvalid"),
    expiresInvalid: t("redeem.expiresInvalid"),
  };
  return (
    <ChangeDialog
      operation={`create-redeem-codes:${siteId}`}
      title={t("redeem.generateTitle")}
      intro={<p>{t("redeem.generateIntro")}</p>}
      action={t("redeem.generate")}
      fields={(error) => <BatchFields currency={currency} error={error} />}
      read={(values) => readBatch(values, siteId, currency, words)}
      send={(batch, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/payments/redeem-codes", {
            params: { header: { "Idempotency-Key": key } },
            body: { ...batch, reason },
          }),
        )
      }
      done={(list) => <Issued list={list} />}
      onClose={onClose}
    />
  );
}

function BatchFields({ currency, error }: { currency: string; error: unknown }) {
  const { t } = useTranslation();
  const errors = useErrors();
  return (
    <>
      <Field
        label={t("redeem.amount", { currency })}
        hint={t("redeem.amountHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "amount")}
      />
      <Field
        label={t("redeem.count")}
        hint={t("redeem.countHint")}
        name="count"
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "count")}
      />
      <Field
        label={t("redeem.note")}
        hint={t("redeem.noteHint")}
        name="note"
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "note")}
      />
      <Field
        label={t("redeem.expires")}
        hint={t("redeem.expiresHint")}
        name="expires_at"
        type="datetime-local"
        error={errors.field(error, "expires_at")}
      />
    </>
  );
}

// Issued shows a batch's codes the one time they are known, as text a person can select and keep.
function Issued({ list }: { list: AdminSchemas["RedeemCodeList"] }) {
  const { t } = useTranslation();
  const format = useFormat();
  const codes = (list.data ?? []).flatMap((code) => (code.code ? [code.code] : []));
  return (
    <Stack>
      {codes.length === 0 ? (
        <Text tone="muted">{t("redeem.issuedGone")}</Text>
      ) : (
        <Text tone="muted">{t("redeem.issuedIntro", { count: codes.length })}</Text>
      )}
      {codes.length > 0 ? (
        <TextArea
          label={t("redeem.issuedLabel")}
          hint={t("redeem.issuedHint")}
          readOnly
          rows={Math.min(codes.length + 1, 12)}
          value={codes.join("\n")}
          onFocus={(event) => event.currentTarget.select()}
        />
      ) : null}
      {list.data?.[0] ? (
        <Text tone="muted">{t("redeem.issuedAmount", { amount: format.money(list.data[0].amount) })}</Text>
      ) : null}
    </Stack>
  );
}
