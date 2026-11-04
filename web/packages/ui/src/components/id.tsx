import { useTranslation } from "react-i18next";
import { cn } from "../cn";
import { CopyButton } from "./copy-button";

// IdText shows an identifier as both of its ends: two IDs that share a long prefix usually differ at the tail, and a
// narrow column can spare only the middle. The head truncates when the space runs out, so an ID that fits is shown
// whole; the full value is in the title (hover) and in the text a screen reader reads.
export function IdText({ value, className }: { value: string; className?: string }) {
  // Eight characters of tail are enough to tell apart the IDs the contract issues; a short ID keeps half of itself.
  const tail = Math.min(8, Math.floor(value.length / 2));
  return (
    <span className={cn("inline-flex min-w-0 font-mono text-label", className)} title={value}>
      <span className="sr-only">{value}</span>
      <span aria-hidden="true" className="min-w-0 truncate">
        {value.slice(0, value.length - tail)}
      </span>
      <span aria-hidden="true" className="shrink-0">
        {value.slice(value.length - tail)}
      </span>
    </span>
  );
}

// Id is IdText with the button that copies the whole value. A cell whose ID is a link puts IdText inside the link and
// the copy button beside it, so a button never sits inside a link.
export function Id({ value, className }: { value: string; className?: string }) {
  const { t } = useTranslation("common");
  return (
    <span className={cn("inline-flex min-w-0 max-w-full items-center gap-1 align-middle text-t2", className)}>
      <IdText value={value} />
      <CopyButton compact text={value} label={t("copy.id")} />
    </span>
  );
}
