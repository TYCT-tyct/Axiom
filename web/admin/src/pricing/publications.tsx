import { type AdminSchemas, adminApi, unwrapChange } from "@axiom/api";
import { type DiffRow, Mono, Stack, Text } from "@axiom/ui";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import { rateItems } from "../models/catalog";
import { showRate } from "../models/price-fields";
import { Json } from "../parts";
import { belowFloor, raises } from "./floor";
import { publicationKey } from "./publication-key";

type Publication = AdminSchemas["PricePublication"];
type RateItem = (typeof rateItems)[number];
type EndpointPrices = NonNullable<Publication["endpoints"]>;

function EndpointPriceList({ label, prices }: { label: string; prices?: EndpointPrices }) {
  if (!prices || Object.keys(prices).length === 0) {
    return null;
  }
  return (
    <Stack gap={8}>
      <Text size="sm" tone="faint">
        {label}
      </Text>
      {Object.entries(prices).map(([endpoint, schedule]) => (
        <Stack key={endpoint} gap={4}>
          <Text size="sm" tone="faint">
            <Mono>{endpoint}</Mono>
          </Text>
          <Json value={schedule} label={endpoint} />
        </Stack>
      ))}
    </Stack>
  );
}

export function PublicationAudience({ audience }: { audience: Publication["audience"] }) {
  const { t } = useTranslation();
  if (!audience) {
    return <>{t("pricing.publish.unavailable")}</>;
  }
  return (
    <>
      <Text>{t(`pricing.audience.${audience.kind}`)}</Text>
      {audience.id ? (
        <Text size="sm" tone="faint">
          {audience.id}
        </Text>
      ) : null}
    </>
  );
}

// A current version without per-endpoint prices (or an old node) has only current_schedule.
function PublishPrices({ price }: { price: Publication }) {
  const { t } = useTranslation();
  const hasCurrentEndpoints = !!price.current_endpoints && Object.keys(price.current_endpoints).length > 0;
  return (
    <Stack gap={8}>
      <EndpointPriceList
        label={t("pricing.publish.currentEndpoints")}
        prices={hasCurrentEndpoints ? price.current_endpoints : undefined}
      />
      {!hasCurrentEndpoints && price.current_schedule ? (
        <Schedule label={t("pricing.publish.currentSchedule")} value={price.current_schedule} />
      ) : null}
      <Schedule label={t("pricing.publish.requested")} value={price.requested} />
      <EndpointPriceList label={t("pricing.publish.requestedEndpoints")} prices={price.requested_endpoints} />
      <Schedule label={t("pricing.publish.safe")} value={price.schedule} />
      <EndpointPriceList label={t("pricing.publish.safeEndpoints")} prices={price.endpoints} />
      <EndpointPriceList label={t("pricing.publish.endpointCosts")} prices={price.costs} />
      <EndpointPriceList label={t("pricing.publish.endpointFloors")} prices={price.floors} />
    </Stack>
  );
}

function Schedule({ label, value }: { label: string; value: unknown }) {
  return (
    <Stack gap={4}>
      <Text size="sm" tone="faint">
        {label}
      </Text>
      <Json value={value} label={label} />
    </Stack>
  );
}

// What a publication changes at the model level, item by item as the contract gives it: the current price list's price
// of each item against the one the proposal publishes, per million tokens (per use where an item is priced so).
function priceDiff(price: Publication, name: (item: RateItem) => string): DiffRow[] {
  const next = price.schedule;
  if (!next) return [];
  const was = price.current_schedule;
  return rateItems.flatMap((item) => {
    const after = next.rates[item];
    const before = was?.rates[item];
    if (after === undefined && before === undefined) return [];
    const shown = (value: string | undefined) => (value === undefined ? undefined : showRate(item, value));
    return [{ label: name(item), before: shown(before), after: shown(after) ?? "—" }];
  });
}

// Publishing makes a price version current for every user at once. A price the rule asks below the site's floor is
// published at the floor, which is what `schedule` is: there is no way to publish under it here. The person confirms by
// typing the model's ID, as of the version they read.
export function PublishDialog({ live, onClose }: { live: Publication; onClose: () => void }) {
  const { t } = useTranslation();
  const [price, reload] = useSnapshot(live);
  const model = price.model_id;
  const candidate = price.candidate_id;
  if (!model || !candidate) {
    return null;
  }
  const atFloor = belowFloor(price);
  return (
    <ChangeDialog
      operation={`price-publication:${publicationKey(price)}`}
      title={atFloor ? t("pricing.publish.atFloor") : t("pricing.publish.action")}
      intro={
        <>
          <p>{t("pricing.publish.intro")}</p>
          <PublicationAudience audience={price.audience} />
        </>
      }
      diff={priceDiff(price, (item) => t(`models.item.${item}`))}
      warn={
        atFloor
          ? t("pricing.publish.floorWarn")
          : raises(price.schedule, price.current_schedule)
            ? t("pricing.publish.raisesWarn")
            : undefined
      }
      action={atFloor ? t("pricing.publish.atFloor") : t("pricing.publish.action")}
      confirm={price.model_id}
      reload={reload}
      fields={() => <PublishPrices price={price} />}
      read={() => null}
      send={(_, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/pricing/publications/{model_id}", {
            params: { path: { model_id: model }, header: { "Idempotency-Key": key } },
            body: {
              candidate_id: candidate,
              current_id: price.current_id,
              confirmed: true,
              override_floor: false,
              reason,
            },
          }),
        )
      }
      done={() => <p>{t("pricing.publish.saved")}</p>}
      onClose={onClose}
    />
  );
}
