import { Toast } from "@axiom/ui";
import { useSyncExternalStore } from "react";

// The admin's one-line feedback after a change (this.say in admin_core.js): a change made from a dialog that has since
// closed says how it went here, with its undo when it has one. One toast shows at a time, the latest; a new one replaces
// what is showing, so an undo is never offered for a change older than the one just made.
type Said = { id: number; message: string; kind: "ok" | "bad"; action?: { label: string; onClick: () => void } };

let showing: Said | undefined;
let made = 0;
const watchers = new Set<() => void>();
const tell = () => {
  for (const watcher of watchers) watcher();
};

export function say(message: string, options: Pick<Said, "kind" | "action"> = { kind: "ok" }) {
  made += 1;
  showing = { id: made, message, ...options };
  tell();
}

function dismiss(id: number) {
  if (showing?.id !== id) return;
  showing = undefined;
  tell();
}

const subscribe = (watcher: () => void) => {
  watchers.add(watcher);
  return () => void watchers.delete(watcher);
};

// Toasts is where the toast shows: mounted once beside the router, so it outlives the page and dialog that said it.
export function Toasts() {
  const said = useSyncExternalStore(subscribe, () => showing);
  if (!said) return null;
  return (
    <Toast
      key={said.id}
      message={said.message}
      kind={said.kind}
      action={said.action}
      duration={said.action ? 5000 : undefined}
      onDismiss={() => dismiss(said.id)}
    />
  );
}
