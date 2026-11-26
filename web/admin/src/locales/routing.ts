import enRouting from "./en/routing-presets";
import enSources from "./en/source-classes";
import settings from "./settings";
import zhRouting from "./zh/routing-presets";
import zhSources from "./zh/source-classes";

// The routing page's own words, with the settings' it shows (the limits).
export default {
  en: { ...settings.en, routingPresets: enRouting, sourceClasses: enSources },
  zh: { ...settings.zh, routingPresets: zhRouting, sourceClasses: zhSources },
};
