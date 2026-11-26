import type { AdminSchemas } from "@axiom/api";
import { can, useSession } from "@axiom/app";
import { ActionButton, Cluster, DataState, LinkButton, SidePanel, Stack } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Lines } from "../lines";
import { Code, Facts } from "../parts";
import { modelQuery } from "../queries";
import { LinesPanel, NamesPanel, SpecPanel } from "./detail";
import { ModelStatusDialog, NameDialog, SpecDialog } from "./dialogs";
import { FamilyDialog } from "./library-dialogs";
import { ModelState } from "./parts";
import { Prices } from "./prices";

type Model = AdminSchemas["Model"];
type Open = "spec" | "name" | "family" | "listed" | "retired";

// The dialog open on a model: kept with the model's ID, so stepping to another model closes it.
type Opened = { id: string; kind: Open } | null;

// ModelDrawer is a model's detail over the catalog (adP_model in admin_pages_a.js): its facts, spec, names, reference
// prices and the lines it runs on, with what can be done to it in the foot. Stepping walks the rows the catalog has
// read. The family is shown, and set, only where the model library's backend is (library).
export function ModelDrawer({
  id,
  index,
  count,
  slug,
  authors,
  lines,
  library,
  onStep,
  onClose,
}: {
  id: string;
  index: number;
  count: number;
  slug: string | undefined;
  authors: ReadonlyMap<string, string>;
  lines: Lines;
  library: boolean;
  onStep: (by: -1 | 1) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const query = useQuery(modelQuery(id));
  const manage = can(useSession(), "catalog.manage") && query.data?.status !== "retired";
  const [opened, setOpened] = useState<Opened>(null);
  const open = opened?.id === id ? opened.kind : null;
  const model = query.data;
  const author = model ? (authors.get(model.author) ?? model.author) : undefined;
  return (
    <>
      <SidePanel
        drawer
        wide
        title={model?.slug ?? slug ?? t("models.drawer.loading")}
        subtitle={author}
        onClose={onClose}
        steps={
          index < 0
            ? undefined
            : {
                index,
                count,
                onStep,
                previousLabel: t("models.drawer.previous"),
                nextLabel: t("models.drawer.next"),
              }
        }
        footer={model && manage ? <Actions model={model} onOpen={(kind) => setOpened({ id, kind })} /> : undefined}
      >
        <DataState query={query} compact>
          {(data) => (
            <Stack>
              <ModelFacts
                model={data}
                authors={authors}
                onFamily={library ? (manage ? () => setOpened({ id, kind: "family" }) : null) : undefined}
              />
              <SpecPanel model={data} />
              <NamesPanel model={data} />
              <Prices model={data} />
              <LinesPanel model={data} lines={lines} />
            </Stack>
          )}
        </DataState>
      </SidePanel>
      {model && open ? <ModelDialog model={model} open={open} onClose={() => setOpened(null)} /> : null}
    </>
  );
}

function Actions({ model, onOpen }: { model: Model; onOpen: (open: Open) => void }) {
  const { t } = useTranslation();
  return (
    <Cluster justify="end">
      {model.status === "listed" ? (
        <ActionButton variant="dan" onClick={() => onOpen("retired")}>
          {t("models.status.retired.action")}
        </ActionButton>
      ) : null}
      <ActionButton icon="plus" onClick={() => onOpen("name")}>
        {t("models.names.add")}
      </ActionButton>
      <ActionButton icon="pencil" variant={model.status === "draft" ? "sec" : "pri"} onClick={() => onOpen("spec")}>
        {t("models.spec.edit")}
      </ActionButton>
      {model.status === "draft" ? (
        <ActionButton variant="pri" onClick={() => onOpen("listed")}>
          {t("models.status.listed.action")}
        </ActionButton>
      ) : null}
    </Cluster>
  );
}

// The facts of a model. Its family is shown where the library's backend is or the model has one, and can be set when
// onFamily is given: undefined says there is no library, null that the person may not change it.
function ModelFacts({
  model,
  authors,
  onFamily,
}: {
  model: Model;
  authors: ReadonlyMap<string, string>;
  onFamily: (() => void) | null | undefined;
}) {
  const { t } = useTranslation();
  const family = onFamily !== undefined || model.family !== undefined;
  return (
    <Facts
      rows={[
        [t("models.col.status"), <ModelState key="status" status={model.status} />],
        [t("models.col.author"), authors.get(model.author) ?? model.author],
        ...(family
          ? [
              [
                t("models.family.label"),
                <Cluster key="family" gap={8}>
                  {model.family ? <Code>{model.family}</Code> : t("models.family.none")}
                  {onFamily ? <LinkButton onClick={onFamily}>{t("models.family.set")}</LinkButton> : null}
                </Cluster>,
              ] as const,
            ]
          : []),
        [t("models.col.basis"), t(`models.basis.${model.version_basis}`)],
        [t("models.col.source"), t(`models.source.${model.price_source}`)],
        [t("models.detail.rev"), model.rev],
        [t("models.detail.id"), <Code key="id">{model.id}</Code>],
      ]}
    />
  );
}

function ModelDialog({ model, open, onClose }: { model: Model; open: Open; onClose: () => void }) {
  switch (open) {
    case "spec":
      return <SpecDialog model={model} onClose={onClose} />;
    case "name":
      return <NameDialog model={model} onClose={onClose} />;
    case "family":
      return <FamilyDialog model={model} onClose={onClose} />;
    default:
      return <ModelStatusDialog model={model} to={open} onClose={onClose} />;
  }
}
