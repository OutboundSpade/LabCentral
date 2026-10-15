// Keep stored slugs and pinned IDs intact; format names only at display sites.
export function templateName(value: string) {
  const [name, ...context] = value.split(" · ");
  const words = name.replace(/[-_]+/g, " ");
  return [words.charAt(0).toUpperCase() + words.slice(1), ...context].join(" · ");
}
