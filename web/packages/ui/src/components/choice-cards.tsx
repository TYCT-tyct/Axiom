import { classes } from "../classes";
import s from "./choice-cards.module.css";
import { Glyph, type GlyphName } from "./glyph";

type Choice<T extends string> = { value: T; title: string; detail: string; icon: GlyphName };

// ChoiceCards is a choice among a few ways of doing one thing, each a card with an icon, a name and a line on what it
// is for (the admin's .adways at the top of 添加上游账号). The form under it follows the choice. Each card is a
// pressed-or-not button in a named group, as the board draws it; on a phone the cards stack, icon beside the words.
export function ChoiceCards<T extends string>({
  label,
  value,
  choices,
  onChange,
}: {
  label: string;
  value: T;
  choices: readonly Choice<T>[];
  onChange: (value: T) => void;
}) {
  return (
    <fieldset aria-label={label} className={s.root}>
      {choices.map((choice) => (
        <button
          key={choice.value}
          type="button"
          aria-pressed={choice.value === value}
          className={classes(s.card, choice.value === value && s.on)}
          onClick={() => onChange(choice.value)}
        >
          <span className={s.icon}>
            <Glyph name={choice.icon} size={20} />
          </span>
          <b>{choice.title}</b>
          <span className={s.detail}>{choice.detail}</span>
        </button>
      ))}
    </fieldset>
  );
}
