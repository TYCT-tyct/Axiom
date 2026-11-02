import { useTranslation } from "react-i18next";

// seconds as the largest units that hold it: 172800 as "2 days".
export function useDuration() {
  const { i18n } = useTranslation();
  return (seconds: number) => {
    const units = [
      ["day", 86_400],
      ["hour", 3_600],
      ["minute", 60],
      ["second", 1],
    ] as const;
    let rest = seconds;
    const parts: string[] = [];
    for (const [unit, size] of units) {
      const count = Math.floor(rest / size);
      rest -= count * size;
      if (count > 0) {
        parts.push(new Intl.NumberFormat(i18n.language, { style: "unit", unit, unitDisplay: "long" }).format(count));
      }
    }
    return parts.join(" ");
  };
}
