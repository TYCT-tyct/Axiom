import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { LoadMore, useErrors, useSession } from "@axiom/app";
import {
  Button,
  DataState,
  EmptyState,
  PageHeader,
  Panel,
  Table,
  Td,
  TextAreaField,
  TextField,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { ChoiceFilter, Code, FilterBar, Head, mainCell, PageBody, State, Time, text } from "../parts";
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
      <PageHeader title={t("nav.redeemCodes")} description={t("redeem.description")} />
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
      <Panel
        title={t("redeem.codes")}
        action={
          <Button variant="primary" size="md" onClick={() => setGenerating(true)}>
            {t("redeem.generate")}
          </Button>
        }
      >
        <Codes siteId={siteId} filters={filters} />
      </Panel>
      {generating ? <GenerateDialog siteId={siteId} currency={currency} onClose={() => setGenerating(false)} /> : null}
    </>
  );
}

function Filters({ filters }: { filters: FiltersValue }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <FilterBar
      values={filters}
      onApply={(values) =>
        navigate({ to: "/redeem-codes", search: { state: redeemState(values.state), note: text(values.note) } })
      }
    >
      <ChoiceFilter
        label={t("redeem.filters.state")}
        name="state"
        value={filters.state}
        any={t("redeem.filters.stateAny")}
        options={redeemStates.map((state) => ({ value: state, label: t(`redeem.state.${state}`) }))}
      />
      <TextField
        label={t("redeem.filters.note")}
        hint={t("redeem.filters.noteHint")}
        name="note"
        defaultValue={filters.note}
        maxLength={200}
        autoComplete="off"
      />
    </FilterBar>
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
                [t("redeem.col.code")],
                [t("redeem.col.amount"), "text-right"],
                [t("redeem.col.note"), "hidden lg:table-cell"],
                [t("redeem.col.state")],
                [t("redeem.col.created"), "hidden sm:table-cell"],
                [t("redeem.col.expires"), "hidden md:table-cell"],
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
    <tr className="align-top hover:bg-s2">
      <Td className={mainCell}>
        <Code title={code.id}>
          {"…"}
          {code.hint}
        </Code>
        <span className="block text-label text-t3">
          {code.id.slice(0, 12)}
          {"…"}
        </span>
      </Td>
      <Td className="whitespace-nowrap text-right tabular-nums">{format.money(code.amount)}</Td>
      <Td className="hidden max-w-64 lg:table-cell">
        <span className="block truncate" title={code.note}>
          {code.note || "—"}
        </span>
      </Td>
      <Td>
        <State tone={tone}>{t(`redeem.state.${code.state}`)}</State>
        {code.redeemed_at ? (
          <span className="mt-1 block text-label text-t3">{format.dateTime(new Date(code.redeemed_at))}</span>
        ) : null}
      </Td>
      <Td className="hidden whitespace-nowrap sm:table-cell">
        <Time at={code.created_at} />
      </Td>
      <Td className="hidden whitespace-nowrap md:table-cell">
        <Time at={code.expires_at} />
      </Td>
      <Td className="text-right">
        {code.state === "enabled" ? (
          <Button size="sm" onClick={() => setDisabling(true)}>
            {t("redeem.disable")}
          </Button>
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
      done={() => <p className="text-t2">{t("redeem.disabled")}</p>}
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
      <TextField
        label={t("redeem.amount", { currency })}
        hint={t("redeem.amountHint")}
        name="amount"
        inputMode="decimal"
        autoComplete="off"
        error={errors.field(error, "amount")}
      />
      <TextField
        label={t("redeem.count")}
        hint={t("redeem.countHint")}
        name="count"
        inputMode="numeric"
        autoComplete="off"
        error={errors.field(error, "count")}
      />
      <TextField
        label={t("redeem.note")}
        hint={t("redeem.noteHint")}
        name="note"
        maxLength={200}
        autoComplete="off"
        error={errors.field(error, "note")}
      />
      <TextField
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
    <div className="space-y-4">
      {codes.length === 0 ? (
        <p className="text-t2">{t("redeem.issuedGone")}</p>
      ) : (
        <p className="text-t2">{t("redeem.issuedIntro", { count: codes.length })}</p>
      )}
      {codes.length > 0 ? (
        <TextAreaField
          label={t("redeem.issuedLabel")}
          hint={t("redeem.issuedHint")}
          readOnly
          rows={Math.min(codes.length + 1, 12)}
          value={codes.join("\n")}
          onFocus={(event) => event.currentTarget.select()}
        />
      ) : null}
      {list.data?.[0] ? (
        <p className="text-t2">{t("redeem.issuedAmount", { amount: format.money(list.data[0].amount) })}</p>
      ) : null}
    </div>
  );
}
