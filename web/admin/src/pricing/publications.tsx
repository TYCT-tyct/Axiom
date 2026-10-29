import { type AdminSchemas, adminApi, unwrap, unwrapChange } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import { Button, CheckField, DataState, Panel, Table, Td, Th } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, useSnapshot } from "../change";
import { publicationKey } from "./publication-key";

type Publication = AdminSchemas["PricePublication"];

function PublicationAudience({ audience }: { audience: Publication["audience"] }) {
  const { t } = useTranslation();
  if (!audience) {
    return <>{t("pricing.publish.unavailable")}</>;
  }
  return (
    <div className="max-w-48">
      <p className="truncate">{t(`pricing.audience.${audience.kind}`)}</p>
      {audience.id ? (
        <p className="truncate text-label text-t3" title={audience.id}>
          {audience.id}
        </p>
      ) : null}
    </div>
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
                  <Th>{t("pricing.rules.audience")}</Th>
                  <Th>{t("pricing.publish.version")}</Th>
                  <Th>{t("pricing.publish.state")}</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {data.data.map((p) => (
                  <tr key={publicationKey(p)}>
                    <Td className="max-w-48">
                      <p className="truncate" title={p.model_id}>
                        {p.model_id}
                      </p>
                    </Td>
                    <Td>
                      <PublicationAudience audience={p.audience} />
                    </Td>
                    <Td className="max-w-48">
                      <p className="truncate" title={p.current_id}>
                        {p.current_id ?? t("pricing.publish.unavailable")}
                      </p>
                    </Td>
                    <Td>{p.status ? t(`pricing.publish.${p.status}`) : t("pricing.publish.unavailable")}</Td>
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
          <p className="text-label text-t3">{t("pricing.publish.requested")}</p>
          <pre className="max-h-48 overflow-auto text-label">{JSON.stringify(price.requested, null, 2)}</pre>
          <p className="text-label text-t3">{t("pricing.publish.safe")}</p>
          <pre className="max-h-48 overflow-auto text-label">{JSON.stringify(price.schedule, null, 2)}</pre>
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
