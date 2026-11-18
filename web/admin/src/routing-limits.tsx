import { type AdminSchemas, ApiError, adminApi, unwrapChange } from "@axiom/api";
import { useErrors } from "@axiom/app";
import { ActionButton, Cluster, DataState, Field, Grid, useFormat } from "@axiom/ui";
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChangeDialog, type Problem, useSnapshot } from "./change";
import { Facts, State } from "./parts";
import { routingLimitsQuery } from "./queries";

type View = AdminSchemas["RoutingPresetLimitsView"];
type Limits = AdminSchemas["RoutingPresetLimits"];

const mib = 1024 * 1024;
const layers = ["platform", "site", "account", "workspace"] as const;
type Layer = (typeof layers)[number];

// One number of the record: its form field, where the server points at it, the bounds the contract states, and how it
// is read from and written into the record. The bytes of the global budget are typed in MiB: a value divided by a power
// of two prints exactly and reads back to the same bytes.
type LimitField = {
  name: string;
  layer?: Layer;
  label: "active" | "total" | "kept" | "presets" | "bytes";
  min: number;
  max: number;
  unit: number;
  get: (limits: Limits) => number | undefined;
  set: (limits: Limits, value: number) => void;
};

const layerField = (layer: Layer, label: "active" | "total", max: number): LimitField => ({
  name: `${layer}/${label}`,
  layer,
  label,
  min: 1,
  max,
  unit: 1,
  get: (limits) => limits[layer]?.[label],
  set: (limits, value) => {
    limits[layer] = { ...limits[layer], [label]: value };
  },
});

const fields: LimitField[] = [
  ...layers.flatMap((layer) => [layerField(layer, "active", 500), layerField(layer, "total", 5000)]),
  {
    name: "kept_versions",
    label: "kept",
    min: 50,
    max: 500,
    unit: 1,
    get: (limits) => limits.kept_versions,
    set: (limits, value) => {
      limits.kept_versions = value;
    },
  },
  {
    name: "global/presets",
    label: "presets",
    min: 1,
    max: 1_000_000,
    unit: 1,
    get: (limits) => limits.global?.presets,
    set: (limits, value) => {
      limits.global = { ...limits.global, presets: value };
    },
  },
  {
    name: "global/bytes",
    label: "bytes",
    min: 1,
    max: 2048,
    unit: mib,
    get: (limits) => limits.global?.bytes,
    set: (limits, value) => {
      limits.global = { ...limits.global, bytes: value };
    },
  },
];

// readLimits turns the dialog's text into the record: a field left empty is left out, so the server applies the default
// in code, and a value outside its bounds is refused before the request. That a layer's live limit is at most its total
// is the server's to check, with the defaults filling the rest.
export function readLimits(values: Record<string, string>, problem: string): Limits | Problem {
  const out: Limits = {};
  for (const field of fields) {
    const typed = (values[field.name] ?? "").trim();
    if (typed === "") {
      continue;
    }
    const number = Number(typed);
    const value = number * field.unit;
    if (!(number >= field.min && number <= field.max && Number.isInteger(value))) {
      return { problem };
    }
    field.set(out, value);
  }
  return out;
}

// RoutingLimits is what every layer may hold of routing presets, the rule versions a preset keeps and the global budget
// every node holds in memory, each the saved value or the default in code.
export function RoutingLimits() {
  return <LimitsState query={useQuery(routingLimitsQuery)} />;
}

// LimitsState shows the card, or for a node of a release before part six of the presets says the limits are not there
// yet.
export function LimitsState({ query }: { query: UseQueryResult<View> }) {
  const { t } = useTranslation();
  return (
    <NodeState
      query={query}
      title={t("routingPresets.limits.title")}
      unavailable={t("routingPresets.limits.unavailable")}
    >
      {(view) => <LimitsCard view={view} />}
    </NodeState>
  );
}

