import { useCallback, useMemo, useSyncExternalStore } from "react";

// A page's list state (the selected row, the filters) lives in the address's search params, so reload, back and forward
// and a pasted link keep it. Every value is text; the caller types it per page with its defaults and, where a param has
// a closed set, its allowed values. A param equal to its default is left out of the address, so a clean list has a
// clean URL, and a value outside its set reads as the default, so a hand-edited link cannot put the page in a state it
// has no words for.
//
// This is the History API's own state, shared by every user of the hook through one event. A page that already keeps
// its search in a router passes the same values and handlers to the components itself.
const changed = "axiom:search";

function subscribe(notify: () => void) {
  window.addEventListener("popstate", notify);
  window.addEventListener(changed, notify);
  return () => {
    window.removeEventListener("popstate", notify);
    window.removeEventListener(changed, notify);
  };
}

type Allowed<K extends string> = Partial<Record<K, readonly string[]>>;

function readParams<K extends string>(search: string, defaults: Record<K, string>, allowed: Allowed<K> = {}) {
  const params = new URLSearchParams(search);
  const values = { ...defaults };
  for (const key of Object.keys(defaults) as K[]) {
    const raw = params.get(key);
    if (raw !== null && (allowed[key] === undefined || allowed[key].includes(raw))) {
      values[key] = raw;
    }
  }
  return values;
}

type Change<K extends string> = Partial<Record<K, string>>;

export function useUrlParams<K extends string>(defaults: Record<K, string>, allowed?: Allowed<K>) {
  const search = useSyncExternalStore(
    subscribe,
    () => window.location.search,
    () => "",
  );
  const values = useMemo(() => readParams(search, defaults, allowed), [search, defaults, allowed]);
  // push adds a history entry, for a choice the back button should undo (a selected row); replace rewrites the current
  // one, for what changes continuously (typing in a search box, arrow keys through a list).
  const set = useCallback(
    (change: Change<K>, mode: "push" | "replace" = "replace") => {
      const params = new URLSearchParams(window.location.search);
      for (const [key, value] of Object.entries<string | undefined>(change)) {
        if (value === undefined || value === defaults[key as K]) {
          params.delete(key);
        } else {
          params.set(key, value);
        }
      }
      const query = params.toString();
      const url = `${window.location.pathname}${query ? `?${query}` : ""}${window.location.hash}`;
      if (url === `${window.location.pathname}${window.location.search}${window.location.hash}`) {
        return;
      }
      window.history[mode === "push" ? "pushState" : "replaceState"](window.history.state, "", url);
      window.dispatchEvent(new Event(changed));
    },
    [defaults],
  );
  return [values, set] as const;
}

const selectionDefaults = { selected: "" };

// useUrlSelection keeps a list's selected row in the address, for ListDetail. Choosing a row adds a history entry, so
// Back undoes it; moving through rows with the arrow keys rewrites the current one, so holding a key does not fill the
// history.
export function useUrlSelection() {
  const [values, set] = useUrlParams(selectionDefaults);
  const onSelect = useCallback(
    (key: string | null, by: "pointer" | "key" | "close") =>
      set({ selected: key ?? "" }, by === "key" ? "replace" : "push"),
    [set],
  );
  return { selected: values.selected || null, onSelect };
}
