import { adminSuite } from "./admin-scenes.ts";
import { consoleSuite } from "./console-scenes.ts";

// The apps whose screens the acceptance gate walks.
export const suites = [consoleSuite, adminSuite];
