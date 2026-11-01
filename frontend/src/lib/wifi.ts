// Mirrors the server's SET_WIFI validation (cmd/api wifiSettings.validate):
// an SSID of 1-32 bytes and/or an 8-63 character WPA passphrase, so a typo
// is caught before up to 500 jobs fan out.
export function wifiInputValid(ssid: string, password: string): boolean {
  const name = ssid.trim();
  if (!name && !password) return false;
  if (name && new TextEncoder().encode(name).length > 32) return false;
  if (password && (password.length < 8 || password.length > 63)) return false;
  return true;
}
