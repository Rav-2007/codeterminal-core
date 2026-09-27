// formatLine joins fields into one line of CSV, quoting a field only when it
// has to: when it contains a comma, a double quote or a line break.
export function formatLine(fields) {
  return fields
    .map((field) => {
      if (/[",\r\n]/.test(field)) {
        return '"' + field.replaceAll('"', '""') + '"';
      }
      return field;
    })
    .join(',');
}
