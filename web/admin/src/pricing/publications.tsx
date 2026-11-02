import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import { Button, CheckField, DataState, Panel, Table, Td, Th } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import { publicationKey } from "./publication-key";

type Publication = AdminSchemas["PricePublication"];
type EndpointPrices = NonNullable<Publication["endpoints"]>;

function EndpointPriceList({ label, prices }: { label: string; prices?: EndpointPrices }) {
  if (!prices || Object.keys(prices).length === 0) {
    return null;
  }
  return (
    <div className="space-y-2">
      <p className="text-label text-t3">{label}</p>
      {Object.entries(prices).map(([endpoint, schedule]) => (
        <div key={endpoint} className="rounded border border-line p-2">
          <p className="break-all text-label text-t3" title={endpoint}>
            {endpoint}
          </p>
          <pre className="max-h-40 overflow-auto text-label">{JSON.stringify(schedule, null, 2)}</pre>
        </div>
      ))}
    </div>
  );
}

function PublicationAudience({ audience }: { audience: Publication["audience"] }) {
  const { t } = useTranslation();
  if (!audience) {
    return <>{t("pricing.publish.unavailable")}</>;
  }
  return (
    <div className="max-w-48">
      <p className="truncate">{t(`pricing.audience.${audience.kind}`)}</p>
      {audience.id ? (
        <p className="break-all text-label text-t3 xl:truncate" title={audience.id}>
          {audience.id}
        </p>
      ) : null}
    </div>
  );
}

function PublicationModel({ publication: p }: { publication: Publication }) {
  const { t } = useTranslation();
  return (
    <Td className="max-w-48">
      <p className="break-all xl:truncate" title={p.model_id}>
        {p.model_id}
      </p>
      <div className="mt-2 space-y-2 xl:hidden">
        <PublicationAudience audience={p.audience} />
        <p>{p.status ? t(`pricing.publish.${p.status}`) : t("pricing.publish.unavailable")}</p>
        <p className="break-all text-label text-t3">
          {t("pricing.publish.version")}
          {": "}
          {p.current_id ?? t("pricing.publish.unavailable")}
        </p>
      </div>
    </Td>
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
    <Panel title={t("pricing.publish.title")}>
      <DataState query={query}>
        {(data) =>
          data.data ? (
            <Table label={t("pricing.publish.title")}>
              <thead>
                <tr>
                  <Th>{t("pricing.publish.model")}</Th>
                  <Th className="hidden xl:table-cell">{t("pricing.rules.audience")}</Th>
                  <Th className="hidden xl:table-cell">{t("pricing.publish.version")}</Th>
                  <Th className="hidden xl:table-cell">{t("pricing.publish.state")}</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {data.data.map((p) => (
                  <tr key={publicationKey(p)}>
                    <PublicationModel publication={p} />
                    <Td className="hidden xl:table-cell">
                      <PublicationAudience audience={p.audience} />
                    </Td>
                    <Td className="hidden max-w-48 xl:table-cell">
                      <p className="truncate" title={p.current_id}>
                        {p.current_id ?? t("pricing.publish.unavailable")}
                      </p>
                    </Td>
                    <Td className="hidden xl:table-cell">
                      {p.status ? t(`pricing.publish.${p.status}`) : t("pricing.publish.unavailable")}
                    </Td>
                    <Td>
                      {manage && p.schedule && p.model_id && p.candidate_id ? (
                        <Button onClick={() => setEditing(publicationKey(p))}>{t("pricing.publish.action")}</Button>
                      ) : (
                        p.problem
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          ) : (
            <p className="p-4 text-t3">{t("pricing.publish.unavailable")}</p>
          )
        }
      </DataState>
      {current ? <PublishDialog live={current} onClose={() => setEditing(null)} /> : null}
    </Panel>
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
  const hasCurrentEndpoints = !!price.current_endpoints && Object.keys(price.current_endpoints).length > 0;
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
          <EndpointPriceList
            label={t("pricing.publish.currentEndpoints")}
            prices={hasCurrentEndpoints ? price.current_endpoints : undefined}
          />
          {!hasCurrentEndpoints && price.current_schedule ? (
            <>
              <p className="text-label text-t3">{t("pricing.publish.currentSchedule")}</p>
              <pre className="max-h-48 overflow-auto text-label">{JSON.stringify(price.current_schedule, null, 2)}</pre>
            </>
          ) : null}
          <p className="text-label text-t3">{t("pricing.publish.requested")}</p>
          <pre className="max-h-48 overflow-auto text-label">{JSON.stringify(price.requested, null, 2)}</pre>
          <EndpointPriceList label={t("pricing.publish.requestedEndpoints")} prices={price.requested_endpoints} />
          <p className="text-label text-t3">{t("pricing.publish.safe")}</p>
          <pre className="max-h-48 overflow-auto text-label">{JSON.stringify(price.schedule, null, 2)}</pre>
          <EndpointPriceList label={t("pricing.publish.safeEndpoints")} prices={price.endpoints} />
          <EndpointPriceList label={t("pricing.publish.endpointCosts")} prices={price.costs} />
          <EndpointPriceList label={t("pricing.publish.endpointFloors")} prices={price.floors} />
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
