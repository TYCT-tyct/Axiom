import type { Schemas } from "@axiom/api";
import type { KeyFilters } from "./queries";

type KeyListPage = { filters?: Schemas["KeyFilterEcho"] };

const hasFilters = (filters: KeyFilters) => Object.values(filters).some(Boolean);

function sameText(want: string | undefined, got: string | undefined) {
  return (want ?? "") === (got ?? "");
}

// A node formats an instant it echoes; compare the instant, not the text.
function sameInstant(want: string | undefined, got: string | undefined) {
  if (!want || !got) {
    return !want && !got;
  }
  return Date.parse(want) === Date.parse(got);
}

// keyFiltersApplied says whether every page came from a node that applied the filters the page asked for. A node
// before key filters ignores the parameters and sends no echo; its rows must not be shown as filtered results. With no
// filters there is nothing to verify, and an old node's full list is correct.
export function keyFiltersApplied(filters: KeyFilters, pages: KeyListPage[]): boolean {
  if (!hasFilters(filters)) {
    return true;
  }
  if (pages.length === 0) {
    return false;
  }
  return pages.every((page) => {
    const applied = page.filters;
    return (
      applied !== undefined &&
      sameText(filters.name, applied.name) &&
      sameText(filters.prefix, applied.prefix) &&
      sameText(filters.status, applied.status) &&
      sameInstant(filters.created_after, applied.created_after) &&
      sameInstant(filters.created_before, applied.created_before) &&
      sameText(filters.workspace_id, applied.workspace_id)
    );
  });
}
