#!/usr/bin/env bash
# The closest proof before the box: the built image on a GitHub runner —
# real gocryptfs init/mount/unmount, real signalbackup-tools and msgvault,
# and the supervisor end to end (anlegen, öffnen, the key reaching msgvault,
# an additive import, a Signal backup that fails, abschließen, a wrong
# password, öffnen again).
#
# What it is NOT: the runner is rootful Docker on Ubuntu (AppArmor). The
# box is rootless podman on Fedora CoreOS (SELinux). That FUSE mounts there
# with the same flags is still unproven.
#
# usage: integration.sh <image>
set -euo pipefail
IMG=$1
FUSE=(--device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor=unconfined)
PW='Integration-Passwort-42'
PASSPHRASE='123451234512345123451234512345'
fail() { echo "FAIL: $*" >&2; docker logs archiv 2>&1 | tail -80 >&2 || true; exit 1; }
gruppe() { echo "::group::$*"; }
ende() { echo "::endgroup::"; }

gruppe "the tools run"
docker run --rm --entrypoint msgvault "$IMG" version
out=$(docker run --rm --entrypoint signalbackup-tools "$IMG" --help 2>&1 || true)
grep -qi "usage\|signalbackup-tools" <<<"$out" || fail "signalbackup-tools --help: $out"
head -3 <<<"$out"
docker run --rm --entrypoint gocryptfs "$IMG" -version
docker run --rm --entrypoint wadecrypt "$IMG" --help >/dev/null
ende

gruppe "gocryptfs: init, mount, unmount, wrong password (exit 12)"
docker run --rm "${FUSE[@]}" --entrypoint sh "$IMG" -c '
set -e
mkdir -p /tmp/c /tmp/m
printf "%s\n" "geheim-geheim" | gocryptfs -init -q /tmp/c
printf "%s\n" "geheim-geheim" | gocryptfs -q /tmp/c /tmp/m
echo klartext-im-tresor > /tmp/m/datei
grep -q " /tmp/m " /proc/self/mounts
if grep -rq klartext-im-tresor /tmp/c; then echo "plaintext in the cipher dir"; exit 1; fi
fusermount3 -u /tmp/m
test ! -e /tmp/m/datei
set +e
printf "%s\n" "falsch" | gocryptfs -q /tmp/c /tmp/m
rc=$?
set -e
test "$rc" = 12 || { echo "wrong password: exit $rc, want 12"; exit 1; }
echo "gocryptfs ok"
'
ende

gruppe "the supervisor end to end"
D=$(mktemp -d)
mkdir -p "$D/data" "$D/eingang/signal" "$D/eingang/.teil" "$D/eingang/whatsapp/Databases" "$D/eingang/whatsapp/Media/WhatsApp Images"
head -c 8192 /dev/urandom >"$D/eingang/signal/signal-2026-10-01-03-00-00.backup"
echo teil >"$D/eingang/.teil/abc"
head -c 4096 /dev/urandom >"$D/eingang/whatsapp/Databases/msgstore.db.crypt15"
echo "KLARTEXT-BILD-VOM-HANDY" >"$D/eingang/whatsapp/Media/WhatsApp Images/IMG-1.jpg"
docker run -d --name archiv "${FUSE[@]}" \
  -e ARCHIV_PERSON=anna -e PROXY_SECRET=s3cret -e ARCHIV_HAUS_SECRET=haus -e EINGANG_RUHE=2 -e IMPORT_TAKT=2 -e LEERLAUF=3600 \
  -v "$D/data:/data" -v "$D/eingang:/eingang" -p 127.0.0.1:18145:8080 "$IMG" >/dev/null
U=http://127.0.0.1:18145
H=(-H "X-Archiv-Proxy: s3cret" -H "Remote-User: anna")
warte() { # warte <seconds> <jq filter on /tresor/stand>
  for _ in $(seq "$1"); do
    if curl -fsS "${H[@]}" "$U/tresor/stand" 2>/dev/null | jq -e "$2" >/dev/null; then return 0; fi
    sleep 1
  done
  curl -sS "${H[@]}" "$U/tresor/stand" || true
  fail "waited $1 s for $2"
}
for _ in $(seq 30); do curl -fsS "$U/healthz" >/dev/null 2>&1 && break; sleep 1; done
jq -e '.zustand == "uneingerichtet"' "$D/data/laib-status.json" || fail "status before anlegen"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$U/tresor")" = 401 ] || fail "a request without Caddy's secret got in"

