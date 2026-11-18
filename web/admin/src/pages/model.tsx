import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { CodeBlock } from "@axiom/boards/ds";
import { ActionButton, Card, Cluster, DataState, Icon, PageHead, Table, Tag, Td, Text } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ModelStatusDialog, NameDialog, SpecDialog } from "../models/dialogs";
import { ModelState } from "../models/parts";
import { Prices } from "../models/prices";
import { otherFacts } from "../models/spec-fields";
import { Actions, Code, Facts, Head, PageBody, State } from "../parts";
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
          <PageHead title={data.slug} lede={t("models.detail.description")} />
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
        <ActionButton asChild>
          <Link to="/models">
            <Icon name="left" />
            {t("models.detail.back")}
          </Link>
        </ActionButton>
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
        <ActionButton variant="pri" onClick={() => onOpen("listed")}>
          {t("models.status.listed.action")}
        </ActionButton>
      ) : null}
      <ActionButton icon="pencil" onClick={() => onOpen("spec")}>
        {t("models.spec.edit")}
      </ActionButton>
      <ActionButton icon="plus" onClick={() => onOpen("name")}>
        {t("models.names.add")}
      </ActionButton>
      {model.status === "listed" ? (
        <ActionButton variant="dan" onClick={() => onOpen("retired")}>
          {t("models.status.retired.action")}
        </ActionButton>
      ) : null}
    </>
  );
}

function SpecPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  const { items, tiers } = model.spec;
  const other = otherFacts(model.spec);
  return (
    <Facts
      title={t("models.spec.title")}
      rows={[
        [t("models.spec.items"), <List key="items" values={items?.map((item) => t(`models.item.${item}`))} />],
        [t("models.spec.tiers"), <List key="tiers" values={tiers?.map((tier) => t(`models.tier.${tier}`))} />],
        ...(other
          ? [
              [
                t("models.spec.other"),
                <CodeBlock key="other" label={t("models.spec.other")}>
                  {other}
                </CodeBlock>,
              ] as [string, ReactNode],
            ]
          : []),
      ]}
    />
  );
}

// A declared list: absent means the review has not been told yet, which keeps the model from going on sale; empty
// means it was told there is none.
function List({ values }: { values: string[] | undefined }) {
  const { t } = useTranslation();
  if (values === undefined) {
    return <State tone="warn">{t("models.spec.undeclared")}</State>;
  }
  if (values.length === 0) {
    return <Text tone="faint">{t("models.spec.none")}</Text>;
  }
  return (
    <Cluster gap={4}>
      {values.map((value) => (
        <Tag key={value}>{value}</Tag>
      ))}
    </Cluster>
  );
}

function NamesPanel({ model }: { model: Model }) {
  const { t } = useTranslation();
  return (
    <Card layout="flush" title={t("models.names.title")}>
      <Table label={t("models.names.title")}>
        <Head cols={[[t("models.names.name"), { main: true }], t("models.names.visibility")]} />
        <tbody>
          {model.names.map((name) => (
            <tr key={name.name}>
              <Td main>
                <Code>{name.name}</Code>
              </Td>
              <Td>{name.visibility === "public" ? t("models.names.public") : t("models.names.internal")}</Td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Card>
  );
}
