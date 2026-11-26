import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  EmptyState,
  LinkButton,
  RowList,
  SegmentedControl,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery, useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactElement, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog } from "../change";
import { type SupplierView, supplierQuery } from "../queries";
import { basisPoints, type Category, categories, categoryOf, mismatch, toneOf } from "./inbox";
import { MapDialog, type Unmatched } from "./map-dialog";
import { BuiltState } from "./parts";
import { inboxQuery } from "./queries";
import { credentialName } from "./upstream";

type Item = AdminSchemas["SupplierInboxItem"];

// 待处理 (adPg_suppliers' inbox): what wants a decision, from every channel, in one queue, filtered by kind. Each item
// has the one action that deals with it, and 忽略, which hides it until a later sync finds it still there; the queue
// is read again after either, so its count follows.
export default function InboxTab() {
  const { t } = useTranslation();
  const inbox = useInfiniteQuery(inboxQuery);
  const [kind, setKind] = useState<"all" | Category>("all");
  const items = inbox.data?.pages.flatMap((page) => page.data) ?? [];
  const shown = kind === "all" ? items : items.filter((item) => categoryOf[item.kind] === kind);
  // The dialogs sit above the list: the last item dealt with empties it, and its dialog still says how it went.
  const [open, setOpen] = useState<Open | null>(null);
  return (
    <>
      <BuiltState
        query={inbox}
        isEmpty={() => items.length === 0}
        empty={<EmptyState title={t("upstream.inbox.empty.title")}>{t("upstream.inbox.empty.body")}</EmptyState>}
      >
        {() => (
          <>
            <Cluster justify="between">
              <SegmentedControl
                label={t("upstream.inbox.filter")}
                value={kind}
                options={(["all", ...categories] as const).map((value) => ({
                  value,
                  label: t(`upstream.inbox.kinds.${value}`),
                }))}
                onChange={setKind}
              />
              <Text as="span" size="sm" tone="faint" numeric>
                {t("upstream.inbox.count", { count: shown.length })}
              </Text>
            </Cluster>
            <Card layout="rows">
              {shown.length > 0 ? (
                <Items items={shown} onOpen={setOpen} />
              ) : (
                <EmptyState compact title={t("upstream.inbox.noneOfKind.title")}>
                  {t("upstream.inbox.noneOfKind.body")}
                </EmptyState>
              )}
            </Card>
            <LoadMore list={inbox} />
          </>
        )}
      </BuiltState>
      {open?.kind === "ignore" ? <IgnoreDialog item={open.item} onClose={() => setOpen(null)} /> : null}
      {open?.kind === "map" ? <MapDialog unmatched={open.unmatched} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

type Open = { kind: "ignore"; item: Item } | { kind: "map"; unmatched: Unmatched };

function Items({ items, onOpen: setOpen }: { items: Item[]; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  const format = useFormat();
  const manage = can(useSession(), "suppliers.manage");
  const details = useDetails(items);
  return (
    <RowList
      label={t("upstream.tabs.inbox")}
      alignTop
      items={items.map((item) => {
        const words = wordsOf(t, format, item, details.get(item.supplier_id));
        return {
          key: item.id,
          tone: toneOf[item.kind],
          title: words.title,
          sub: `${words.body}。${words.supplier}，${format.dateTimeMinute(new Date(item.last_seen_at))}`,
          end: (
            <Cluster gap={12}>
              <Act
                item={item}
                detail={details.get(item.supplier_id)}
                manage={manage}
                onMap={(unmatched) => setOpen({ kind: "map", unmatched })}
              />
              {manage ? (
                <LinkButton onClick={() => setOpen({ kind: "ignore", item })}>{t("upstream.inbox.ignore")}</LinkButton>
              ) : null}
            </Cluster>
          ),
        };
      })}
    />
  );
}

// The suppliers the items are of, as their pages read them (the same queries, so already in hand when the lines were).
function useDetails(items: readonly Item[]) {
  const ids = [...new Set(items.map((item) => item.supplier_id))];
  const details = useQueries({ queries: ids.map((id) => supplierQuery(id)) });
  return new Map(details.flatMap((detail) => (detail.data ? [[detail.data.supplier.id, detail.data] as const] : [])));
}

type T = TFunction<"admin">;
type Format = ReturnType<typeof useFormat>;

// An item in words (adInbox): what happened, what it means, and the channel it happened at.
function wordsOf(t: T, format: Format, item: Item, detail: SupplierView | undefined) {
  const name = detail?.supplier.name ?? t("upstream.inbox.unknownSupplier");
  const group = detail?.groups.find((g) => g.id === item.group_id);
  const credential = detail?.credentials.find((c) => c.id === item.credential_id);
  const decimal = (value: string | undefined) => (value === undefined ? "—" : format.decimal(value));
  const bp = mismatch(item.estimated_native, item.charged_native);
  const values = {
    name,
    group: group?.name ?? item.group_id?.slice(0, 8) ?? "",
    days: item.balance_days ?? 0,
    balance: decimal(item.balance_native),
    from: decimal(item.previous_ratio),
    to: decimal(item.ratio),
    ratio: decimal(item.ratio),
    percent: bp === undefined ? "—" : basisPoints(bp),
    date: item.date ?? "",
    estimated: decimal(item.estimated_native),
    charged: decimal(item.charged_native),
    model: item.upstream_name ?? "",
    calls: format.number(item.calls ?? 0),
    accounts: format.number(item.accounts ?? 0),
    key: credential ? credentialName(credential) : "",
  };
  return {
    title: t(`upstream.inbox.item.${item.kind}.title`, values),
    body: t(`upstream.inbox.item.${item.kind}.body`, values),
    supplier: name,
  };
}

// The one thing that deals with an item: where it is decided, or, for a name no model matches, the mapping itself.
function Act({
  item,
  detail,
  manage,
  onMap,
}: {
  item: Item;
  detail: SupplierView | undefined;
  manage: boolean;
  onMap: (unmatched: Unmatched) => void;
}) {
  const { t } = useTranslation();
  const label = t(`upstream.inbox.item.${item.kind}.act`);
  const supplierId = item.supplier_id;
  const go = (link: ReactElement) => (
    <ActionButton size="sm" asChild>
      {link}
    </ActionButton>
  );
  switch (item.kind) {
    case "unknown_model":
      return manage && item.group_id && item.upstream_name ? (
        <ActionButton
          size="sm"
          onClick={() =>
            onMap({
              supplierId,
              groupId: item.group_id ?? "",
              group: detail?.groups.find((group) => group.id === item.group_id)?.name,
              upstreamName: item.upstream_name ?? "",
            })
          }
        >
          {label}
        </ActionButton>
      ) : null;
    case "ratio_changed":
      return go(<Link to="/pricing">{label}</Link>);
    case "key_invalid":
      return go(
        <Link to="/suppliers" search={{ tab: "keys", credential: item.credential_id }}>
          {label}
        </Link>,
      );
    default:
      return go(
        <Link to="/suppliers/$supplierId" params={{ supplierId }} search={{ tab: tabOf[item.kind] }}>
          {label}
        </Link>,
      );
  }
}

const tabOf = {
  low_balance: undefined,
  cost_mismatch: "sync",
  group_added: undefined,
  unlisted_model_called: "models",
} as const;

// Ignoring hides the item until a later look finds the condition still there; it has no inverse to offer.
function IgnoreDialog({ item, onClose }: { item: Item; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ChangeDialog
      operation={`inbox-ignore:${item.id}`}
      title={t("upstream.inbox.ignoreTitle")}
      intro={<p>{t("upstream.inbox.ignoreBody")}</p>}
      action={t("upstream.inbox.ignore")}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/supplier-inbox/{item_id}/ignore", {
            params: { path: { item_id: item.id }, header: { "Idempotency-Key": key } },
            body: { reason },
          }),
        )
      }
      done={() => null}
      toast={() => t("upstream.inbox.ignored")}
      onClose={onClose}
    />
  );
}
