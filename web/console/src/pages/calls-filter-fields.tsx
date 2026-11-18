import { toInstant, toLocalInput } from "@axiom/app";
import LogsFilters from "@axiom/boards/AxApp/regions/LogsFilters";
import filtersZh from "@axiom/boards/AxApp/regions/LogsFilters.zh";
import type { TFunction } from "i18next";
import type { CallFilters } from "../queries";
import type { PresetChoice } from "./calls-data";

export type FilterPatch = Partial<CallFilters> & { preset?: PresetChoice };

const inputFrameStyle =
  "display: inline-block; min-width: 190px; max-width: 260px; padding: 0; margin-right: 8px; margin-bottom: 8px; vertical-align: middle;";
export const zeroBarStyle = "margin: 0;";

export const clearSearch = {
  since: undefined,
  until: undefined,
  preset: undefined,
  model: undefined,
  client_app: undefined,
  key_id: undefined,
  workspace_id: undefined,
  status: undefined,
  fell_back: undefined,
  request_id: undefined,
  call: undefined,
};

export type Draft = {
  model: string;
  client_app: string;
  request_id: string;
  key_id: string;
  workspace_id: string;
  sinceLocal: string;
  untilLocal: string;
};

export function draftFromFilters(f: CallFilters): Draft {
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

function draftInstant(local: string, previous?: string) {
  return local && local === toLocalInput(previous) ? previous : toInstant(local);
}

export function submitDraft(filters: FilterPatch, draft: Draft, onApply: (next: FilterPatch) => void) {
  const since = draftInstant(draft.sinceLocal, filters.since);
  const until = draftInstant(draft.untilLocal, filters.until);
  onApply({
    ...filters,
    preset: since === filters.since && until === filters.until ? filters.preset : undefined,
    model: draft.model.trim() || undefined,
    client_app: draft.client_app.trim() || undefined,
    request_id: draft.request_id.trim() || undefined,
    key_id: draft.key_id.trim() || undefined,
    workspace_id: draft.workspace_id.trim() || undefined,
    since,
    until,
  });
}

export function FilterInput({
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

export function ExtraFields({
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
        label={t("callsBoard.filter.keyId")}
        placeholder={t("callsBoard.filter.keyIdPlaceholder")}
        value={draft.key_id}
        onChange={(v) => setDraft((d) => ({ ...d, key_id: v }))}
      />
      <FilterInput
        label={t("callsBoard.filter.workspaceId")}
        placeholder={t("callsBoard.filter.workspaceIdPlaceholder")}
        value={draft.workspace_id}
        onChange={(v) => setDraft((d) => ({ ...d, workspace_id: v }))}
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
