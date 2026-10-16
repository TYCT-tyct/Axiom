import { Panel } from "@axiom/ui";
import type { ReactNode } from "react";

// Stat is one figure the user looks at first: what it is, then the number, large. The number may be as long as an
// amount can be, so it wraps rather than pushing the page wider.
export function Stat({ label, value, note }: { label: string; value: string; note?: ReactNode }) {
  return (
    <Panel className="p-4">
      <p className="text-label text-t3">{label}</p>
      <p className="mt-1.5 text-xl font-medium tracking-[-0.01em] text-t1 tabular-nums [overflow-wrap:anywhere] sm:text-2xl">
        {value}
      </p>
      {note ? <p className="mt-1 text-ui text-t3">{note}</p> : null}
    </Panel>
  );
}
