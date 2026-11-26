import { ApiError, adminApi, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { queryOptions, useQuery } from "@tanstack/react-query";

// The model library's operations (the catalog's family and the import from a price list) answer 501 until their
// backend unit lands (#948 B4), and a write cannot be tried to find out. The unit's read, a supplier's piles of
// names, is asked for with no supplier at all: before the unit it answers 501, after it 404 for the supplier that is
// not there. Anything but that 404 (or an answer) says the library is not here to use, and the pages that depend on it
// leave it out without saying so.
const nobody = "00000000-0000-0000-0000-000000000000";

const modelLibraryQuery = queryOptions({
  queryKey: [accountKey, "model-library"],
  queryFn: async ({ signal }) => {
    try {
      await unwrap(
        adminApi.GET("/api/admin/v1/suppliers/{supplier_id}/models", {
          params: { path: { supplier_id: nobody }, query: { limit: 1 } },
          signal,
        }),
      );
      return true;
    } catch (error) {
      return error instanceof ApiError && error.status === 404;
    }
  },
  retry: false,
  staleTime: 5 * 60_000,
});

// useModelLibrary tells whether the library's backend is there. While it is asked, it is not.
export function useModelLibrary() {
  return useQuery(modelLibraryQuery).data === true;
}
