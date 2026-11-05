import { expect, it } from "vitest";
import { readLimits } from "./routing-limits";

const problem = "out of range";

type Typed = Record<string, string>;

// What the dialog sends: only the typed values, nested as the contract has them, the global size from MiB to bytes.
it.each<{ case: string; typed: Typed; sent: object }>([
  { case: "nothing typed", typed: {}, sent: {} },
  {
    case: "every bound",
    typed: {
      "platform/active": "1",
      "account/total": "5000",
      kept_versions: "50",
      "global/presets": "1000000",
      "global/bytes": "2048",
    },
    sent: {
      platform: { active: 1 },
      account: { total: 5000 },
      kept_versions: 50,
      global: { presets: 1_000_000, bytes: 2 * 1024 ** 3 },
    },
  },
  {
    case: "a size that is not whole MiB, as it was saved",
    typed: { "global/bytes": "256.5", "site/active": " 30 ", "site/total": "40" },
    sent: { global: { bytes: 256.5 * 1024 ** 2 }, site: { active: 30, total: 40 } },
  },
])("reads $case", ({ typed, sent }) => {
  expect(readLimits(typed, problem)).toEqual(sent);
});

it.each<Typed>([
  { "workspace/active": "0" },
  { "workspace/active": "501" },
  { "site/total": "5001" },
  { kept_versions: "49" },
  { kept_versions: "12.5" },
  { "global/presets": "0" },
  { "global/bytes": "0.5" },
  { "global/bytes": "2049" },
  { "account/active": "twenty" },
])("refuses %o", (typed) => {
  expect(readLimits(typed, problem)).toEqual({ problem });
});
