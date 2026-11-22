import type { Schemas } from "@axiom/api";
import LogsBtn from "@axiom/boards/AxApp/regions/LogsBtn";
import btnZh from "@axiom/boards/AxApp/regions/LogsBtn.zh";
import LogsFilters from "@axiom/boards/AxApp/regions/LogsFilters";
import filtersZh from "@axiom/boards/AxApp/regions/LogsFilters.zh";
import LogsSeg from "@axiom/boards/AxApp/regions/LogsSeg";
import segZh from "@axiom/boards/AxApp/regions/LogsSeg.zh";
import { useBoardMedia } from "@axiom/ui";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { phoneWidth } from "../board-interaction";
import type { CallFilters } from "../queries";
import {
  dateRangeForPreset,
  modelDotColor,
  type PresetChoice,
  type RangePreset,
  resolveRangePreset,
} from "./calls-data";
import {
  clearSearch,
  type Draft,
  draftFromFilters,
  ExtraFields,
  FilterInput,
  type FilterPatch,
  submitDraft,
  zeroBarStyle,
} from "./calls-filter-fields";
import { segClickIndex } from "./calls-states";

type ChipOption = { id: string; label: string; color?: string };
type SegItem = { key: string; kind: "0"; f0: "button"; f1: string; f2: string };

const loadMoreKeysToken = "__load_more_keys__";
const inlineBarStyle =
  "display: inline-block; overflow: visible; position: relative; vertical-align: top; padding: 0; margin-right: 8px; margin-bottom: 8px;";
const chipFrameStyle = (open: boolean) => `${inlineBarStyle} z-index: ${open ? 40 : 25};`;
const segWrapStyle = "margin-right: 8px; margin-bottom: 8px; vertical-align: middle;";

function segItems(labels: readonly string[], activeIndex: number): SegItem[] {
  return labels.map((label, i) => ({
    key: `${i}-${label}`,
    kind: "0",
    f0: "button",
    f1: i === activeIndex ? "on" : "",
    f2: label,
  }));
}

