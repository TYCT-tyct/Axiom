import type { ReactNode } from "react";
import { cn } from "../cn";
import { Icon, type IconName } from "./icon";

// Panel is a titled block of a page: the card the design uses for the things the user looks at together.
export function Panel({
  id,
  title,
  action,
  children,
  className,
}: {
  id?: string;
  title?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section id={id} className={cn("min-w-0 rounded-card border border-b1 bg-s1 shadow-panel", className)}>
      {title || action ? (
        <header className="flex items-center justify-between gap-3 border-b border-b1 px-4 py-3">
          {title ? <h2 className="min-w-0 text-ui font-medium text-t1">{title}</h2> : <span />}
          {action}
        </header>
      ) : null}
      {children}
    </section>
  );
}

const tones = {
  neutral: "bg-s3 text-t2",
  ok: "bg-ok-soft text-ok",
  warn: "bg-warn-soft text-warn",
  bad: "bg-bad-soft text-bad",
} as const;

// Badge names a state in words, with an icon so the colour is never the only signal.
export function Badge({
  tone = "neutral",
  icon,
  children,
}: {
  tone?: keyof typeof tones;
  icon?: IconName;
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1 rounded-xs px-1.5 text-label whitespace-nowrap",
        tones[tone],
      )}
    >
      {icon ? <Icon name={icon} width="12" height="12" /> : null}
      {children}
    </span>
  );
}

// A table scrolls sideways inside its own box when the window is too narrow, and never widens the page. `fixed` lets a
// table whose columns are all known give each one a width and truncate long values inside it; without it, a browser
// sizes columns from their widest content and no `max-width` on a cell can make a table narrower than that.
export function Table({ children, label, fixed = false }: { children: ReactNode; label: string; fixed?: boolean }) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need focus to scroll a table region.
    <section className="overflow-x-auto" aria-label={label} tabIndex={0}>
      <table aria-label={label} className={cn("w-full border-collapse text-left text-ui", fixed && "table-fixed")}>
        {children}
      </table>
    </section>
  );
}

// A table header keeps its own line: a narrow numeric column would otherwise break a two-character label one character
// per line, which reads as two columns. A table that needs more width scrolls sideways inside its own box.
export function Th({ children, className }: { children?: ReactNode; className?: string }) {
  return (
    <th
      scope="col"
      className={cn("border-b border-b1 px-3 py-2 text-label font-medium text-t4 whitespace-nowrap sm:px-4", className)}
    >
      {children}
    </th>
  );
}

export function Td({ children, className, colSpan }: { children?: ReactNode; className?: string; colSpan?: number }) {
  return (
    <td colSpan={colSpan} className={cn("border-b border-b1 px-3 py-2.5 sm:px-4 text-t2", className)}>
      {children}
    </td>
  );
}