code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=$PW" --data-urlencode "wiederholung=$PW" "$U/tresor/anlegen")
[ "$code" = 303 ] || fail "anlegen: $code"
warte 180 '.zustand == "offen" and .bereit == true'
echo "open and msgvault answers"
sudo grep -q '^age1' "$D/data/briefkasten.pub" || fail "no public key outside the vault"

curl -fsS "${H[@]}" "$U/" | grep -qi msgvault || fail "/ while open is not msgvault's UI"
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 0' || fail "the person's request did not carry the key"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer falsch' "$U/api/v1/stats")" = 401 ] || fail "a wrong bearer was not refused by msgvault"
if sudo find "$D/data/tresor" -name 'msgvault*' -o -name 'api-key' -o -name 'config.toml' | grep -q .; then fail "plain names in the cipher dir"; fi

echo "additive import with the real msgvault, in the vault"
cat >"$D/a.xml" <<'EOF'
<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <sms protocol="0" address="+4915112345678" date="1700000000000" type="1" subject="null" body="Hallo eins" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="1700000000000" readable_date="" contact_name="Anna" />
  <sms protocol="0" address="+4915112345678" date="1700000100000" type="2" subject="null" body="Hallo zwei" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="0" readable_date="" contact_name="Anna" />
</smses>
EOF
cat >"$D/b.xml" <<'EOF'
<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="1">
  <sms protocol="0" address="+4915112345678" date="1700000100000" type="2" subject="null" body="Hallo zwei" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="0" readable_date="" contact_name="Anna" />
</smses>
EOF
docker cp "$D/a.xml" archiv:/tmp/a.xml
docker cp "$D/b.xml" archiv:/tmp/b.xml
MV=(docker exec -e MSGVAULT_HOME=/run/tresor/msgvault -e HOME=/run/tresor/msgvault -e MSGVAULT_TELEMETRY_ENABLED=0 archiv msgvault)
"${MV[@]}" import-synctech-sms /tmp/a.xml --owner-phone +4917000000000
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 2' || fail "first import"
"${MV[@]}" import-synctech-sms /tmp/b.xml --owner-phone +4917000000000 # a newer backup that lacks a message
"${MV[@]}" import-synctech-sms /tmp/a.xml --owner-phone +4917000000000 # the same again
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 2 and .active_messages == 2' || fail "the import is not additive or makes duplicates"

echo "a Signal backup that does not decrypt: the sentence, the backup kept, no plaintext anywhere"
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passphrase=$PASSPHRASE" --data-urlencode "telefon=+49 151 2345678" "$U/tresor/signal")
[ "$code" = 303 ] || fail "keys: $code"
warte 120 '.letzter_fehler.quelle == "signal"'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq '.letzter_fehler, .findings'
test -f "$D/eingang/signal/signal-2026-10-01-03-00-00.backup" || fail "the failed backup left the inbox"
test -f "$D/eingang/.teil/abc" || fail "a dot-entry was touched"
[ -z "$(docker exec archiv ls -A /run/tresor/arbeit)" ] || fail "plaintext left in the vault's work folder"
[ -z "$(docker exec archiv ls -A /dev/shm/archiv)" ] || fail "something left in the plaintext pipe folder"
[ -z "$(sudo find "$D/data/briefkasten" -name '.*' 2>/dev/null)" ] || fail "an unfinished packet stayed"
sudo test -f "$D/data/schluessel/signal" || fail "the passphrase is not outside the vault"
[ "$(sudo stat -c %a "$D/data/schluessel/signal")" = 600 ] || fail "the passphrase file is not 0600"
curl -fsS "${H[@]}" "$U/tresor" | grep -q "hinterlegt am" || fail "the keys are not shown as stored"
if curl -fsS "${H[@]}" "$U/tresor" | grep -q "$PASSPHRASE"; then fail "the passphrase is shown"; fi

echo "WhatsApp: the newest database kept, the media only encrypted, Haus gets them back while open"
for _ in $(seq 30); do sudo test -f "$D/eingang/letzte/whatsapp/Media/WhatsApp Images/IMG-1.jpg.age" && break; sleep 1; done
sudo test -f "$D/eingang/letzte/whatsapp/Databases/msgstore.db.crypt15" || fail "the newest WhatsApp database is not kept"
sudo test -f "$D/eingang/letzte/whatsapp/Media/WhatsApp Images/IMG-1.jpg.age" || fail "the media file was not encrypted into letzte/"
[ ! -e "$D/eingang/whatsapp/Media/WhatsApp Images/IMG-1.jpg" ] || fail "the plaintext media stayed in the inbox"
if sudo grep -rq KLARTEXT-BILD "$D/eingang"; then fail "plaintext media somewhere in the inbox"; fi
[ "$(curl -fsS -H 'X-Archiv-Haus: haus' "$U/intern/whatsapp/medien/WhatsApp%20Images/IMG-1.jpg")" = KLARTEXT-BILD-VOM-HANDY ] || fail "Haus does not get the media back"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$U/intern/whatsapp/medien/WhatsApp%20Images/IMG-1.jpg")" = 401 ] || fail "the media without Haus' secret"

