import { consoleApi, type Schemas, unwrap } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { balanceQuery } from "./queries";

// useOrderActions changes one order and settles every copy of it the page may hold: the order itself, the list it came
// from, and the balance a paid or cancelled order changes. A failed call is the caller's to show.
export function useOrderActions(id: string) {
  const client = useQueryClient();
  const settle = (order: Schemas["Order"]) => {
    // A verified or cancelled order answers without its refunds. Keeping the ones the page already holds avoids turning a
    // list that was read into "none": these calls do not move refunds, so what was read earlier is still true.
    client.setQueryData<Schemas["Order"]>([accountKey, "order", id], (held) =>
      order.refunds === undefined && held?.refunds !== undefined ? { ...order, refunds: held.refunds } : order,
    );
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
