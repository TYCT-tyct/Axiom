import en from "./en/suppliers";
import enUpstream from "./en/upstream";
import enChannel from "./en/upstream-channel";
import zh from "./zh/suppliers";
import zhUpstream from "./zh/upstream";
import zhChannel from "./zh/upstream-channel";

// The words of 上游 and a channel's page: the supplier dialogs they keep, and the upstream views.
export default {
  en: { suppliers: en, upstream: { ...enUpstream, ...enChannel } },
  zh: { suppliers: zh, upstream: { ...zhUpstream, ...zhChannel } },
};
