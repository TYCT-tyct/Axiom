import type { ReactNode } from "react";
import PageHeaderRegion from "./AxDS/regions/PageHeader.tsx";

// The design system's parts (manifest/ds.ts) for pages that have no board of their own, typed by what the page gives
// them. Every word they show is the page's: the board's sample words they replace are never asked for. They are drawn
// for the shell's board root (.ax), so they render inside a board shell.
const t = String;

export function PageHeader({ title, description }: { title: ReactNode; description?: ReactNode }) {
  return <PageHeaderRegion t={t} title={title} description={description} />;
}

export { ActionButton, CopyButton } from "./ds-admin-actions.tsx";
export { CheckboxField, FileField, SelectField, SuggestionField } from "./ds-admin-fields.tsx";
export { FilterForm } from "./ds-admin-filter.tsx";
export { CodeBlock, TestProgress } from "./ds-admin-output.tsx";
export { Dialog, openedModal, TextAreaField, TextField } from "./ds-dialog.tsx";
export { FilterBar, LoadMore, SortableHeader } from "./ds-list.tsx";
export { Empty, ErrorState, Loading, NoPermission } from "./ds-states.tsx";
