import { ApiError, consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey, can, useSession } from "@axiom/app";
import Desktop from "@axiom/boards/AxApp/regions/RequestPresets";
import State from "@axiom/boards/AxApp/regions/RequestPresetsState";
import Phone from "@axiom/boards/AxAppPhone/regions/RequestPresets";
import { pressBoardButton, useBoardMedia, usePageTitle } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type MouseEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { phoneWidth } from "../board-interaction";
import type { zh } from "../locales/request-presets";
import { PresetDialog } from "./request-presets-dialog";

type Row = Parameters<typeof Desktop>[0]["rows"]["presets0"][number];
const listKey = [accountKey, "request-presets"] as const;
const path = "/api/console/v1/presets";
type Copy = typeof zh;

export default function RequestPresetsPage() {
  const { t, i18n } = useTranslation();
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
  const copy = t("requestPresets.copy", { returnObjects: true });
  usePageTitle(copy.title);
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
        copy={copy}
        t={t}
        readOnly={denied || !manage}
        open={open}
      />
      <ListState copy={copy} problem={problem} retry={() => void list.refetch()} />
      {selected ? (
        <PresetDialog
          slug={selected.slug}
          opener={selected.opener}
          manage={manage}
          listKey={listKey}
          copy={copy}
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

function PresetBoard({
  data,
  loading,
  language,
  copy,
  t,
  readOnly,
  open,
}: {
  data: Schemas["Preset"][];
  loading: boolean;
  language: string;
  copy: Copy;
  t: TFunction;
  readOnly: boolean;
  open: (event: MouseEvent<Element>) => void;
}) {
  const phone = useBoardMedia(phoneWidth);
  const Board = phone ? Phone : Desktop;
  const labels: Record<string, string> = {
    m3187029f2b: copy.introStart,
    m98472d23f3: copy.introReference,
    m181bcfdf83: copy.introEnd,
    m74ed19426d: copy.exampleTitle,
    mdfd719cbad: copy.exampleHint,
  };
  const rows = (loading ? [{}, {}] : data).map((preset, index) => row(preset, index, { copy, language, t }));
  return (
    <div aria-busy={loading}>
      <Board
        t={(key) => labels[key] ?? ""}
        text={{ title: copy.title, create: copy.create, example: `"@preset/${data[0]?.slug ?? ""}"` }}
        rows={{ presets0: rows }}
        values={{
          loadingClass: loading ? "pstL" : "",
          layout: phone ? "padding: 14px 16px 0" : "padding: 30px 40px 0; max-width: 1180px; margin: 0 auto",
          wrap: "overflow-wrap: anywhere",
          readOnly,
          noExample: !data.length,
          ppNewPre: open,
          ppPre: loading ? undefined : open,
          cardRole: loading ? "" : "button",
          cardTab: loading ? -1 : 0,
          cardKey: pressBoardButton,
        }}
      />
      {loading ? <span role="status" aria-label={copy.loading} /> : null}
    </div>
  );
}

function ListState({
  copy,
  problem,
  retry,
}: {
  copy: Copy;
  problem?: "denied" | "failed" | "empty";
  retry: () => void;
}) {
  if (!problem) return null;
  const failed = problem === "failed";
  const denied = problem === "denied";
  return (
    <State
      t={() => ""}
      values={{
        tone: failed ? "err" : denied ? "deny" : "empty",
        stateRole: failed ? "alert" : "status",
        noAction: !failed,
        retry,
      }}
      text={{
        title: failed ? copy.failed : denied ? copy.forbidden : copy.empty,
        detail: failed ? copy.failedHint : denied ? copy.forbiddenHint : copy.emptyHint,
        action: copy.retry,
      }}
    />
  );
}

type SummaryCopy = { copy: Copy; language: string; t: TFunction };

function row(preset: Schemas["Preset"], index: number, labels: SummaryCopy): Row {
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
    f11: summary(config, fallback, labels),
  };
}

function summary(config: Schemas["PresetConfig"], fallback: string[], { copy, language, t }: SummaryCopy) {
  const fields = [];
  if (typeof config.temperature === "number") {
    fields.push(`${copy.temperature} ${config.temperature.toLocaleString(language)}`);
  }
  const max = config.max_tokens ?? config.max_completion_tokens ?? config.max_output_tokens;
  if (typeof max === "number") {
    fields.push(t("requestPresets.copy.maxSummary", { value: max.toLocaleString(language) }));
  }
  if (fallback.length) fields.push(`${copy.follow} ${fallback.join(", ")}`);
  return fields.join(language.startsWith("en") ? ", " : "，");
}
