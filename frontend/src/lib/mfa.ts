// The base32 key in groups of four, the way authenticator apps show it,
// so it can be read across and typed without losing your place.
export function groupSecret(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}
