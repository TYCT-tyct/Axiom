// Who is in the sea's water (clawd-cast.js, "one director"): the whale and Clawd never share it. Clawd waits while the
// whale is out (or is waiting to come), and the whale waits while Clawd is busy. Each module writes its own entry while
// it is running and clears it when it ends; the other only reads it.
export const water: {
  whale: { readonly wh: { active(): boolean }; active(): boolean } | null;
  clawd: { busy(): boolean } | null;
} = { whale: null, clawd: null };
