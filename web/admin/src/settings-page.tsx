import { ApiError } from "@axiom/api";
import { Button, DataState, Icon } from "@axiom/ui";
import type { UseQueryResult } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { Facts } from "./parts";

// SettingsLabels are the words of one settings page: what the resource is called, what a node without it says, and
// what its two buttons do.
type SettingsLabels = {
  title: string;
  unavailableLabel: string;
  unavailable: string;
  edit: string;
  test: string;
};

// SettingsPage is a settings resource: the view the admin API answers with, the card standing in for a node of an
// older release that has no such settings, and the card with the two dialogs its buttons open. A page says how its
// facts read and what its dialogs are; taking callbacks rather than component types lets one card serve two resources
// that differ only in their words and endpoints, while a dialog still returns its own component, so an open dialog
// keeps its state when the view is fetched again. That the buttons open them, that closing unmounts them and how the
// view loads is the same for every settings resource.
export function SettingsPage<T>({
  query,
  labels,
  facts,
  dialogs,
}: {
  query: UseQueryResult<T>;
  labels: SettingsLabels;
  facts: (props: { view: T; action: ReactNode }) => ReactNode;
  dialogs: {
    edit: (props: { view: T; onClose: () => void }) => ReactNode;
    // A test dialog may show what it is testing: the search page names the default engine.
    test: (props: { view: T; onClose: () => void }) => ReactNode;
  };
}) {
  const [editing, setEditing] = useState(false);
  const [testing, setTesting] = useState(false);
  // A node of an older release answers 404 for settings it does not have: the rest of the admin still works, so the
  // card stands in rather than the page's not-found state.
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <Facts title={labels.title} rows={[[labels.unavailableLabel, labels.unavailable]]} />;
  }
  return (
    <DataState query={query}>
      {(view) => (
        <>
          {facts({
            view,
            action: (
              <div className="flex gap-2">
                <Button size="md" onClick={() => setTesting(true)}>
                  <Icon name="check" />
                  {labels.test}
                </Button>
                <Button size="md" onClick={() => setEditing(true)}>
                  <Icon name="pencil" />
                  {labels.edit}
                </Button>
              </div>
            ),
          })}
          {editing ? dialogs.edit({ view, onClose: () => setEditing(false) }) : null}
          {testing ? dialogs.test({ view, onClose: () => setTesting(false) }) : null}
        </>
      )}
    </DataState>
  );
}
