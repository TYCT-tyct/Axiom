import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { balanceQuery } from "./queries";

// useOrderActions changes one order and settles every copy of it the page may hold: the order itself, the list it came
// from, and the balance a paid or cancelled order changes. A failed call is the caller's to show.
export function useOrderActions(id: string) {
  const client = useQueryClient();
  const settle = (order: Schemas["Order"]) => {
    client.setQueryData([accountKey, "order", id], order);
    void client.invalidateQueries({ queryKey: [accountKey, "orders"] });
    void client.invalidateQueries({ queryKey: balanceQuery.queryKey });
  };
  const verify = useMutation({
    mutationFn: () =>
      unwrap(consoleApi.POST("/api/console/v1/orders/{order_id}/verify", { params: { path: { order_id: id } } })),
    onSuccess: settle,
  });
  const cancel = useMutation({
    mutationFn: () =>
      unwrap(consoleApi.POST("/api/console/v1/orders/{order_id}/cancel", { params: { path: { order_id: id } } })),
    onSuccess: settle,
  });
  return { verify, cancel };
}
