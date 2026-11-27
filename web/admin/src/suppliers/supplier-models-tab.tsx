import type { AdminSchemas } from "@axiom/api";
import { can, LoadMore, useSession } from "@axiom/app";
import {
  ActionButton,
  Card,
  Cluster,
  Disclosure,
  RowList,
  SegmentedControl,
  SettingRow,
  Text,
  useFormat,
} from "@axiom/ui";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useLines } from "../lines";
import { Code } from "../parts";
import type { SupplierView } from "../queries";
import { takesBases, targetOf } from "./base-price";
import { ConfirmBaseDialog } from "./base-price-dialogs";
import { EndpointEnabledDialog, QuantizationDialog } from "./dialogs";
import { EndpointDrawer } from "./endpoint-drawer";
import { EndpointTestDialog } from "./endpoint-test-dialog";
import { FamilyDialog, IntakeDialog } from "./intake-dialogs";
import { Endpoints } from "./lists";
import { MapDialog, type Unmatched } from "./map-dialog";
import type { Open } from "./open";
import { BuiltState } from "./parts";
import { supplierModelsQuery } from "./queries";
import { Unpriced, useListed } from "./unpriced";
import { byFamily, modeOf } from "./upstream";

type Mode = AdminSchemas["SupplierIntakeMode"];
const rules = ["listed", "known", "names"] as const satisfies readonly Mode[];

// 模型 (adPg_supplier's models tab): the rule that decides which names the sync takes, the names that match no model
// (要你看的), and the other three piles folded: on sale and taken, known but not on sale (by family, a family put on
// sale at once), and the old versions and snapshots the intake leaves alone.
export default function SupplierModelsTab({ detail, endpoint }: { detail: SupplierView; endpoint?: string }) {
  const manage = can(useSession(), "suppliers.manage") && detail.supplier.status !== "removed";
  return (
    <>
      {detail.intake_rule && modeOf(detail.supplier) === "relay" ? <Rule detail={detail} manage={manage} /> : null}
      <Review detail={detail} manage={manage} />
      {manage && takesBases(detail.supplier) ? <Unpriced detail={detail} /> : null}
      <Piles detail={detail} manage={manage} endpoint={endpoint} />
    </>
  );
}

function Rule({ detail, manage }: { detail: SupplierView; manage: boolean }) {
  const { t } = useTranslation();
  const [to, setTo] = useState<Mode | null>(null);
  const rule = detail.intake_rule;
  if (!rule) return null;
  return (
    <Card layout="rows">
      <SettingRow title={t("upstream.models.rule")} detail={t(`upstream.models.ruleNotes.${rule.mode}`)}>
        <SegmentedControl
          label={t("upstream.models.rule")}
          value={rule.mode}
          options={rules.map((value) => ({ value, label: t(`upstream.models.rules.${value}`) }))}
          onChange={(value) => (manage ? setTo(value) : undefined)}
        />
      </SettingRow>
      {to ? <IntakeDialog supplier={detail.supplier} rule={rule} to={to} onClose={() => setTo(null)} /> : null}
    </Card>
  );
}

function Review({ detail, manage }: { detail: SupplierView; manage: boolean }) {
  const { t } = useTranslation();
  const format = useFormat();
  const names = useInfiniteQuery(supplierModelsQuery(detail.supplier.id, "review"));
  const [mapping, setMapping] = useState<Unmatched | null>(null);
  const rows = names.data?.pages.flatMap((page) => page.data) ?? [];
  const groupName = (id: string) => detail.groups.find((group) => group.id === id)?.name;
  return (
    <BuiltState query={names}>
      {() => (
        <Card
          layout="rows"
          title={t("upstream.models.review")}
          lede={t("upstream.models.reviewLede")}
          actions={
            <Text as="span" size="sm" tone="faint">
              {t("upstream.models.reviewCount", { count: detail.model_counts?.review ?? rows.length })}
            </Text>
          }
        >
          <RowList
            label={t("upstream.models.review")}
            empty={t("upstream.models.reviewNone")}
            items={rows.map((row) => {
              const date = format.dateTimeMinute(new Date(row.first_seen_at));
              return {
                key: `${row.group_id}/${row.upstream_name}`,
                title: <Code>{row.upstream_name}</Code>,
                sub: row.suggested_model_slug
                  ? t("upstream.models.closest", { slug: row.suggested_model_slug, date })
                  : t("upstream.models.noClosest", { date }),
                end: manage ? (
                  <ActionButton
                    size="sm"
                    onClick={() =>
                      setMapping({
                        supplierId: detail.supplier.id,
                        groupId: row.group_id,
                        group: groupName(row.group_id),
                        upstreamName: row.upstream_name,
                        closest: row.suggested_model_slug,
                      })
                    }
                  >
                    {t("upstream.models.map")}
                  </ActionButton>
                ) : undefined,
              };
            })}
          />
          <LoadMore list={names} />
          {mapping ? <MapDialog unmatched={mapping} onClose={() => setMapping(null)} /> : null}
        </Card>
      )}
    </BuiltState>
  );
}

