// parseLine splits one line of CSV into its fields, following RFC 4180: a
// field wrapped in double quotes may contain commas, and inside it two double
// quotes stand for one. Fields are not trimmed. An unterminated quoted field
// throws.
export function parseLine(line) {
  const fields = [];
  let field = '';
  let i = 0;
  let quoted = false;
  let fieldStart = true;
  while (i < line.length) {
    const ch = line[i];
    if (quoted) {
      if (ch === '"') {
        if (line[i + 1] === '"') {
          field += '"';
          i += 2;
          continue;
        }
        quoted = false;
        i++;
        continue;
      }
      field += ch;
      i++;
      continue;
    }
    if (ch === '"' && fieldStart) {
      quoted = true;
      fieldStart = false;
      i++;
      continue;
    }
    if (ch === ',') {
      fields.push(field);
      field = '';
      fieldStart = true;
      i++;
      continue;
    }
    field += ch;
    fieldStart = false;
    i++;
  }
  if (quoted) {
    throw new Error('unterminated quoted field');
  }
  fields.push(field);
  return fields;
}
