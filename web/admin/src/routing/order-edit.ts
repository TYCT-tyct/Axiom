import type { AdminSchemas } from "@axiom/api";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import type { Lines } from "../lines";
import { checkRules } from "../preset-dialogs";
import type { PresetLayer } from "../queries";
import { orderOf, same, withOrder } from "./line-order";

type Preset = AdminSchemas["RoutingPreset"];

// What a check of the lines found: the codenames of the order whose line cannot take a request now, and how many of
// the preset's rules keep no endpoint. It is of the order as it was checked, and says so no more once the order moves.
export type Found = { order: readonly string[]; down: readonly string[]; unmatched: number };

// useOrderEdit is a preset's line order as it is being edited, and what a check of it found. The order being edited
// belongs to one revision of one preset: stepping to another preset, or a save, starts over from what is stored.
export function useOrderEdit(preset: Preset, layer: PresetLayer, currency: string | undefined, lines: Lines) {
  const saved = orderOf(preset.rules);
  const stamp = `${preset.id}:${preset.revision}`;
  const [edit, setEdit] = useState<{ stamp: string; order: readonly string[] }>();
  const order = edit?.stamp === stamp ? edit.order : saved;
  const [found, setFound] = useState<Found>();
  const check = useMutation({
    mutationFn: () => checkRules(layer, withOrder(preset.rules, order), preset.currency ?? currency ?? ""),
    onSuccess: (answer) =>
      setFound({
        order,
        down: order.filter((code) => !lines.byCodename.get(code)?.usable),
        unmatched: answer.unmatched.length,
      }),
  });
  return {
    saved,
    order,
    dirty: !same(order, saved),
    set: (next: readonly string[]) => setEdit({ stamp, order: next }),
    check,
    found: found && same(found.order, order) ? found : undefined,
    free: lines.all.filter((line) => line.codename && !order.includes(line.codename)),
    names: (codes: readonly string[]) => codes.map((code) => lines.byCodename.get(code)?.name ?? code),
  };
}
