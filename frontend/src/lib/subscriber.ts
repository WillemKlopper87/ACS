// serialTail shortens a serial to its last n characters, the digits a
// subscriber reads off the label on the box.
export function serialTail(ouiSerial: string, n = 4) {
  return ouiSerial.length > n ? `…${ouiSerial.slice(-n)}` : ouiSerial;
}

// maskName keeps each word's first letter: "Thandi Nkosi" → "T*** N***".
// Enough to tell namesakes apart on screen without showing the name.
export function maskName(name: string) {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .map((w) => `${w[0]}***`)
    .join(" ");
}