// NodeState shows a card of the routing presets page from what query read. A node of an earlier release, which has no
// such operation, answers 404: the card says the node does not have it yet and offers to ask again, where DataState
// would call it a missing page and offer nothing.
export function NodeState<T>({
  query,
  title,
  unavailable,
  children,
}: {
  query: UseQueryResult<T>;
  title: string;
  unavailable: string;
  children: (data: T) => ReactNode;
}) {
  const { t } = useTranslation();
  if (query.error instanceof ApiError && query.error.status === 404) {
    return (
      <Facts
        title={title}
        action={
          <ActionButton disabled={query.isFetching} onClick={() => void query.refetch()} icon="refresh">
            {t("routingPresets.limits.retry")}
          </ActionButton>
        }
        rows={[[t("routingPresets.unavailableTitle"), unavailable]]}
      />
    );
  }
  return <DataState query={query}>{children}</DataState>;
}

function LimitsCard({ view }: { view: View }) {
  const { t } = useTranslation();
  const show = useShow();
  const [open, setOpen] = useState(false);
  const shown = (field: LimitField): ReactNode => {
    const own = field.get(view.limits);
    return (
      <span key={field.name}>
        {show(field, own ?? field.get(view.defaults))}{" "}
        <State tone="neutral">{own === undefined ? t("settings.default") : t("settings.setHere")}</State>
      </span>
    );
  };
  const rows: [string, ReactNode][] = [
    ...layers.map((layer): [string, ReactNode] => [
      t(`routingPresets.limits.layer.${layer}`),
      <Cluster gap={16} key={layer}>
        {fields
          .filter((field) => field.layer === layer)
          .map((field) => (
            <span key={field.name}>
              {t(`routingPresets.limits.field.${field.label}`)} {shown(field)}
            </span>
          ))}
      </Cluster>,
    ]),
    ...fields
      .filter((field) => field.layer === undefined)
      .map((field): [string, ReactNode] => [t(`routingPresets.limits.field.${field.label}`), shown(field)]),
  ];
  return (
    <>
      <Facts
        title={t("routingPresets.limits.title")}
        action={
          <ActionButton onClick={() => setOpen(true)} icon="pencil">
            {t("routingPresets.limits.edit")}
          </ActionButton>
        }
        rows={rows}
      />
      {open ? <LimitsDialog view={view} onClose={() => setOpen(false)} /> : null}
    </>
  );
}

function useShow() {
  const format = useFormat();
  return (field: LimitField, value: number | undefined) => {
    if (value === undefined) {
      return "—";
    }
    // A size saved through the API in bytes that are not whole MiB is shown in bytes, never rounded.
    return field.unit === mib && value % mib === 0 ? `${format.number(value / mib)} MiB` : format.number(value);
  };
}

function LimitsDialog({ view, onClose }: { view: View; onClose: () => void }) {
  const { t } = useTranslation();
  const errors = useErrors();
  const show = useShow();
  const [read, reload] = useSnapshot(view);
  return (
    <ChangeDialog<Limits, View>
      operation="routing-limits"
      title={t("routingPresets.limits.edit")}
      intro={<p>{t("routingPresets.limits.intro")}</p>}
      action={t("routingPresets.limits.save")}
      reload={reload}
      fields={(error) => (
        <Grid min="16rem">
          {fields.map((field) => {
            const own = field.get(read.limits);
            const label = t(`routingPresets.limits.field.${field.label}`);
            return (
              <Field
                key={field.name}
                name={field.name}
                label={field.layer ? `${t(`routingPresets.limits.layer.${field.layer}`)} · ${label}` : label}
                hint={t("routingPresets.limits.hint", {
                  min: show(field, field.min * field.unit),
                  max: show(field, field.max * field.unit),
                  standard: show(field, field.get(read.defaults)),
                })}
                inputMode="numeric"
                autoComplete="off"
                defaultValue={own === undefined ? "" : String(own / field.unit)}
                error={errors.field(error, `limits/${field.name}`)}
              />
            );
          })}
        </Grid>
      )}
      read={(values) => readLimits(values, t("routingPresets.limits.invalid"))}
      send={(limits, { reason, key }) =>
        unwrapChange(
          adminApi.PUT("/api/admin/v1/settings/routing-limits", {
            params: { header: { "Idempotency-Key": key } },
            body: { limits, rev: read.rev, reason },
          }),
        )
      }
      done={() => <p>{t("routingPresets.limits.saved")}</p>}
      onClose={onClose}
    />
  );
}
