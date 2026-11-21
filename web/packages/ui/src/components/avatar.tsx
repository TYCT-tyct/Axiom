import { board } from "../board-classes";
import { classes } from "../classes";

// initial is the first character a reader sees in a name, whole: an emoji or a letter with its marks is one character
// of several code points, and slicing a code unit would draw half of it.
const initial = (name: string) =>
  (
    new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(name.trim())[Symbol.iterator]().next().value
      ?.segment ?? ""
  ).toLocaleUpperCase();

// Avatar stands for a person or an account by the first character of its name (.qav sm/lg from AxApp/regions.css). It
// is decoration beside the name, so a screen reader skips it.
export function Avatar({ name, size = "md" }: { name: string; size?: "sm" | "md" | "lg" }) {
  return (
    <span aria-hidden="true" className={classes(board.qav, size !== "md" && board[size])}>
      {initial(name)}
    </span>
  );
}
