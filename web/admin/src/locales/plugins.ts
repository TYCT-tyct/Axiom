import enCalls from "./en/calls";
import en from "./en/plugins";
import settings from "./settings";
import zhCalls from "./zh/calls";
import zh from "./zh/plugins";

// The plugins' two settings pages are the search engines and file parser sections of the settings, so their words come
// with this page; so do the words of the calls list it opens a call from.
export default {
  en: { plugins: en, calls: enCalls, ...settings.en },
  zh: { plugins: zh, calls: zhCalls, ...settings.zh },
};
