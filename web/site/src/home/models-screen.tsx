import Wide, { type Props } from "@axiom/boards/Site/regions/Models";
import wideEn from "@axiom/boards/Site/regions/Models.en";
import wideZh from "@axiom/boards/Site/regions/Models.zh";
import WideRow, { type Props as RowProps } from "@axiom/boards/Site/regions/ModelsRow";
import wideRowEn from "@axiom/boards/Site/regions/ModelsRow.en";
import wideRowZh from "@axiom/boards/Site/regions/ModelsRow.zh";
import Title from "@axiom/boards/Site/regions/ModelsTitle";
import titleEn from "@axiom/boards/Site/regions/ModelsTitle.en";
import titleZh from "@axiom/boards/Site/regions/ModelsTitle.zh";
import Narrow from "@axiom/boards/SitePhone/regions/Models";
import narrowEn from "@axiom/boards/SitePhone/regions/Models.en";
import narrowZh from "@axiom/boards/SitePhone/regions/Models.zh";
import NarrowRow from "@axiom/boards/SitePhone/regions/ModelsRow";
import narrowRowEn from "@axiom/boards/SitePhone/regions/ModelsRow.en";
import narrowRowZh from "@axiom/boards/SitePhone/regions/ModelsRow.zh";
import { DataState, EmptyState, useFormat } from "@axiom/ui";
import { useLayoutEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { links } from "./links";
import { start } from "./models-script";
import { screenVars, useBoardWords, useScript } from "./script";
import styles from "./models-screen.module.css";

const zh = { ...wideZh, ...wideRowZh, ...titleZh, ...narrowZh, ...narrowRowZh };
const en = { ...wideEn, ...wideRowEn, ...titleEn, ...narrowEn, ...narrowRowEn };

const vars = screenVars(
  "--ok: var(--ok)",
  "--warn: var(--warn)",
  "--bad: var(--bad)",
  "--failText: var(--badT)",
  "--pickRing: light-dark(rgba(0, 162, 199, 0.22), rgba(0, 162, 199, 0.32))",
);
// The columns the API cannot fill, whose figures are the board's kind of sample (models-script.ts): their headings say so.
const sampled = new Set<keyof typeof zh>(["mddab042697", "m51694b10eb", "m16ece33056"]);

// The models-and-routes screen (Site.dc.html M2): the site's models and their routes from listPublicModels, and in
// place of the list and the panel the state of that call until it has models to show. onWhale is the whale's easter
// egg, called when a DeepSeek model is pointed at; the whale comes with S2b.
export function ModelsScreen({ wide, onWhale = () => {} }: { wide: boolean; onWhale?: () => void }) {
  const { t, i18n } = useTranslation();
  const format = useFormat();
  // the script draws on every frame of its tweens: the names are made once per language
  const names = useMemo(() => new Intl.DisplayNames(i18n.language, { type: "currency" }), [i18n.language]);
  const { setStage, script, values } = useScript(
    (stage: HTMLElement, changed: () => void) => start(stage, changed, !wide, onWhale),
    {
      t: (word, options) => t(`script.models.${word}`, options),
      money: format.money,
      currency: (code) => names.of(code) ?? code,
    },
  );
  useLayoutEffect(() => script?.layout());
  const word = useBoardWords(zh, en);
  const board = (key: keyof typeof zh) => {
    const text = word(key);
    return sampled.has(key) ? `${text} · ${t("script.models.sample")}` : text;
  };
  const v = { ...values, m2_vars: vars, m2_setStage: setStage };
  const Region = wide ? Wide : Narrow;
  const Row = wide ? WideRow : NarrowRow;
  const query = script?.query();
  return (
    <Region
      t={board}
      values={v as Props["values"]}
      title={i18n.language === "zh" ? <Title t={board} /> : board("m78d48a1986")}
      row={
        query?.status === "success" && query.data?.length ? (
          <Row
            t={board}
            values={v as RowProps["values"]}
            text={{ unit: String(values?.unit ?? ""), moreHref: links.models }}
          />
        ) : query ? (
          <div className={styles.state}>
            <DataState
              query={query}
              isEmpty={(models) => models.length === 0}
              empty={<EmptyState title={t("script.models.emptyTitle")}>{t("script.models.emptyBody")}</EmptyState>}
            >
              {() => null}
            </DataState>
          </div>
        ) : null
      }
    />
  );
}