code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X POST "$U/tresor/abschliessen")
[ "$code" = 303 ] || fail "abschließen: $code"
warte 5 '.zustand == "verschlossen"'
if docker exec archiv grep -q " /run/tresor " /proc/self/mounts; then fail "still mounted after abschließen"; fi
[ -z "$(docker exec archiv ls -A /run/tresor)" ] || fail "the mount point shows files while closed"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer x' "$U/api/v1/stats")" = 423 ] || fail "a bearer while closed is not 423"

[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Archiv-Haus: haus' "$U/intern/whatsapp/medien/WhatsApp%20Images/IMG-1.jpg")" = 423 ] || fail "media while closed is not 423"

echo "the Briefkasten while closed: packets the vault cannot see, the real msgvault takes them on opening"
PNG=iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==
cat >"$D/c.xml" <<XML
<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <sms protocol="0" address="+4915199999999" date="1700000200000" type="1" subject="null" body="Neu aus dem Briefkasten" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="0" readable_date="" contact_name="Ben" />
  <mms date="1700000300000" msg_box="1" read="1" address="+4915199999999~+4915188888888" contact_name="Familie" m_type="132">
    <parts>
      <part seq="0" ct="text/plain" name="null" chset="106" cl="null" text="Ein Bild" />
      <part seq="1" ct="image/png" name="p.png" chset="null" cl="p.png" text="null" data="$PNG" />
    </parts>
    <addrs>
      <addr address="+4915199999999" type="137" charset="106" />
      <addr address="+4917000000000" type="151" charset="106" />
      <addr address="+4915188888888" type="151" charset="106" />
    </addrs>
  </mms>
</smses>
XML
docker exec -i archiv archiv paket-aus-xml -telefon +4917000000000 <"$D/c.xml" || fail "paket-aus-xml c"
sleep 1 # one packet per second keeps the names in order
docker exec -i archiv archiv paket-aus-xml -telefon +4917000000000 <"$D/a.xml" || fail "paket-aus-xml a" # the two messages msgvault has already
out=$(docker exec -i archiv archiv paket-aus-xml -telefon +4917000000000 <"$D/c.xml")
grep -q "nichts Neues" <<<"$out" || fail "the same messages packed twice: $out"
[ "$(sudo find "$D/data/briefkasten" -name '*-signal.age' | wc -l)" = 2 ] || fail "not two packets"
if sudo grep -rq "Briefkasten" "$D/data/briefkasten"; then fail "plaintext in a packet"; fi
curl -fsS "${H[@]}" "$U/tresor/stand" | jq -e '.briefkasten.wartend == 2' || fail "status does not count the packets"

code=$(curl -s -o "$D/falsch.html" -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=falsch-falsch" "$U/tresor/oeffnen")
[ "$code" = 403 ] && grep -q "stimmt nicht" "$D/falsch.html" || fail "wrong password: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=$PW" "$U/tresor/oeffnen")
[ "$code" = 303 ] || fail "öffnen: $code"
warte 180 '.zustand == "offen" and .bereit == true'
warte 120 '.briefkasten.wartend == 0 and .letzter_import.signal.anzahl == 2'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq '.letzter_import, .letztes_paket'
[ -z "$(docker exec archiv ls -A /run/tresor/arbeit)" ] || fail "plaintext left in the vault's work folder after delivery"
# 2 from before, 2 new from the first packet; the packet of a.xml brings nothing twice
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 4 and .active_messages == 4 and .total_attachments >= 1' || { curl -fsS "${H[@]}" "$U/api/v1/stats"; fail "the packets did not arrive as they should (or duplicated)"; }

docker stop -t 90 archiv >/dev/null
docker logs archiv >"$D/log" 2>&1
grep -q "abgeschlossen (Container hält an)" "$D/log" || fail "stopping the container did not close the vault"
for s in "$PW" "$PASSPHRASE"; do
  if grep -q "$s" "$D/log"; then fail "a secret in the container log"; fi
done
jq . "$D/data/laib-status.json"
jq -e '.zustand == "verschlossen"' "$D/data/laib-status.json" || fail "status after stop"
docker rm archiv >/dev/null
sudo rm -rf "$D"
ende
echo "integration: ok"
