// readForm returns what a form's named controls hold, as typed. A password is never trimmed; a caller trims the rest.
export function readForm(form: HTMLFormElement) {
  return Object.fromEntries(new FormData(form)) as Record<string, string>;
}
