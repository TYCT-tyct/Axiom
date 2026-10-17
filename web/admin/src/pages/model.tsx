import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { Button, DataState, Icon, PageHeader, Panel, Table, Td, Th } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ModelStatusDialog, NameDialog, SpecDialog } from "../models/dialogs";
import { ModelState } from "../models/parts";
import { Prices } from "../models/prices";
import { otherFacts } from "../models/spec-fields";
import { Actions, Code, Facts, mainCell, PageBody, State } from "../parts";
import { modelQuery } from "../queries";

type Model = AdminSchemas["Model"];
type Open = "spec" | "name" | "listed" | "retired";

export default function ModelPage() {
  const { t } = useTranslation();
  const { modelId } = useParams({ from: "/app/models/$modelId" });
  const model = useQuery(modelQuery(modelId));
  return (
    <DataState query={model} page>
      {(data) => (
        <>
          <PageHeader title={data.slug} description={t("models.detail.description")} />
          <PageBody>
            <Body model={data} />
          </PageBody>
        </>
      )}
    </DataState>
  );
}

function Body({ model }: { model: Model }) {
  const { t } = useTranslation();
  const manage = can(useSession(), "catalog.manage") && model.status !== "retired";
  const [open, setOpen] = useState<Open | null>(null);
  return (
    <>
      <Actions>
        <Button asChild>
          <Link to="/models">
            <Icon name="left" />
            {t("models.detail.back")}
          </Link>
        </Button>
        {manage ? <ModelActions model={model} onOpen={setOpen} /> : null}
      </Actions>
      <Facts
        title={t("models.detail.facts")}
        rows={[
          [t("models.col.status"), <ModelState key="status" status={model.status} />],
          [t("models.col.author"), model.author],
          [t("models.col.basis"), t(`models.basis.${model.version_basis}`)],
          [t("models.col.source"), t(`models.source.${model.price_source}`)],
          [t("models.detail.rev"), model.rev],
          [t("models.detail.id"), <Code key="id">{model.id}</Code>],
        ]}
      />
      <SpecPanel model={model} />
      <NamesPanel model={model} />
      <Prices model={model} />
      {open === "spec" ? <SpecDialog model={model} onClose={() => setOpen(null)} /> : null}
      {open === "name" ? <NameDialog model={model} onClose={() => setOpen(null)} /> : null}
      {open === "listed" || open === "retired" ? (
        <ModelStatusDialog model={model} to={open} onClose={() => setOpen(null)} />
      ) : null}
    </>
  );
}

function ModelActions({ model, onOpen }: { model: Model; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  return (
    <>
      {model.status === "draft" ? (
        <Button variant="primary" onClick={() => onOpen("listed")}>
          {t("models.status.listed.action")}
        </Button>
      ) : null}
      <Button onClick={() => onOpen("spec")}>
        <Icon name="pencil" />
        {t("models.spec.edit")}
      </Button>
      <Button onClick={() => onOpen("name")}>
        <Icon name="plus" />
        {t("models.names.add")}
      </Button>
      {model.status === "listed" ? (
        <Button variant="danger" onClick={() => onOpen("retired")}>
          {t("models.status.retired.action")}
        </Button>
      ) : null}
    </>
  );
}

function SpecPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  const { items, tiers } = model.spec;
  const other = otherFacts(model.spec);
  return (
    <Panel title={t("models.spec.title")}>
      <dl className="divide-y divide-b1">
        <List label={t("models.spec.items")} values={items?.map((item) => t(`models.item.${item}`))} />
        <List label={t("models.spec.tiers")} values={tiers?.map((tier) => t(`models.tier.${tier}`))} />
        {other ? (
          <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
            <dt className="text-t3">{t("models.spec.other")}</dt>
            <dd className="min-w-0">
              <pre className="overflow-x-auto rounded-btn bg-s2 p-3 font-mono text-label text-t1">{other}</pre>
            </dd>
          </div>
        ) : null}
      </dl>
    </Panel>
  );
}

// A declared list: absent means the review has not been told yet, which keeps the model from going on sale; empty
// means it was told there is none.
function List({ label, values }: { label: string; values: string[] | undefined }) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
      <dt className="text-t3">{label}</dt>
      <dd className="flex min-w-0 flex-wrap gap-1.5">
        {values === undefined ? (
          <State tone="warn">{t("models.spec.undeclared")}</State>
        ) : values.length === 0 ? (
          <span className="text-t3">{t("models.spec.none")}</span>
        ) : (
          values.map((value) => (
            <span key={value} className="rounded-xs bg-s3 px-1.5 py-0.5 text-label text-t2">
              {value}
            </span>
          ))
        )}
      </dd>
    </div>
  );
}

function NamesPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  return (
    <Panel title={t("models.names.title")}>
      <Table label={t("models.names.title")}>
        <thead>
          <tr>
            <Th>{t("models.names.name")}</Th>
            <Th>{t("models.names.visibility")}</Th>
          </tr>
        </thead>
        <tbody>
          {model.names.map((name) => (
            <tr key={name.name}>
              <Td className={mainCell}>
                <span className="block truncate" title={name.name}>
                  <Code>{name.name}</Code>
                </span>
              </Td>
              <Td className="whitespace-nowrap">
                {name.visibility === "public" ? t("models.names.public") : t("models.names.internal")}
              </Td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Panel>
  );
}
