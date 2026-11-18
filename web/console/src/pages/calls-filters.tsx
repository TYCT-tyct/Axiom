import type { Schemas } from "@axiom/api";
import { toInstant, toLocalInput } from "@axiom/app";
import LogsBtn from "@axiom/boards/AxApp/regions/LogsBtn";
import btnZh from "@axiom/boards/AxApp/regions/LogsBtn.zh";
import LogsFilters from "@axiom/boards/AxApp/regions/LogsFilters";
import filtersZh from "@axiom/boards/AxApp/regions/LogsFilters.zh";
import LogsSeg from "@axiom/boards/AxApp/regions/LogsSeg";
import segZh from "@axiom/boards/AxApp/regions/LogsSeg.zh";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import type { CallFilters } from "../queries";
import { dateRangeForPreset, detectRangePreset, modelDotColor, type RangePreset } from "./calls-data";
import { segClickIndex } from "./calls-states";

type ChipOption = { id: string; label: string; color?: string };
type SegItem = { key: string; kind: "0"; f0: "button"; f1: string; f2: string };

const inlineBarStyle =
  "display: inline-block; overflow: visible; position: relative; vertical-align: top; padding: 0; margin-right: 8px; margin-bottom: 8px;";
const chipFrameStyle = (open: boolean) => `${inlineBarStyle} z-index: ${open ? 40 : 25};`;
const inputFrameStyle =
  "display: inline-block; min-width: 190px; max-width: 260px; padding: 0; margin-right: 8px; margin-bottom: 8px; vertical-align: middle;";
const segWrapStyle = "margin-right: 8px; margin-bottom: 8px; vertical-align: middle;";
const zeroBarStyle = "margin: 0;";
const clearSearch = {
  since: undefined,
  until: undefined,
  model: undefined,
  client_app: undefined,
  key_id: undefined,
  workspace_id: undefined,
  status: undefined,
  fell_back: undefined,
  request_id: undefined,
};

function segItems(labels: readonly string[], activeIndex: number): SegItem[] {
  return labels.map((label, i) => ({
    key: `${i}-${label}`,
    kind: "0",
    f0: "button",
    f1: i === activeIndex ? "on" : "",
    f2: label,
  }));
}

function SegControl({
  labels,
  activeIndex,
  ariaLabel,
  onPick,
}: {
  labels: readonly string[];
  activeIndex: number;
  ariaLabel: string;
  onPick: (index: number) => void;
}) {
  return (
    <LogsSeg
      t={(k) => segZh[k]}
      values={{
        ariaLabel,
        segStyle: `--n: ${labels.length}; --i: ${Math.max(0, activeIndex)}; ${segWrapStyle}`,
        onClick: (event) => {
          const idx = segClickIndex(event);
          if (idx >= 0) onPick(idx);
        },
      }}
      rows={{ items0: segItems(labels, activeIndex) }}
    />
  );
}

function FilterChip({
  label,
  clearLabel,
  selectedId,
  options,
  open,
  onToggle,
  onPick,
  onClear,
}: {
  label: string;
  clearLabel: string;
  selectedId?: string;
  options: ChipOption[];
  open: boolean;
  onToggle: () => void;
  onPick: (id: string) => void;
  onClear: () => void;
}) {
  const rows = options.map((o, i) => ({
    key: o.id,
    kind: "0" as const,
    f0: "button" as const,
    f1: o.id === selectedId ? "on" : "",
    f2: "pickm",
    f3: String(i),
    f4: `background: ${o.color ?? "var(--c0)"};`,
    f5: o.label,
  }));
  return (
    <LogsFilters
      t={(k) => filtersZh[k]}
      values={{
        pageStyle: chipFrameStyle(open),
        barStyle: zeroBarStyle,
        hideHeader: true,
        hideSummary: true,
        hideList: true,
        hideRange: true,
        hideChipK: true,
        hideStatus: true,
        hideInput: true,
        hideChipPop: !open,
        unbuilt: true,
        chipExpanded: open,
        chipBtnStyle: selectedId ? "background: var(--accentWash); color: var(--accentT);" : undefined,
        onChipToggle: onToggle,
        onChipPick: (e) => {
          const idx = Number((e.target as Element).closest("[data-i]")?.getAttribute("data-i") ?? -1);
          const picked = options[idx];
          if (picked) onPick(picked.id);
        },
        onChipClear: onClear,
      }}
      rows={{ options0: rows }}
      text={{ heading: "", chipLabel: label, chipClear: clearLabel, inputPlaceholder: "" }}
    />
  );
}

