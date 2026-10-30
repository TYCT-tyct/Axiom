// readForm returns what a form's named controls hold, as typed. A password is never trimmed; a caller trims the rest.
export function readForm(form: HTMLFormElement) {
  return Object.fromEntries(new FormData(form)) as Record<string, string>;
}

// A time in a datetime-local field is the viewer's own clock; the API takes an instant.
export function toLocalInput(instant: string | undefined) {
  const date = new Date(instant ?? Number.NaN);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

export function toInstant(local: string | undefined) {
  const date = new Date(local ?? Number.NaN);
  return local && !Number.isNaN(date.getTime()) ? date.toISOString() : undefined;
}
