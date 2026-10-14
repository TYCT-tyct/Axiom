import type { ReactNode } from "react";
import { Icon } from "./icon";

// Notice is a one-line message across the top of the page that says what changed and what to do about it.
export function Notice({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-accent-line bg-accent-soft px-4 py-2 text-ui text-t1"
    >
      <Icon name="refresh" className="shrink-0 text-accent" />
      <span className="min-w-0 flex-1">{children}</span>
      {action}
    </div>
  );
}
