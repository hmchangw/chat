export function parseList(input) {
  return input
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}
