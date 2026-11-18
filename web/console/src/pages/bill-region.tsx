import BillDesktop, { type Props as BillProps } from "@axiom/boards/AxApp/regions/Bill";
import BillEn from "@axiom/boards/AxApp/regions/Bill.en";
import BillZh from "@axiom/boards/AxApp/regions/Bill.zh";
import BillPhone from "@axiom/boards/AxAppPhone/regions/Bill";
import { useKeptPage } from "@axiom/ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type BoardKind, useBoardKind } from "./bill-data";

export type BillRegionProps = {
  boardKind?: BoardKind;
  text: BillProps["text"];
  rows?: BillProps["rows"];
  values?: BillProps["values"];
  tabs?: ReactNode;
  extra?: ReactNode;
};

const EMPTY_ROWS: BillProps["rows"] = { quick0: [], cols0: [], entries0: [] };

export function BillRegion({ boardKind, text, rows = EMPTY_ROWS, values, tabs, extra }: BillRegionProps) {
  const { i18n } = useTranslation();
  const english = (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
  const messages: Record<string, string> = english ? BillEn : BillZh;
  const billT = (key: keyof typeof BillZh) => messages[key] ?? "";
  const detected = useBoardKind();
  const kind = boardKind ?? detected;
  const Region = kind === "phone" ? BillPhone : BillDesktop;
  const { page, slot } = useKeptPage(extra);
  const { page: keptTabs, slot: tabsSlot } = useKeptPage(tabs);
  const pageStyle =
    kind === "phone"
      ? "width: 100%; padding: 14px 16px 120px"
      : "width: 100%; max-width: 1180px; margin: 0 auto; padding: 30px 40px 96px";
  return (
    <>
      <Region t={billT} text={text} rows={rows} values={{ pageStyle, ...values }} tabs={tabsSlot}>
        {slot}
      </Region>
      {keptTabs}
      {page}
    </>
  );
}
