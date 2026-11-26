import type { AdminSchemas } from "@axiom/api";
import { useSession } from "@axiom/app";
import {
  ActionButton,
  DataState,
  DrawerFacts,
  Figure,
  MetricStrip,
  PageBody,
  PageHead,
  type StripItem,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { type Columns, type Format, ListTable } from "../list-table";
import enRedeem from "../locales/en/redeem";
import zhRedeem from "../locales/zh/redeem";
import { Code, NewButton, State, SubLine } from "../parts";
import { accountQuery, redeemCodesQuery } from "../queries";
import { redeemState, redeemStates } from "../redeem";
import { DisableDialog, GenerateDialog } from "../redeem-dialogs";
import { RowDrawer, useAddressParam, useSelected } from "../row-drawer";

type RedeemCode = AdminSchemas["RedeemCode"];
type Filter = "all" | AdminSchemas["RedeemCodeState"];
const day = 24 * 60 * 60 * 1000;

const tone = (state: RedeemCode["state"]) => (state === "enabled" ? "neutral" : state === "used" ? "good" : "warn");

// Whole days to when a code stops being redeemable, counted up; nothing for a code that never expires.
const daysLeft = (code: RedeemCode, now: number) =>
  code.expires_at === undefined ? undefined : Math.ceil((Date.parse(code.expires_at) - now) / day);

function codeColumns(t: TFunction, format: Format, now: number): Columns<RedeemCode> {
  return [
    {
      key: "code",
      header: t("redeem.col.code"),
      cell: (code) => (
        <>
          <Code title={code.id}>
            {"…"}
            {code.hint}
          </Code>
          <SubLine>{code.note || "—"}</SubLine>
        </>
      ),
    },
    {
      key: "amount",
      header: t("redeem.col.amount"),
      align: "end",
      cell: (code) => format.money(code.amount),
      sort: (code) => Number(BigInt(code.amount.amount) / 1_000_000n),
    },
    {
      key: "state",
      header: t("redeem.col.state"),
      cell: (code) => (
        <>
          <State tone={tone(code.state)}>{t(`redeem.state.${code.state}`)}</State>
          {code.redeemed_at ? <SubLine>{format.dateTime(new Date(code.redeemed_at))}</SubLine> : null}
        </>
      ),
    },
    {
      key: "created",
      header: t("redeem.col.created"),
      muted: true,
      cell: (code) => format.dateTime(new Date(code.created_at)),
      sort: (code) => Date.parse(code.created_at),
    },
    {
      key: "expires",
      header: t("redeem.col.expires"),
      muted: true,
      cell: (code) => <Expiry code={code} now={now} />,
      sort: (code) => (code.expires_at ? Date.parse(code.expires_at) : null),
    },
  ];
}

// The four figures above the codes, counted from the codes the list gave: those still redeemable, those redeemed and the
// credit they were worth, and those that stop within 30 days. The API gives no totals, so when there are more pages than
// have been loaded each figure says it counts what is loaded, and the share of all codes that were redeemed is left out.
export function redeemFigures(codes: readonly RedeemCode[], now: number) {
  const used = codes.filter((code) => code.state === "used");
  const enabled = codes.filter((code) => code.state === "enabled");
  return {
    enabled: enabled.length,
    used: used.length,
    redeemed: used.reduce((sum, code) => sum + BigInt(code.amount.amount), 0n).toString(),
    soon: enabled.filter((code) => (daysLeft(code, now) ?? Number.POSITIVE_INFINITY) <= 30).length,
  };
}

// 兑换码: the codes of the site and what became of them, a drawer for one, and the batch that makes more. A code is shown
// once, in the answer that made it; the list and the drawer have only its last four characters.
export default function RedeemCodes() {
  const owner = useQuery(accountQuery(useSession().account));
  return (
    <DataState query={owner} page>
      {(account) => <Current siteId={account.site_id} currency={account.balance.balance.currency} />}
    </DataState>
  );
}

function Current({ siteId, currency }: { siteId: AdminSchemas["SiteId"]; currency: string }) {
  const { t } = useTranslation();
  const format = useFormat();
  const navigate = useNavigate();
  const { state, note } = useSearch({ from: "/app/redeem-codes" });
  const [selected, select] = useSelected();
  const [now] = useState(() => Date.now());
  const { text, setText } = useAddressParam("note");
  const query = useInfiniteQuery(redeemCodesQuery(siteId, { state, note }));
  const rows = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const open = rows.find((code) => code.id === selected);
  return (
    <>
      <PageHead
        title={t("shell.nav.redeem")}
        lede={t("redeem.description")}
        actions={
          <NewButton permission="sites.manage" label={t("redeem.generate")}>
            {(close) => <GenerateDialog siteId={siteId} currency={currency} onClose={close} />}
          </NewButton>
        }
      />
      <PageBody>
        <Strip rows={rows} more={Boolean(query.hasNextPage)} now={now} />
        <ListTable<RedeemCode, Filter>
          label={t("redeem.codes")}
          unit={t("redeem.unit")}
          search={{ label: t("redeem.search"), value: text, onChange: setText }}
          filter={{
            label: t("redeem.filters.state"),
            value: state ?? "all",
            options: [
              { value: "all", label: t("redeem.filters.stateAny") },
              ...redeemStates.map((value) => ({ value, label: t(`redeem.state.${value}`) })),
            ],
            onChange: (value) =>
              void navigate({ to: "/redeem-codes", search: { state: redeemState(value), note }, replace: true }),
          }}
          query={query}
          rows={rows}
          loaded={rows.length}
          rowKey={(code) => code.id}
          selected={selected}
          onOpen={(code) => select(code.id)}
          narrowed={Boolean(state || note)}
          empty={{ title: t("redeem.empty.title"), body: t("redeem.empty.body") }}
          columns={codeColumns(t, format, now)}
        />
      </PageBody>
      {open ? <CodeDrawer code={open} keys={rows.map((code) => code.id)} select={select} now={now} /> : null}
    </>
  );
}

function Strip({ rows, more, now }: { rows: readonly RedeemCode[]; more: boolean; now: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const figures = redeemFigures(rows, now);
  const currency = rows[0]?.amount.currency;
  if (rows.length === 0) return null;
  const counted = more
    ? t("redeem.strip.loaded", { count: rows.length })
    : t("redeem.strip.all", { count: rows.length });
  const items: StripItem[] = [
    {
      key: "enabled",
      label: t("redeem.strip.enabled"),
      value: format.number(figures.enabled),
      suffix: ` ${t("redeem.unit")}`,
      note: counted,
    },
    {
      key: "used",
      label: t("redeem.strip.used"),
      value: format.number(figures.used),
      suffix: ` ${t("redeem.unit")}`,
      note: counted,
    },
    ...(currency
      ? [
          {
            key: "redeemed",
            label: t("redeem.strip.redeemed"),
            value: format.money({ amount: figures.redeemed, currency }),
            note: counted,
          },
        ]
      : []),
    {
      key: "soon",
      label: t("redeem.strip.soon"),
      value: format.number(figures.soon),
      suffix: ` ${t("redeem.unit")}`,
      note: t("redeem.strip.soonNote"),
    },
  ];
  return <MetricStrip label={t("redeem.strip.label")} items={items} />;
}

function Expiry({ code, now }: { code: RedeemCode; now: number }) {
  const { t } = useTranslation();
  const format = useFormat();
  const left = daysLeft(code, now);
  if (left === undefined) return t("redeem.never");
  return (
    <>
      {format.dayFull(new Date(code.expires_at ?? ""))}
      {code.state === "enabled" ? (
        <SubLine>
          {left < 0 ? (
            t("redeem.expired")
          ) : (
            <Text as="span" size="sm" tone={left <= 30 ? "warn" : "faint"}>
              {t("redeem.daysLeft", { count: left })}
            </Text>
          )}
        </SubLine>
      ) : null}
    </>
  );
}

// One code in a drawer: what it is worth and where it stands, with the plain statement that the code itself was shown
// once, when it was made, and is not here. A code that is still enabled can be stopped, which cannot be undone.
function CodeDrawer({
  code,
  keys,
  select,
  now,
}: {
  code: RedeemCode;
  keys: readonly string[];
  select: ReturnType<typeof useSelected>[1];
  now: number;
}) {
  const { t } = useTranslation();
  const format = useFormat();
  const [disabling, setDisabling] = useState(false);
  return (
    <RowDrawer
      title={`…${code.hint}`}
      subtitle={code.note || undefined}
      keys={keys}
      selected={code.id}
      select={select}
      footer={
        code.state === "enabled" ? (
          <>
            <ActionButton variant="gho" onClick={() => setDisabling(true)}>
              {t("redeem.disable")}
            </ActionButton>
            {disabling ? <DisableDialog code={code} onClose={() => setDisabling(false)} /> : null}
          </>
        ) : undefined
      }
    >
      <Figure
        value={format.money(code.amount)}
        caption={<State tone={tone(code.state)}>{t(`redeem.state.${code.state}`)}</State>}
      />
      <DrawerFacts
        rows={[
          [t("redeem.col.note"), code.note || "—"],
          [t("redeem.col.created"), format.dateTime(new Date(code.created_at))],
          [t("redeem.col.expires"), <Expiry key="expires" code={code} now={now} />],
          ...(code.redeemed_at ? [[t("redeem.redeemedAt"), format.dateTime(new Date(code.redeemed_at))] as const] : []),
          [t("redeem.drawer.id"), <Code key="id">{code.id}</Code>],
          [t("redeem.drawer.createdBy"), <Code key="by">{code.created_by}</Code>],
        ]}
      />
      <Text size="sm" tone="muted">
        {t("redeem.drawer.once")}
      </Text>
    </RowDrawer>
  );
}

export const messages = { zh: { redeem: zhRedeem }, en: { redeem: enRedeem } };
