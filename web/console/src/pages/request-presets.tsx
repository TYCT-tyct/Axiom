import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import Desktop from "@axiom/boards/AxApp/regions/RequestPresets";
import desktopEn from "@axiom/boards/AxApp/regions/RequestPresets.en";
import desktopZh from "@axiom/boards/AxApp/regions/RequestPresets.zh";
import State from "@axiom/boards/AxApp/regions/RequestPresetsState";
import stateEn from "@axiom/boards/AxApp/regions/RequestPresetsState.en";
import stateZh from "@axiom/boards/AxApp/regions/RequestPresetsState.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/RequestPresets";
import { usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { type KeyboardEvent, type MouseEvent, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { requestPresetText } from "../locales/request-presets";
import { PresetDialog } from "./request-presets-dialog";

type Row = Parameters<typeof Desktop>[0]["rows"]["presets0"][number];
const listKey = [accountKey, "request-presets"] as const;
const path = "/api/console/v1/presets";

const phoneQuery = "(max-width: 819px)";
function subscribePhone(notify: () => void) {
  const query = matchMedia(phoneQuery);
  query.addEventListener("change", notify);
  return () => query.removeEventListener("change", notify);
}

export default function RequestPresetsPage() {
  const { i18n } = useTranslation();
  const session = useSession();
  const view = can(session, "api_keys.view");
  const manage = can(session, "api_keys.manage");
  const [selected, setSelected] = useState<{ slug: string; opener: HTMLElement }>();
  const list = useQuery({
    queryKey: listKey,
    // The server permits at most 100 live configurations per account.
    queryFn: ({ signal }) => unwrap(consoleApi.GET(path, { params: { query: { limit: 100 } }, signal })),
    enabled: view,
  });
  const language = i18n.resolvedLanguage ?? i18n.language;
  usePageTitle(requestPresetText(language).title);
  const denied = !view || (list.error instanceof ApiError && list.error.status === 403);
  const open = (event: MouseEvent<Element>) => {
    const opener = event.currentTarget as HTMLElement;
    setSelected({ slug: opener.dataset.slug ?? "", opener });
  };
  const data = denied || list.isError ? [] : (list.data?.data ?? []).filter((preset) => preset.slug);
  const problem = listProblem(denied, list.isError, list.isSuccess && !data.length);
  return (
    <>
      <PresetBoard
        data={data}
        loading={view && list.isPending}
        language={language}
        readOnly={denied || !manage}
        open={open}
      />
      <ListState language={language} problem={problem} retry={() => void list.refetch()} />
      {selected ? (
        <PresetDialog
          slug={selected.slug}
          opener={selected.opener}
          manage={manage}
          listKey={listKey}
          language={language}
          onClose={() => setSelected(undefined)}
        />
      ) : null}
    </>
  );
}

function listProblem(denied: boolean, failed: boolean, empty: boolean) {
  if (denied) return "denied";
  if (failed) return "failed";
  return empty ? "empty" : undefined;
}

function cardKey(event: KeyboardEvent<Element>) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    (event.currentTarget as HTMLElement).click();
  }
}

function PresetBoard({
  data,
  loading,
  language,
  readOnly,
  open,
}: {
  data: Schemas["Preset"][];
  loading: boolean;
  language: string;
  readOnly: boolean;
  open: (event: MouseEvent<Element>) => void;
}) {
  const phone = useSyncExternalStore(subscribePhone, () => matchMedia(phoneQuery).matches);
  const Board = phone ? Phone : Desktop;
  const text = requestPresetText(language);
  const labels = language.startsWith("en")
    ? desktopEn
    : { ...desktopZh, m181bcfdf83: text.introEnd, mdfd719cbad: text.exampleHint };
  const rows = (loading ? [{}, {}] : data).map((preset, index) => row(preset, index, language));
  return (
    <div className={loading ? "pstL" : undefined} aria-busy={loading}>
      <Board
        t={(key) => labels[key]}
        text={{ title: text.title, create: text.create, example: `"@preset/${data[0]?.slug ?? ""}"` }}
        rows={{ presets0: rows }}
        values={{
          layout: phone ? "padding: 14px 16px 0" : "padding: 30px 40px 0; max-width: 1180px; margin: 0 auto",
          wrap: "overflow-wrap: anywhere",
          readOnly,
          noExample: !data.length,
          ppNewPre: open,
          ppPre: loading ? undefined : open,
          cardRole: loading ? "" : "button",
          cardTab: loading ? -1 : 0,
          cardKey,
        }}
      />
      {loading ? <span role="status" aria-label={text.loading} /> : null}
    </div>
  );
}

function ListState({
  language,
  problem,
  retry,
}: {
  language: string;
  problem?: "denied" | "failed" | "empty";
  retry: () => void;
}) {
  if (!problem) return null;
  const failed = problem === "failed";
  const denied = problem === "denied";
  const text = requestPresetText(language);
  return (
    <State
      t={(key) => (language.startsWith("en") ? stateEn : stateZh)[key]}
      values={{
        tone: failed ? "err" : denied ? "deny" : "empty",
        stateRole: failed ? "alert" : "status",
        noAction: !failed,
        retry,
      }}
      text={{
        title: failed ? text.failed : denied ? text.forbidden : text.empty,
        detail: failed ? text.failedHint : denied ? text.forbiddenHint : text.emptyHint,
        action: text.retry,
      }}
    />
  );
}

function row(preset: Schemas["Preset"], index: number, language: string): Row {
  const config = preset.config ?? {};
  const fallback = Array.isArray(config.models) ? config.models.filter((name) => typeof name === "string") : [];
  const model = typeof config.model === "string" ? config.model : fallback.shift();
  const reference = preset.slug ? `@preset/${preset.slug}` : " ";
  const name = preset.name && preset.name !== preset.slug ? `${preset.name} · ${reference}` : reference;
  return {
    key: preset.slug ?? String(index),
    kind: "0",
    f0: "qpre",
    f1: preset.slug ?? "",
    f2: "qpreh",
    f3: "mono",
    f4: name,
    f5: "qmn",
    f6: "qmd",
    f7: `background: var(--c${index % 5});`,
    f8: model ?? "",
    f9: typeof config.system === "string" ? config.system : "",
    f10: "mut",
    f11: summary(config, fallback, language),
  };
}

function summary(config: Schemas["PresetConfig"], fallback: string[], language: string) {
  const english = language.startsWith("en");
  const fields = [];
  if (typeof config.temperature === "number") {
    fields.push(`${english ? "Temperature" : "温度"} ${config.temperature.toLocaleString(language)}`);
  }
  const max = config.max_tokens ?? config.max_completion_tokens ?? config.max_output_tokens;
  if (typeof max === "number") {
    fields.push(english ? `Max ${max.toLocaleString(language)} tokens` : `最长 ${max.toLocaleString(language)} Token`);
  }
  if (fallback.length) fields.push(`${requestPresetText(language).follow} ${fallback.join(", ")}`);
  return fields.join(english ? ", " : "，");
}