// SegControl is one row of equal options, or with rows two, for more options than a phone's width holds in one: the
// columns, and the column and row of the active option, are what the region's thumb is placed by.
function SegControl({
  labels,
  activeIndex,
  ariaLabel,
  rows = 1,
  onPick,
}: {
  labels: readonly string[];
  activeIndex: number;
  ariaLabel: string;
  rows?: 1 | 2;
  onPick: (index: number) => void;
}) {
  const columns = Math.ceil(labels.length / rows);
  const active = Math.max(0, activeIndex);
  return (
    <LogsSeg
      t={(k) => segZh[k]}
      values={{
        ariaLabel,
        rows,
        segStyle: `--n: ${columns}; --i: ${active % columns}; --r: ${Math.floor(active / columns)}; ${segWrapStyle}`,
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

function statusIndex(filters: CallFilters) {
  if (filters.status === "success") return 1;
  if (filters.fell_back) return 2;
  if (filters.status === "error") return 3;
  return 0;
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

type FilterSourceProps = {
  t: TFunction;
  filters: CallFilters;
  preset?: PresetChoice;
  canKeys: boolean;
  hasMoreKeys: boolean;
  onLoadMoreKeys: () => void;
  models: Schemas["PublicModel"][];
  keys: Schemas["Key"][];
  workspaces: Schemas["Workspace"][];
  onApply: (next: FilterPatch) => void;
};

function chipViewModel(p: FilterSourceProps) {
  const modelOpts = p.models.map((m) => ({ id: m.id, label: m.id, color: modelDotColor(m.id) }));
  const keyOpts: ChipOption[] = p.keys.map((k) => ({ id: k.id, label: k.name || k.id, color: "var(--c1)" }));
  if (p.hasMoreKeys) {
    keyOpts.push({ id: loadMoreKeysToken, label: p.t("callsBoard.filter.loadMoreKeys"), color: "transparent" });
  }
  const wsOpts = p.workspaces.map((w) => ({ id: w.id, label: w.name || w.id, color: "var(--c2)" }));
  return {
    modelOpts,
    keyOpts,
    wsOpts,
    keyLabel:
      keyOpts.find((k) => k.id === p.filters.key_id)?.label ?? p.filters.key_id ?? p.t("callsBoard.filter.allKeys"),
    wsLabel:
      wsOpts.find((w) => w.id === p.filters.workspace_id)?.label ??
      p.filters.workspace_id ??
      p.t("callsBoard.filter.allWorkspaces"),
    showKeyChip: p.canKeys && (p.keys.length > 0 || Boolean(p.filters.key_id)),
    showWsChip: p.canKeys && (p.workspaces.length > 1 || Boolean(p.filters.workspace_id)),
  };
}

function ChipRow(p: FilterSourceProps) {
  const { openChip, setOpenChip } = useChipDropdowns();
  const vm = chipViewModel(p);
  const pick = (patch: FilterPatch) => {
    setOpenChip(null);
    p.onApply({ ...p.filters, preset: p.preset, ...patch });
  };
  return (
    <>
      <FilterChip
        label={p.filters.model || p.t("callsBoard.filter.allModels")}
        clearLabel={p.t("callsBoard.filter.statusAll")}
        selectedId={p.filters.model}
        options={vm.modelOpts}
        open={openChip === "model"}
        onToggle={() => setOpenChip((c) => (c === "model" ? null : "model"))}
        onPick={(model) => pick({ model })}
        onClear={() => pick({ model: undefined })}
      />
      {vm.showKeyChip ? (
        <FilterChip
          label={vm.keyLabel}
          clearLabel={p.t("callsBoard.filter.statusAll")}
          selectedId={p.filters.key_id}
          options={vm.keyOpts}
          open={openChip === "key"}
          onToggle={() => setOpenChip((c) => (c === "key" ? null : "key"))}
          onPick={(key_id) => (key_id === loadMoreKeysToken ? p.onLoadMoreKeys() : pick({ key_id }))}
          onClear={() => pick({ key_id: undefined })}
        />
      ) : null}
      {vm.showWsChip ? (
        <FilterChip
          label={vm.wsLabel}
          clearLabel={p.t("callsBoard.filter.statusAll")}
          selectedId={p.filters.workspace_id}
          options={vm.wsOpts}
          open={openChip === "workspace"}
          onToggle={() => setOpenChip((c) => (c === "workspace" ? null : "workspace"))}
          onPick={(workspace_id) => pick({ workspace_id })}
          onClear={() => pick({ workspace_id: undefined })}
        />
      ) : null}
    </>
  );
}

const rangePresets: readonly RangePreset[] = ["all", "today", "d7", "d30", "month", "custom"];

function FilterSegRow(p: FilterSourceProps & { customPicked: boolean; onPickPreset: (next: RangePreset) => void }) {
  const detected = resolveRangePreset(p.filters.since, p.filters.until, p.preset);
  const activePreset = p.customPicked ? "custom" : detected;
  const rangeIdx = Math.max(0, rangePresets.indexOf(activePreset));
  // Six ranges do not fit a phone's width in one row in either language, even in the board's short words.
  const phone = useBoardMedia(phoneWidth);
  return (
    <div>
      <SegControl
        ariaLabel={p.t("callsBoard.range.custom")}
        activeIndex={rangeIdx}
        rows={phone ? 2 : 1}
        labels={rangePresets.map((r) => p.t(`callsBoard.range.${r}`))}
        onPick={(i) => {
          const picked = rangePresets[i];
          if (picked) p.onPickPreset(picked);
        }}
      />
      <SegControl
        ariaLabel={p.t("callsBoard.filter.status")}
        activeIndex={statusIndex(p.filters)}
        labels={[
          p.t("callsBoard.filter.statusAll"),
          p.t("callsBoard.filter.statusSuccess"),
          p.t("callsBoard.filter.statusFallback"),
          p.t("callsBoard.filter.statusError"),
        ]}
        onPick={(i) => p.onApply({ ...p.filters, preset: p.preset, ...statusPatch(i) })}
      />
      <ChipRow {...p} />
    </div>
  );
}

function filterBarKey(f: CallFilters, preset?: PresetChoice) {
  return [
    f.since ?? "",
    f.until ?? "",
    preset ?? "",
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
  preset,
  showMore,
  onToggleMore,
  onReset,
  onApply,
}: {
  t: TFunction;
  filters: CallFilters;
  preset?: PresetChoice;
  showMore: boolean;
  onToggleMore: () => void;
  onReset: () => void;
  onApply: (next: FilterPatch) => void;
}) {
  const [draft, setDraft] = useState<Draft>(() => draftFromFilters(filters));
  const apply = () => submitDraft({ ...filters, preset }, draft, onApply);
  return (
    <form
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        apply();
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
          onClick: apply,
        }}
        text={{ label: t("callsBoard.filter.apply") }}
      />
      <LogsBtn
        t={(k) => btnZh[k]}
        values={{ btnStyle: "margin-right: 8px; margin-bottom: 8px;", onClick: onToggleMore }}
        text={{ label: t("callsBoard.filter.more") }}
      />
      <input type="submit" hidden />
      <Link
        to="/calls"
        search={clearSearch}
        onClick={() => {
          setDraft(draftFromFilters(clearSearch));
          onReset();
        }}
      >
        {t("callsBoard.filter.resetShort")}
      </Link>
      {showMore ? <ExtraFields t={t} draft={draft} setDraft={setDraft} /> : null}
    </form>
  );
}

export function CallsFilterBar(p: FilterSourceProps) {
  const missingKeyOption = Boolean(p.filters.key_id && !p.keys.some((k) => k.id === p.filters.key_id));
  const hasAdvanced = Boolean(
    p.filters.client_app ||
      p.filters.request_id ||
      p.filters.since ||
      p.filters.until ||
      (!p.canKeys && Boolean(p.filters.key_id || p.filters.workspace_id)) ||
      missingKeyOption,
  );
  const [showMore, setShowMore] = useState(hasAdvanced);
  const draftKey = filterBarKey(p.filters, p.preset);
  const [customDraftKey, setCustomDraftKey] = useState<string>();
  // Reset the date editor on navigation while keeping the range buttons mounted for keyboard focus.
  if (customDraftKey !== undefined && customDraftKey !== draftKey) setCustomDraftKey(undefined);
  useEffect(() => {
    if (hasAdvanced) setShowMore(true);
  }, [hasAdvanced]);
  const handlePickPreset = (picked: RangePreset) => {
    if (picked === "custom") {
      setCustomDraftKey(draftKey);
      setShowMore(true);
      return;
    }
    setCustomDraftKey(undefined);
    p.onApply({ ...p.filters, ...dateRangeForPreset(picked), preset: picked === "all" ? undefined : picked });
  };
  return (
    <section aria-label={p.t("callsBoard.filter.more")}>
      <FilterSegRow {...p} customPicked={customDraftKey === draftKey} onPickPreset={handlePickPreset} />
      <CallsFilterForm
        key={draftKey}
        t={p.t}
        filters={p.filters}
        preset={p.preset}
        showMore={showMore}
        onToggleMore={() => setShowMore((v) => !v)}
        onReset={() => setCustomDraftKey(undefined)}
        onApply={(next) => {
          setCustomDraftKey(undefined);
          p.onApply(next);
        }}
      />
    </section>
  );
}