function FilterInput({
  label,
  placeholder,
  value,
  type = "text",
  hideIcon,
  onChange,
}: {
  label: string;
  placeholder: string;
  value: string;
  type?: string;
  hideIcon?: boolean;
  onChange: (next: string) => void;
}) {
  return (
    <LogsFilters
      t={(k) => filtersZh[k]}
      values={{
        pageStyle: inputFrameStyle,
        barStyle: zeroBarStyle,
        hideHeader: true,
        hideSummary: true,
        hideList: true,
        hideRange: true,
        hideChipM: true,
        hideChipK: true,
        hideStatus: true,
        hideInputIcon: hideIcon,
        inputType: type,
        inputAriaLabel: label,
        inputValue: value,
        unbuilt: true,
        onInput: (e) => onChange((e.currentTarget as HTMLInputElement).value),
      }}
      rows={{ options0: [] }}
      text={{ heading: "", chipLabel: "", chipClear: "", inputPlaceholder: placeholder }}
    />
  );
}

function statusIndex(filters: CallFilters) {
  if (filters.status === "success") return 1;
  if (filters.fell_back) return 2;
  if (filters.status === "error") return 3;
  return 0;
}

type Draft = {
  model: string;
  client_app: string;
  request_id: string;
  key_id: string;
  workspace_id: string;
  sinceLocal: string;
  untilLocal: string;
};

function draftFromFilters(f: CallFilters): Draft {
  return {
    model: f.model ?? "",
    client_app: f.client_app ?? "",
    request_id: f.request_id ?? "",
    key_id: f.key_id ?? "",
    workspace_id: f.workspace_id ?? "",
    sinceLocal: toLocalInput(f.since),
    untilLocal: toLocalInput(f.until),
  };
}

function statusPatch(idx: number): Partial<CallFilters> {
  if (idx === 1) return { status: "success", fell_back: undefined };
  if (idx === 2) return { status: undefined, fell_back: true };
  if (idx === 3) return { status: "error", fell_back: undefined };
  return { status: undefined, fell_back: undefined };
}

