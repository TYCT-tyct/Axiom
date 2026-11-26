import { useFormat } from "../format";
import s from "./list-count.module.css";

// ListCount is a list's count at the end of its filter row (the admin's .adcnt): "3 / 10 条" while a filter hides some
// of the rows, and otherwise "10 条", or nothing when the row only speaks up while filtering (whileFiltered).
export function ListCount({
  shown,
  total,
  unit,
  whileFiltered = false,
}: {
  shown: number;
  total: number;
  unit: string;
  whileFiltered?: boolean;
}) {
  const format = useFormat();
  const text =
    shown !== total
      ? `${format.number(shown)} / ${format.number(total)} ${unit}`
      : whileFiltered
        ? ""
        : `${format.number(total)} ${unit}`;
  return (
    <span className={s.root} role="status">
      {text}
    </span>
  );
}
