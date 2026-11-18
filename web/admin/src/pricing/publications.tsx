import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import { ActionButton, Card, CheckField, DataState, Mono, Stack, Table, Td, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import { Head, Json } from "../parts";
import { publicationKey } from "./publication-key";

type Publication = AdminSchemas["PricePublication"];
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

function PublicationAudience({ audience }: { audience: Publication["audience"] }) {
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

export function Publications() {
  const { t } = useTranslation();
  const manage = can(useSession(), "pricing.manage");
  const [editing, setEditing] = useState<string | null>(null);
  const query = useQuery({
    queryKey: [accountKey, "price-publications"],
    queryFn: ({ signal }) => unwrap(adminApi.GET("/api/admin/v1/pricing/publications", { signal })),
  });
  const current = query.data?.data?.find((p) => publicationKey(p) === editing);
  return (
    <Card layout="flush" title={t("pricing.publish.title")}>
      <DataState query={query}>
        {(data) =>
          data.data ? (
            <Table label={t("pricing.publish.title")}>
              <Head
                cols={[
                  [t("pricing.publish.model"), { main: true }],
                  t("pricing.rules.audience"),
                  t("pricing.publish.version"),
                  t("pricing.publish.state"),
                ]}
                action
              />
              <tbody>
                {data.data.map((p) => (
                  <tr key={publicationKey(p)}>
                    <Td main>{p.model_id}</Td>
                    <Td>
                      <PublicationAudience audience={p.audience} />
                    </Td>
                    <Td>{p.current_id ?? t("pricing.publish.unavailable")}</Td>
                    <Td>{p.status ? t(`pricing.publish.${p.status}`) : t("pricing.publish.unavailable")}</Td>
                    <Td actions>
                      {manage && p.schedule && p.model_id && p.candidate_id ? (
                        <ActionButton size="sm" onClick={() => setEditing(publicationKey(p))}>
                          {t("pricing.publish.action")}
                        </ActionButton>
                      ) : (
                        p.problem
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          ) : (
            <Text tone="faint">{t("pricing.publish.unavailable")}</Text>
          )
        }
      </DataState>
      {current ? <PublishDialog live={current} onClose={() => setEditing(null)} /> : null}
    </Card>
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

function PublishDialog({ live, onClose }: { live: Publication; onClose: () => void }) {
  const { t } = useTranslation();
  const [price, reload] = useSnapshot(live);
  const model = price.model_id;
  const candidate = price.candidate_id;
  if (!model || !candidate) {
    return null;
  }
  return (
    <ChangeDialog
      operation={`price-publication:${publicationKey(price)}`}
      title={t("pricing.publish.action")}
      intro={
        <>
          <p>{t("pricing.publish.intro")}</p>
          <PublicationAudience audience={price.audience} />
        </>
      }
      action={t("pricing.publish.action")}
      confirm={price.model_id}
      reload={reload}
      fields={() => (
        <>
          <PublishPrices price={price} />
          <CheckField name="override" label={t("pricing.publish.override")} />
        </>
      )}
      read={(values) => ({ override: values.override === "on" })}
      send={(input, { reason, key }) =>
        unwrapChange(
          adminApi.POST("/api/admin/v1/pricing/publications/{model_id}", {
            params: { path: { model_id: model }, header: { "Idempotency-Key": key } },
            body: {
              candidate_id: candidate,
              current_id: price.current_id,
              confirmed: true,
              override_floor: input.override,
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
