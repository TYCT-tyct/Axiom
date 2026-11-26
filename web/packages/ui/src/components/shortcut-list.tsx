import s from "./shortcut-list.module.css";

export type Shortcut = { keys: string; label: string };

// ShortcutList is the keyboard help the help menu opens (adA_keys in admin_core.js): one row per shortcut, its keys in
// a key cap and what they do beside them.
export function ShortcutList({ items }: { items: readonly Shortcut[] }) {
  return (
    <dl className={s.root}>
      {items.map((item) => (
        <div key={item.keys} className={s.row}>
          <dt>
            <kbd className={s.keys}>{item.keys}</kbd>
          </dt>
          <dd>{item.label}</dd>
        </div>
      ))}
    </dl>
  );
}
