import type { Messages } from "@axiom/ui";
import type zh from "../zh";
import initial from "./initial";
import settings from "./settings";
import suppliers from "./suppliers";

export default { ...initial, settings, suppliers } satisfies Messages<typeof zh>;
