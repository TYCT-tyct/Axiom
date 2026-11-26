import enWords from "./en/setting-words";
import en from "./en/settings";
import zhWords from "./zh/setting-words";
import zh from "./zh/settings";

// A settings page carries the words of its rows with its own (the first screen has neither).
export default { en: { settings: en, ...enWords }, zh: { settings: zh, ...zhWords } };
