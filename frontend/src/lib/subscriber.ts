// serialTail shortens a serial to its last n characters, the digits a
// subscriber reads off the label on the box.
export function serialTail(ouiSerial: string, n = 4) {
  return ouiSerial.length > n ? `…${ouiSerial.slice(-n)}` : ouiSerial;
}
