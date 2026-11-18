import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CopyButton } from "./copy-button";
import s from "./id.module.css";
import { TextLink } from "./text-link";

// IdText shows an identifier as both of its ends: two IDs that share a long prefix usually differ at the tail, and a
// narrow column can spare only the middle. The head truncates when the space runs out, so an ID that fits is shown
// whole; the full value is in the title (hover) and in the text a screen reader reads.
export function IdText({ value }: { value: string }) {
  // Eight characters of tail are enough to tell apart the IDs the contract issues; a short ID keeps half of itself.
  const tail = Math.min(8, Math.floor(value.length / 2));
  return (
    <span className={s.text} title={value}>
      <span className={s.srOnly}>{value}</span>
      <span aria-hidden="true" className={s.head}>
        {value.slice(0, value.length - tail)}
      </span>
      <span aria-hidden="true" className={s.tail}>
        {value.slice(value.length - tail)}
      </span>
    </span>
  );
}

// Id is IdText with the button that copies the whole value. A cell whose ID is a link puts IdText inside the link and
// the copy button beside it, so a button never sits inside a link.
export function Id({ value }: { value: string }) {
  const { t } = useTranslation("common");
  return (
    <span className={s.id}>
      <IdText value={value} />
      <CopyButton compact text={value} label={t("copy.id")} />
    </span>
  );
}

// IdLink is an ID that leads somewhere, with the copy button beside the link, never inside it, on one line: link
// receives the ID's text and returns the router link around it.
export function IdLink({ value, link }: { value: string; link: (content: ReactNode) => ReactNode }) {
  const { t } = useTranslation("common");
  return (
    <span className={s.id}>
      <TextLink>{link(<IdText value={value} />)}</TextLink>
      <CopyButton compact text={value} label={t("copy.id")} />
    </span>
  );
}