function useChipDropdowns() {
  const [openChip, setOpenChip] = useState<"model" | "key" | "workspace" | null>(null);
  useEffect(() => {
    if (!openChip) return;
    const onDown = (e: PointerEvent) => {
      if (!(e.target as Element).closest('[data-c="m"]')) setOpenChip(null);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpenChip(null);
    };
    document.addEventListener("pointerdown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [openChip]);
  return { openChip, setOpenChip };
}

function ExtraFields({
  t,
  draft,
  setDraft,
}: {
  t: TFunction;
  draft: Draft;
  setDraft: (fn: (prev: Draft) => Draft) => void;
}) {
  return (
    <div>
      <FilterInput
        label={t("callsBoard.filter.clientApp")}
        placeholder={t("callsBoard.filter.clientAppPlaceholder")}
        value={draft.client_app}
        onChange={(v) => setDraft((d) => ({ ...d, client_app: v }))}
      />
      <FilterInput
        label={t("callsBoard.filter.requestId")}
        placeholder={t("callsBoard.filter.requestIdPlaceholder")}
        value={draft.request_id}
        onChange={(v) => setDraft((d) => ({ ...d, request_id: v }))}
      />
      <FilterInput
        label={t("callsBoard.range.since")}
        placeholder=""
        type="datetime-local"
        hideIcon
        value={draft.sinceLocal}
        onChange={(v) => setDraft((d) => ({ ...d, sinceLocal: v }))}
      />
      <FilterInput
        label={t("callsBoard.range.until")}
        placeholder=""
        type="datetime-local"
        hideIcon
        value={draft.untilLocal}
        onChange={(v) => setDraft((d) => ({ ...d, untilLocal: v }))}
      />
    </div>
  );
}

type FilterSourceProps = {
  t: TFunction;
  filters: CallFilters;
  models: Schemas["PublicModel"][];
  keys: Schemas["Key"][];
  workspaces: Schemas["Workspace"][];
  onApply: (next: CallFilters) => void;
};

function ChipRow({ t, filters, models, keys, workspaces, onApply }: FilterSourceProps) {
  const { openChip, setOpenChip } = useChipDropdowns();
  const modelOpts = models.map((m) => ({ id: m.id, label: m.id, color: modelDotColor(m.id) }));
  const keyOpts = keys.map((k) => ({ id: k.id, label: k.name || k.id, color: "var(--c1)" }));
  const wsOpts = workspaces.map((w) => ({ id: w.id, label: w.name || w.id, color: "var(--c2)" }));
  const keyLabel =
    keyOpts.find((k) => k.id === filters.key_id)?.label ?? filters.key_id ?? t("callsBoard.filter.allKeys");
  const wsLabel =
    wsOpts.find((w) => w.id === filters.workspace_id)?.label ??
    filters.workspace_id ??
    t("callsBoard.filter.allWorkspaces");
  const pick = (patch: Partial<CallFilters>) => {
    setOpenChip(null);
    onApply({ ...filters, ...patch });
  };
  return (
    <>
      <FilterChip
        label={filters.model || t("callsBoard.filter.allModels")}
        clearLabel={t("callsBoard.filter.statusAll")}
        selectedId={filters.model}
        options={modelOpts}
        open={openChip === "model"}
        onToggle={() => setOpenChip((c) => (c === "model" ? null : "model"))}
        onPick={(model) => pick({ model })}
        onClear={() => pick({ model: undefined })}
      />
      <FilterChip
        label={keyLabel}
        clearLabel={t("callsBoard.filter.statusAll")}
        selectedId={filters.key_id}
        options={keyOpts}
        open={openChip === "key"}
        onToggle={() => setOpenChip((c) => (c === "key" ? null : "key"))}
        onPick={(key_id) => pick({ key_id })}
        onClear={() => pick({ key_id: undefined })}
      />
      {workspaces.length > 1 || filters.workspace_id ? (
        <FilterChip
          label={wsLabel}
          clearLabel={t("callsBoard.filter.statusAll")}
          selectedId={filters.workspace_id}
          options={wsOpts}
          open={openChip === "workspace"}
          onToggle={() => setOpenChip((c) => (c === "workspace" ? null : "workspace"))}
          onPick={(workspace_id) => pick({ workspace_id })}
          onClear={() => pick({ workspace_id: undefined })}
        />
      ) : null}
    </>
  );
}

function submitDraft(filters: CallFilters, draft: Draft, onApply: (next: CallFilters) => void) {
  onApply({
    ...filters,
    model: draft.model.trim() || undefined,
    client_app: draft.client_app.trim() || undefined,
    request_id: draft.request_id.trim() || undefined,
    key_id: draft.key_id.trim() || undefined,
    workspace_id: draft.workspace_id.trim() || undefined,
    since: toInstant(draft.sinceLocal),
    until: toInstant(draft.untilLocal),
  });
}

const rangePresets: readonly RangePreset[] = ["all", "today", "d7", "d30", "month", "custom"];

function FilterSegRow({
  t,
  filters,
  models,
  keys,
  workspaces,
  onCustom,
  onApply,
}: FilterSourceProps & { onCustom: () => void }) {
  const rangeIdx = Math.max(0, rangePresets.indexOf(detectRangePreset(filters.since, filters.until)));
  return (
    <div>
      <SegControl
        ariaLabel={t("callsBoard.range.custom")}
        activeIndex={rangeIdx}
        labels={rangePresets.map((p) => t(`callsBoard.range.${p}`))}
        onPick={(i) => {
          const picked = rangePresets[i];
          if (picked === "custom") onCustom();
          else if (picked) onApply({ ...filters, ...dateRangeForPreset(picked) });
        }}
      />
      <SegControl
        ariaLabel={t("callsBoard.filter.status")}
        activeIndex={statusIndex(filters)}
        labels={[
          t("callsBoard.filter.statusAll"),
          t("callsBoard.filter.statusSuccess"),
          t("callsBoard.filter.statusFallback"),
          t("callsBoard.filter.statusError"),
        ]}
        onPick={(i) => onApply({ ...filters, ...statusPatch(i) })}
      />
      <ChipRow t={t} filters={filters} models={models} keys={keys} workspaces={workspaces} onApply={onApply} />
    </div>
  );
}

function filterBarKey(f: CallFilters) {
  return [
    f.since ?? "",
    f.until ?? "",
    f.model ?? "",
    f.client_app ?? "",
    f.key_id ?? "",
    f.workspace_id ?? "",
    f.status ?? "",
    f.fell_back ? "1" : "",
    f.request_id ?? "",
  ].join("\0");
}

function CallsFilterForm({
  t,
  filters,
  showMore,
  onToggleMore,
  onApply,
}: {
  t: TFunction;
  filters: CallFilters;
  showMore: boolean;
  onToggleMore: () => void;
  onApply: (next: CallFilters) => void;
}) {
  const [draft, setDraft] = useState<Draft>(() => draftFromFilters(filters));
  return (
    <form
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        submitDraft(filters, draft, onApply);
      }}
    >
      <FilterInput
        label={t("callsBoard.filter.model")}
        placeholder={t("callsBoard.filter.modelPlaceholder")}
        value={draft.model}
        onChange={(v) => setDraft((d) => ({ ...d, model: v }))}
      />
      <LogsBtn
        t={(k) => btnZh[k]}
        values={{
          btnStyle: "margin-right: 8px; margin-bottom: 8px;",
          onClick: () => submitDraft(filters, draft, onApply),
        }}
        text={{ label: t("callsBoard.filter.apply") }}
      />
      <LogsBtn
        t={(k) => btnZh[k]}
        values={{ btnStyle: "margin-right: 8px; margin-bottom: 8px;", onClick: onToggleMore }}
        text={{ label: t("callsBoard.filter.more") }}
      />
      <input type="submit" hidden />
      <Link to="/calls" search={clearSearch}>
        {t("callsBoard.filter.resetShort")}
      </Link>
      {showMore ? <ExtraFields t={t} draft={draft} setDraft={setDraft} /> : null}
    </form>
  );
}

export function CallsFilterBar({ t, filters, models, keys, workspaces, onApply }: FilterSourceProps) {
  const hasAdvanced = Boolean(filters.client_app || filters.request_id || filters.since || filters.until);
  const [showMore, setShowMore] = useState(hasAdvanced);
  useEffect(() => {
    if (hasAdvanced) setShowMore(true);
  }, [hasAdvanced]);
  return (
    <section aria-label={t("callsBoard.filter.more")}>
      <FilterSegRow
        t={t}
        filters={filters}
        models={models}
        keys={keys}
        workspaces={workspaces}
        onCustom={() => setShowMore(true)}
        onApply={onApply}
      />
      <CallsFilterForm
        key={filterBarKey(filters)}
        t={t}
        filters={filters}
        showMore={showMore}
        onToggleMore={() => setShowMore((v) => !v)}
        onApply={onApply}
      />
    </section>
  );
}