function Piles({ detail, manage, endpoint }: { detail: SupplierView; manage: boolean; endpoint?: string }) {
  const { t } = useTranslation();
  const counts = detail.model_counts;
  const count = (n: number | undefined) => (n === undefined ? "—" : String(n));
  return (
    <Card layout="padded">
      <Disclosure
        summary={t("upstream.models.listed")}
        count={count(counts?.listed ?? detail.endpoints?.length)}
        note={t("upstream.models.listedNote")}
        open={endpoint !== undefined}
        lazy
      >
        <Listed detail={detail} manage={manage} endpoint={endpoint} />
      </Disclosure>
      {counts ? (
        <>
          <Disclosure
            summary={t("upstream.models.known")}
            count={count(counts.known)}
            note={t("upstream.models.knownNote")}
            lazy
          >
            <Known supplierId={detail.supplier.id} supplier={detail.supplier} manage={manage} />
          </Disclosure>
          <Disclosure
            summary={t("upstream.models.outdated")}
            count={count(counts.outdated)}
            note={t("upstream.models.outdatedNote")}
            lazy
          >
            <Outdated supplierId={detail.supplier.id} total={counts.outdated} />
          </Disclosure>
        </>
      ) : null}
    </Card>
  );
}

// The endpoints the channel has: each opens its price lines and checks, and can be tested, switched and given its
// precision from the list (lists.tsx).
function Listed({ detail, manage, endpoint }: { detail: SupplierView; manage: boolean; endpoint?: string }) {
  const navigate = useNavigate();
  const { byGroup } = useLines();
  const [open, setOpen] = useState<Open | null>(null);
  const { supplier } = detail;
  const opened = detail.endpoints?.find((one) => one.id === endpoint);
  const bases = manage && takesBases(supplier);
  const listed = useListed(supplier.id, bases);
  const line = (groupId: string) => byGroup.get(groupId)?.name ?? supplier.name;
  const show = (id: string | undefined) =>
    void navigate({
      to: "/suppliers/$supplierId",
      params: { supplierId: supplier.id },
      search: { tab: "models", endpoint: id },
    });
  const close = () => setOpen(null);
  return (
    <>
      <Endpoints
        endpoints={detail.endpoints}
        groups={detail.groups}
        baseOf={(one) => (bases ? targetOf(one, listed.find) : undefined)}
        onOpen={manage ? setOpen : undefined}
        onShow={(one) => show(one.id)}
      />
      {opened ? (
        <EndpointDrawer
          supplier={supplier}
          endpoint={opened}
          line={line(opened.group_id)}
          onClose={() => show(undefined)}
        />
      ) : null}
      {open?.kind === "test" ? (
        <EndpointTestDialog endpoint={open.endpoint} group={open.group} onClose={close} />
      ) : null}
      {open?.kind === "endpointEnabled" ? (
        <EndpointEnabledDialog supplier={supplier} endpoint={open.endpoint} group={open.group} onClose={close} />
      ) : null}
      {open?.kind === "quantization" ? (
        <QuantizationDialog supplier={supplier} endpoint={open.endpoint} onClose={close} />
      ) : null}
      {open?.kind === "basePrice" ? (
        <ConfirmBaseDialog supplier={supplier} target={open.target} onClose={close} />
      ) : null}
    </>
  );
}

// 认识，但你没有上架: the names of draft models, by the family the catalog gives them.
function Known({
  supplierId,
  supplier,
  manage,
}: {
  supplierId: string;
  supplier: AdminSchemas["Supplier"];
  manage: boolean;
}) {
  const { t } = useTranslation();
  const names = useInfiniteQuery(supplierModelsQuery(supplierId, "known"));
  const [listing, setListing] = useState<{ family: string; models: string[] } | null>(null);
  const rows = names.data?.pages.flatMap((page) => page.data) ?? [];
  const families = byFamily(rows, t("upstream.models.noFamily"));
  return (
    <BuiltState query={names}>
      {() => (
        <>
          <RowList
            label={t("upstream.models.known")}
            empty={t("upstream.models.nothing")}
            items={families.map(({ family, names: of, models }) => ({
              key: family,
              title: family,
              sub: of.slice(0, 4).join("　"),
              end: (
                <Cluster gap={12}>
                  <Text as="span" size="sm" tone="faint">
                    {t("upstream.models.familyCount", { count: of.length })}
                  </Text>
                  {manage && models.length > 0 ? (
                    <ActionButton size="sm" onClick={() => setListing({ family, models })}>
                      {t("upstream.models.listFamily")}
                    </ActionButton>
                  ) : null}
                </Cluster>
              ),
            }))}
          />
          <LoadMore list={names} />
          {listing ? (
            <FamilyDialog
              supplier={supplier}
              family={listing.family}
              models={listing.models}
              onClose={() => setListing(null)}
            />
          ) : null}
        </>
      )}
    </BuiltState>
  );
}

function Outdated({ supplierId, total }: { supplierId: string; total: number }) {
  const { t } = useTranslation();
  const names = useInfiniteQuery(supplierModelsQuery(supplierId, "outdated"));
  const rows = names.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <BuiltState query={names}>
      {() =>
        rows.length === 0 ? (
          <Text tone="muted">{t("upstream.models.nothing")}</Text>
        ) : (
          <>
            <Text tone="muted">
              <Code>{rows.map((row) => row.upstream_name).join("　")}</Code>
              {total > rows.length ? `　${t("upstream.models.more", { count: total - rows.length })}` : ""}
            </Text>
            <Text size="sm" tone="muted">
              {t("upstream.models.outdatedFoot")}
            </Text>
          </>
        )
      }
    </BuiltState>
  );
}
