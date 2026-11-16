// classes joins a component's own CSS Module classes, skipping the ones a variant leaves out. It is not cn: it merges
// no Tailwind utilities, since a new component's classes all come from its module (docs/frontend.md「前端工程」).
export const classes = (...names: (string | false | undefined)[]) => names.filter(Boolean).join(" ");
