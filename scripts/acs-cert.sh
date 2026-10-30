#!/bin/bash
# Creates the CWMP listener's TLS certificate for an ACS reached by IP
# address (no DNS name yet): a private CA plus a server certificate whose
# subjectAltName carries the IP(s). Files go to ~/.acs-tls/ (outside the
# repository, so the keys can never be committed):
#   ca.crt / ca.key    private CA (keep ca.key offline once done)
#   acs.crt            server certificate followed by the CA (full chain:
#                      old CPEs do not fetch intermediates)
#   acs.key            server key
#
# CPEs cannot be given this CA over TR-069 (the FastMile has no CA
# parameter), so a CPE accepts it only if it does not verify the ACS
# certificate; testing exactly that is the point (docs/HANDOFF-PLAN-2026-10-30.md
# §3a.4). Once the ACS has a DNS name, replace acs.crt/acs.key with a
# public-CA certificate for that name (e.g. Let's Encrypt fullchain.pem):
# only these paths and the CPE's ACS URL change, no code.
#
# Usage: scripts/acs-cert.sh <public-IP-or-host> [more ...]
#   Existing files are left alone; delete ~/.acs-tls to regenerate.
set -euo pipefail
trap 'echo "acs-cert.sh: failed at line $LINENO (openssl output is hidden; rerun with bash -x)" >&2' ERR
# Git Bash on Windows would rewrite "-subj /CN=..." into a file path.
export MSYS_NO_PATHCONV=1

CERT_DIR="${ACS_TLS_DIR:-$HOME/.acs-tls}"
if [ "$#" -eq 0 ]; then
  echo "usage: $0 <public-IP-or-host> [more ...]" >&2
  exit 2
fi

if [ -s "$CERT_DIR/acs.crt" ] && [ -s "$CERT_DIR/acs.key" ]; then
  echo "CWMP TLS certificate already present in $CERT_DIR (delete the directory to regenerate)"
else
  SAN=""
  CN=""
  for name in "$@"; do
    [ -z "$name" ] && continue
    [ -z "$CN" ] && CN="$name"
    if [[ "$name" =~ ^[0-9.]+$ || "$name" == *:* ]]; then
      SAN="${SAN:+$SAN,}IP:$name"
    else
      SAN="${SAN:+$SAN,}DNS:$name"
    fi
  done

  mkdir -p "$CERT_DIR"
  chmod 700 "$CERT_DIR"
  # RSA 2048 rather than ECDSA: embedded CPE TLS stacks support it universally.
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -sha256 \
    -subj "/CN=ACS lab CWMP CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -keyout "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -sha256 -subj "/CN=$CN" \
    -keyout "$CERT_DIR/acs.key" -out "$CERT_DIR/acs.csr" >/dev/null 2>&1
  printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n' "$SAN" > "$CERT_DIR/acs.ext"
  # 825 days: the longest validity many TLS clients still accept.
  openssl x509 -req -in "$CERT_DIR/acs.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" \
    -CAcreateserial -days 825 -sha256 -extfile "$CERT_DIR/acs.ext" -out "$CERT_DIR/acs.leaf.crt" >/dev/null 2>&1
  cat "$CERT_DIR/acs.leaf.crt" "$CERT_DIR/ca.crt" > "$CERT_DIR/acs.crt"
  rm -f "$CERT_DIR/acs.csr" "$CERT_DIR/acs.ext" "$CERT_DIR/acs.leaf.crt" "$CERT_DIR/ca.srl"
  chmod 600 "$CERT_DIR/ca.key" "$CERT_DIR/acs.key"
  chmod 644 "$CERT_DIR/ca.crt" "$CERT_DIR/acs.crt"
  echo "Created CWMP TLS certificate for $SAN in $CERT_DIR"
fi

cat <<EOF

Add to ~/.acs-secrets.env, then rerun scripts/start.sh:
  export ACS_TLS_CERT="$CERT_DIR/acs.crt"
  export ACS_TLS_KEY="$CERT_DIR/acs.key"
  export ACS_TLS_MIN_VERSION="1.2"
  export ACS_TLS_ADDR=":7548"   # TLS here; plain HTTP stays on :7547 for recovery

Open 7548/tcp to the same CPE source ranges as 7547. Move one CPE at a time
(FastMile first, from its web UI) to https://<this IP>:7548/cwmp. Once every
CPE works over TLS, switch to the production profile with TLS on ACS_ADDR
(e.g. ACS_ADDR=<private IP>:7548, no ACS_TLS_ADDR) and close 7547.
EOF
