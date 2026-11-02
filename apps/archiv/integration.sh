#!/usr/bin/env bash
# The closest proof before the box: the built image on a GitHub runner —
# real gocryptfs init/mount/unmount, real signalbackup-tools and msgvault,
# and the supervisor end to end (anlegen, öffnen, the key reaching msgvault,
# an additive import, a Signal backup that fails, a real Signal backup —
# made here by signalbackup-tools, two messages added — decrypted into the
# vault and read by msgvault's import-signal, abschließen, a passphrase
# left in the old outside store, a wrong password, öffnen again (the
# passphrase moved into the vault), packets of SMS-XML messages and of mail going
# into msgvault at the opening).
#
# The IMAP fetch itself is proven by `go test` against go-imap's server;
# a real provider (Gmail) only on the box with the person's app password.
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

gruppe "WhatsApp: a crypt15 round trip with the 64-digit key as the archive calls wadecrypt"
docker run --rm --entrypoint sh "$IMG" -c '
set -e
cd /tmp
python3 -c "import sqlite3;c=sqlite3.connect(\"in.db\");c.execute(\"create table t(x)\");c.execute(\"insert into t values(1)\");c.commit()"
K=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
/opt/wa-crypt-tools/bin/wacreatekey --hex $K -y >/dev/null 2>&1
/opt/wa-crypt-tools/bin/waencrypt encrypted_backup.key in.db msgstore.db.crypt15 >/dev/null 2>&1
wadecrypt $K msgstore.db.crypt15 out.db >/dev/null 2>&1
cmp in.db out.db
' || fail "wadecrypt with the hex key did not give the database back"
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
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passphrase=$PASSPHRASE" --data-urlencode "telefon=+49 1700 0000000" "$U/tresor/signal") # the owner number the direct imports above used
[ "$code" = 303 ] || fail "keys: $code"
warte 120 '.letzter_fehler.quelle == "signal"'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq '.letzter_fehler, .findings'
test -f "$D/eingang/signal/signal-2026-10-01-03-00-00.backup" || fail "the failed backup left the inbox"
test -f "$D/eingang/.teil/abc" || fail "a dot-entry was touched"
arbeit_leer() { # the folder is removed right after the error is recorded
  for _ in $(seq 10); do [ -z "$(docker exec archiv ls -A /run/tresor/arbeit)" ] && return 0; sleep 1; done
  docker exec archiv ls -la /run/tresor/arbeit >&2
  fail "plaintext left in the vault's work folder"
}
arbeit_leer
[ -z "$(sudo find "$D/data/briefkasten" -name '.*' 2>/dev/null)" ] || fail "an unfinished packet stayed"
docker exec archiv test -s /run/tresor/signal-passphrase || fail "the passphrase is not inside the vault"
[ "$(docker exec archiv stat -c %a /run/tresor/signal-passphrase)" = 600 ] || fail "the passphrase file is not 0600"
if sudo test -e "$D/data/schluessel/signal"; then fail "the passphrase is outside the vault"; fi
curl -fsS "${H[@]}" "$U/tresor" | grep -q "hinterlegt am" || fail "the keys are not shown as stored"
if curl -fsS "${H[@]}" "$U/tresor" | grep -q "$PASSPHRASE"; then fail "the passphrase is shown"; fi

echo "Signal: a real backup, decrypted inside the vault and read by msgvault's import-signal"
# The backup is made here with the image's own signalbackup-tools: a dummy
# (its full database schema, version 223, no messages), decrypted, two
# messages added with python's sqlite3, encrypted again with the passphrase.
# Setup failures say "setup:"; everything after that is the archive's.
docker exec -e PP="$PASSPHRASE" archiv sh -c '
set -e
rm -rf /tmp/sig && mkdir -p /tmp/sig/leer /tmp/sig/neu
signalbackup-tools --generatedummy /tmp/sig/dummy.backup --opassphrase "$PP" --no-showprogress >/tmp/sig/log 2>&1 || true
test -s /tmp/sig/dummy.backup || { cat /tmp/sig/log; echo "setup: no dummy backup"; exit 1; }
signalbackup-tools /tmp/sig/dummy.backup "$PP" --output /tmp/sig/leer/ --no-showprogress >>/tmp/sig/log 2>&1 || { cat /tmp/sig/log; echo "setup: dummy does not decrypt"; exit 1; }
python3 - /tmp/sig/leer/database.sqlite <<PY
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("INSERT INTO recipient (_id, e164, aci, profile_joined_name) VALUES (1, ?, ?, ?)", ("+4917000000000", "0f0f0f0f-0000-4000-8000-000000000001", "Anna"))
c.execute("INSERT INTO recipient (_id, e164, aci, system_joined_name) VALUES (2, ?, ?, ?)", ("+4915177777777", "0f0f0f0f-0000-4000-8000-000000000002", "Carla"))
c.execute("INSERT INTO thread (_id, recipient_id) VALUES (1, 2)")
ein, aus = 20 | 0x200000 | 0x800000, 23 | 0x200000 | 0x800000
c.execute("INSERT INTO message (date_sent, date_received, thread_id, from_recipient_id, to_recipient_id, type, body) VALUES (1700000500000, 1700000500001, 1, 2, 1, ?, ?)", (ein, "SIGNALTEXT eins"))
c.execute("INSERT INTO message (date_sent, date_received, thread_id, from_recipient_id, to_recipient_id, type, body) VALUES (1700000600000, 1700000600001, 1, 1, 2, ?, ?)", (aus, "SIGNALTEXT zwei"))
c.commit()
PY
signalbackup-tools /tmp/sig/leer/ --output /tmp/sig/neu/signal.backup --opassphrase "$PP" --no-showprogress >>/tmp/sig/log 2>&1 || { cat /tmp/sig/log; echo "setup: cannot encrypt the backup again"; exit 1; }
test -s /tmp/sig/neu/signal.backup || { echo "setup: no backup written"; exit 1; }
' || fail "setup: making a Signal backup"
sudo rm -f "$D/eingang/signal/signal-2026-10-01-03-00-00.backup" # the broken one: no longer in the way
docker exec archiv sh -c 'cp /tmp/sig/neu/signal.backup /eingang/signal/.neu && mv /eingang/signal/.neu /eingang/signal/signal-2026-10-02-03-00-00.backup && rm -rf /tmp/sig'
if sudo grep -rq SIGNALTEXT "$D/eingang"; then fail "setup: the backup is not encrypted"; fi
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passphrase=$PASSPHRASE" "$U/tresor/signal") # a key entered again: what failed is tried at once
[ "$code" = 303 ] || fail "keys again: $code"
warte 180 '.letzter_import.signal.datei == "signal-2026-10-02-03-00-00.backup"'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq '.letzter_import.signal, .letzter_fehler'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq -e '.letzter_import.signal.zahlen.signal == 2' || fail "import-signal did not report 2 new messages"
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 4' || { curl -fsS "${H[@]}" "$U/api/v1/stats"; fail "the two Signal messages are not in msgvault"; }
arbeit_leer
sudo test -f "$D/eingang/letzte/signal/signal-2026-10-02-03-00-00.backup" || fail "the imported backup is not kept in letzte/ for a new phone"
[ -z "$(sudo find "$D/eingang/signal" -name 'signal-*.backup')" ] || fail "the imported backup stayed in the inbox"
if sudo grep -rq SIGNALTEXT "$D/data"; then fail "Signal plaintext outside the vault"; fi

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
if sudo grep -rqF "$PASSPHRASE" "$D/data"; then fail "the Signal passphrase readable outside the closed vault"; fi

echo "a passphrase left in the old outside store (before 2026-11-02) moves into the vault on the next opening"
printf '%s\n' "$PASSPHRASE" | sudo tee "$D/data/schluessel/signal" >/dev/null
sudo chmod 600 "$D/data/schluessel/signal"

echo "the Briefkasten while closed: packets the vault cannot see (an SMS XML, as Signal packets were made before import-signal), the real msgvault takes them on opening"
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

echo "mail: a packet of two mails (what the hourly IMAP round writes), again nothing new"
mkdir -p "$D/mails"
printf 'From: Ben <ben@example.org>\r\nTo: anna@example.org\r\nSubject: Briefkasten-Mail eins\r\nDate: Mon, 02 Nov 2026 08:00:00 +0100\r\nMessage-ID: <eins@example.org>\r\n\r\nMAILTEXT-EINS\r\n' >"$D/mails/1.eml"
printf 'From: Ben <ben@example.org>\r\nTo: anna@example.org\r\nSubject: Briefkasten-Mail zwei\r\nDate: Mon, 02 Nov 2026 09:00:00 +0100\r\nMessage-ID: <zwei@example.org>\r\n\r\nMAILTEXT-ZWEI\r\n' >"$D/mails/2.eml"
docker cp "$D/mails" archiv:/tmp/mails
sleep 1
docker exec archiv archiv paket-aus-mails -konto anna@example.org /tmp/mails || fail "paket-aus-mails"
out=$(docker exec archiv archiv paket-aus-mails -konto anna@example.org /tmp/mails)
grep -q "nichts Neues" <<<"$out" || fail "the same mails packed twice: $out"
[ "$(sudo find "$D/data/briefkasten" -name '*-mail.age' | wc -l)" = 1 ] || fail "not one mail packet"
if sudo grep -rq "MAILTEXT" "$D/data/briefkasten"; then fail "plaintext mail in a packet"; fi
if sudo grep -rq "Briefkasten" "$D/data/briefkasten"; then fail "plaintext in a packet"; fi
curl -fsS "${H[@]}" "$U/tresor/stand" | jq -e '.briefkasten.wartend == 3' || fail "status does not count the packets"

code=$(curl -s -o "$D/falsch.html" -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=falsch-falsch" "$U/tresor/oeffnen")
[ "$code" = 403 ] && grep -q "stimmt nicht" "$D/falsch.html" || fail "wrong password: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=$PW" "$U/tresor/oeffnen")
[ "$code" = 303 ] || fail "öffnen: $code"
warte 180 '.zustand == "offen" and .bereit == true'
if sudo test -e "$D/data/schluessel/signal"; then fail "the old outside passphrase stayed after opening"; fi
docker exec archiv test -s /run/tresor/signal-passphrase || fail "no passphrase inside after the move"
# the whole log first: grep -q ends early, and with pipefail docker's SIGPIPE fails the pipe
log=$(docker logs archiv 2>&1)
grep -q "alte Signal-Passphrase außerhalb des Tresors gelöscht" <<<"$log" || fail "the move was not logged"
warte 120 '.briefkasten.wartend == 0 and .letzter_import.signal.anzahl == 3 and .letzter_import.mail.zahlen.mails == 2'
curl -fsS "${H[@]}" "$U/tresor/stand" | jq '.letzter_import, .letztes_paket'
[ -z "$(docker exec archiv ls -A /run/tresor/arbeit)" ] || fail "plaintext left in the vault's work folder after delivery"
# 2 from before, 2 from Signal, 2 new from the first packet; the packet of a.xml brings nothing twice; 2 mails under their own account
curl -fsS "${H[@]}" "$U/api/v1/stats" | jq -e '.total_messages == 8 and .active_messages == 8 and .total_accounts == 3 and .total_attachments >= 1' || { curl -fsS "${H[@]}" "$U/api/v1/stats"; fail "the packets did not arrive as they should (or duplicated)"; }

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
gruppe "contacts and calendars: a real Radicale with laib's rights for archiv-<person>, the relay, the real msgvault"
# Radicale from PyPI (laib runs docker-radicale:latest, the same Radicale 3),
# htpasswd instead of LLDAP, and the rights of laib templates/radicale/pre-start
# with the archiv sections the template change adds (README, "What the
# template must give"): CI proves those rights give archiv-anna exactly
# anna's handy-kontakte, google-* and privat-*, to read only.
fail2() { echo "FAIL: $*" >&2; docker logs archiv2 2>&1 | tail -80 >&2 || true; docker logs radicale 2>&1 | tail -30 >&2 || true; exit 1; }
R=$(mktemp -d)
mkdir -p "$R/etc" "$R/data2/data" "$R/eingang"
cat >"$R/etc/config" <<'CFG'
[server]
hosts = 0.0.0.0:5232
[auth]
type = htpasswd
htpasswd_filename = /etc/radicale/users
htpasswd_encryption = plain
[rights]
type = from_file
file = /etc/radicale/rights
[storage]
filesystem_folder = /var/lib/radicale/collections
CFG
RPW='Radicale-Lesepasswort-42'
printf 'anna:anna-pw\nkalender:kalender-pw\narchiv-anna:%s\nbob:bob-pw\n' "$RPW" >"$R/etc/users"
cat >"$R/etc/rights" <<'RIGHTS'
[root]
user: .+
collection:
permissions: R

[archiv-home]
user: archiv-(.+)
collection: {0}
permissions: R

[archiv-lesen]
user: archiv-(.+)
collection: {0}/(handy-kontakte|google-(?!kontakte(/|$))[^/]+|privat-[^/]+)(/.*)?
permissions: r

[archiv-sonst-nichts]
user: archiv-.+
collection: .*
permissions:

[kalender-mirrors]
user: kalender
collection: [^/]+/(google-(?!kontakte(/|$))|abo-)[^/]+(/.*)?
permissions: rw

[mirrors-own]
user: (.+)
collection: {0}/(google-(?!kontakte(/|$))|abo-)[^/]+(/.*)?
permissions: r

[owner]
user: (.+)
collection: {0}(/.*)?
permissions: RrWw

[haushalt]
user: .+
collection: haus/(haushalt(-kontakte)?|familie-[^/]+)(/.*)?
permissions: rw
RIGHTS
docker network create archivnet >/dev/null
docker run -d --name radicale --network archivnet -v "$R/etc:/etc/radicale:ro" docker.io/library/python:3.13-alpine \
  sh -c 'pip install --quiet --no-cache-dir "radicale>=3.4,<4" && exec python3 -m radicale --config /etc/radicale/config' >/dev/null
DAV=(docker run --rm --network archivnet docker.io/curlimages/curl:8.11.1 -s)
for _ in $(seq 90); do [ "$("${DAV[@]}" -o /dev/null -w '%{http_code}' -u anna:anna-pw http://radicale:5232/anna/ -X PROPFIND)" = 207 ] && break; sleep 2; done
code() { "${DAV[@]}" -o /dev/null -w '%{http_code}' "$@"; }
mkbook='<?xml version="1.0"?><D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:set><D:prop><D:resourcetype><D:collection/><C:addressbook/></D:resourcetype><D:displayname>Handy-Kontakte</D:displayname></D:prop></D:set></D:mkcol>'
mkcal() { echo '<?xml version="1.0"?><C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:displayname>'"$1"'</D:displayname></D:prop></D:set></C:mkcalendar>'; }
vcard() { printf 'BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nN:%s;;;;\r\nTEL;TYPE=CELL:%s\r\nEND:VCARD\r\n' "$1" "$2" "$2" "$3"; }
event() { printf 'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//ci//\r\nBEGIN:VEVENT\r\nUID:%s\r\nDTSTAMP:20261101T000000Z\r\nDTSTART:20261103T090000Z\r\nSUMMARY:%s\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n' "$1" "$2"; }
[ "$(code -u anna:anna-pw -X MKCOL -H 'Content-Type: application/xml' --data "$mkbook" http://radicale:5232/anna/handy-kontakte/)" = 201 ] || fail2 "setup: address book"
[ "$(code -u anna:anna-pw -X PUT -H 'Content-Type: text/vcard' --data-binary "$(vcard 0b0b0b0b-0000-4000-8000-000000000001 Ben +4915199999999)" http://radicale:5232/anna/handy-kontakte/ben.vcf)" = 201 ] || fail2 "setup: card"
[ "$(code -u anna:anna-pw -X PUT -H 'Content-Type: text/vcard' --data-binary "$(vcard 0c0c0c0c-0000-4000-8000-000000000002 'Ben Mobil' +4915188888888)" http://radicale:5232/anna/handy-kontakte/ben2.vcf)" = 201 ] || fail2 "setup: card 2"
[ "$(code -u kalender:kalender-pw -X MKCALENDAR -H 'Content-Type: application/xml' --data "$(mkcal Arbeit)" http://radicale:5232/anna/google-arbeit/)" = 201 ] || fail2 "setup: google calendar"
for e in eins zwei; do [ "$(code -u kalender:kalender-pw -X PUT -H 'Content-Type: text/calendar' --data-binary "$(event "g-$e" "Termin $e")" "http://radicale:5232/anna/google-arbeit/g-$e.ics")" = 201 ] || fail2 "setup: event $e"; done
[ "$(code -u anna:anna-pw -X MKCALENDAR -H 'Content-Type: application/xml' --data "$(mkcal Sport)" http://radicale:5232/anna/privat-sport/)" = 201 ] || fail2 "setup: own calendar"
[ "$(code -u anna:anna-pw -X PUT -H 'Content-Type: text/calendar' --data-binary "$(event p-1 Training)" http://radicale:5232/anna/privat-sport/p-1.ics)" = 201 ] || fail2 "setup: own event"
[ "$(code -u anna:anna-pw -X MKCALENDAR -H 'Content-Type: application/xml' --data "$(mkcal Geheim)" http://radicale:5232/anna/tagebuch/)" = 201 ] || fail2 "setup: another calendar"
[ "$(code -u bob:bob-pw -X MKCOL -H 'Content-Type: application/xml' --data "$mkbook" http://radicale:5232/bob/handy-kontakte/)" = 201 ] || fail2 "setup: bob's book"

echo "the rights: archiv-anna reads anna's three kinds, nothing else, and writes nothing"
liste=$("${DAV[@]}" -u "archiv-anna:$RPW" -X PROPFIND -H 'Depth: 1' http://radicale:5232/anna/)
for c in handy-kontakte google-arbeit privat-sport; do grep -q "/anna/$c/" <<<"$liste" || fail2 "archiv-anna does not see $c: $liste"; done
if grep -q tagebuch <<<"$liste"; then fail2 "archiv-anna sees a collection that is not its to read"; fi
[ "$(code -u "archiv-anna:$RPW" http://radicale:5232/anna/google-arbeit/)" = 200 ] || fail2 "archiv-anna cannot read the Google calendar"
for c in "-X PUT --data-binary x http://radicale:5232/anna/handy-kontakte/neu.vcf" "-X DELETE http://radicale:5232/anna/handy-kontakte/ben.vcf" \
         "-X DELETE http://radicale:5232/anna/google-arbeit/g-eins.ics" "-X PUT --data-binary x http://radicale:5232/anna/privat-sport/neu.ics" \
         "http://radicale:5232/anna/tagebuch/" "-X PROPFIND http://radicale:5232/bob/handy-kontakte/" "-X MKCOL http://radicale:5232/archiv-anna/x/"; do
  # shellcheck disable=SC2086
  c2=$(code -u "archiv-anna:$RPW" $c)
  [ "$c2" = 403 ] || fail2 "archiv-anna: $c gave $c2, want 403"
done
stand_radicale() { docker exec radicale sh -c 'cd /var/lib/radicale/collections && find . -type f -not -path "*/.Radicale.cache/*" -not -name ".Radicale.lock" -exec sha256sum {} + | sort'; }
vorher=$(stand_radicale)

printf '%s\n' "$RPW" >"$R/radicale-password"
chmod 644 "$R/radicale-password"
docker run -d --name archiv2 --network archivnet "${FUSE[@]}" \
  -e ARCHIV_PERSON=anna -e PROXY_SECRET=s3cret -e ARCHIV_HAUS_SECRET=haus -e LEERLAUF=3600 \
  -e RADICALE_URL=http://radicale:5232/ -e RADICALE_USER=archiv-anna -v "$R/radicale-password:/run/secrets/radicale-password:ro" \
  -v "$R/data2/data:/data" -v "$R/eingang:/eingang" -p 127.0.0.1:18146:8080 "$IMG" >/dev/null
U2=http://127.0.0.1:18146
for _ in $(seq 30); do curl -fsS "$U2/healthz" >/dev/null 2>&1 && break; sleep 1; done
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" --data-urlencode "passwort=$PW" --data-urlencode "wiederholung=$PW" "$U2/tresor/anlegen")
[ "$code" = 303 ] || fail2 "anlegen: $code"
warte2() { # warte2 <seconds> <text on the page>
  for _ in $(seq "$1"); do curl -fsS "${H[@]}" "$U2/tresor" 2>/dev/null | grep -qF "$2" && return 0; sleep 1; done
  curl -sS "${H[@]}" "$U2/tresor" | sed -n '/karte-kontakte/,/<\/section>/p' >&2
  fail2 "waited $1 s for: $2"
}
warte2 240 "Kontakte zuletzt abgeglichen um"
warte2 10 "Google-Kalender „Arbeit“: 2 Termine"
warte2 10 "Eigener Kalender „Sport“: 1 Termin"
seite=$(curl -fsS "${H[@]}" "$U2/tresor")
if grep -q "Geheim" <<<"$seite"; then fail2 "a calendar the archive may not read is on the page"; fi
curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -e '.people | length == 2' || { curl -sS "${H[@]}" "$U2/api/v1/people"; fail2 "the two contacts are not persons in msgvault"; }
curl -fsS "${H[@]}" "$U2/api/v1/carddav/books" | jq -e '.books | length == 1 and .[0].subscribed and .[0].lookup_source and (.[0].write_target | not)' ||
  { curl -sS "${H[@]}" "$U2/api/v1/carddav/books"; fail2 "the book's roles"; }
docker exec archiv2 sh -c 'ls /run/tresor/kalender/' | grep -qx 'google-arbeit.json' || fail2 "no calendar file in the vault"
if sudo grep -rq "Termin eins" "$R/data2/data"; then fail2 "an event in plaintext outside the vault"; fi
grep -q "Ist das dieselbe Person?" <<<"$seite" || fail2 "no suggestions card"
if grep -q "lassen sich gerade nicht abrufen" <<<"$seite"; then fail2 "msgvault's identity-match route did not answer"; fi
[ "$(stand_radicale)" = "$vorher" ] || fail2 "the archive changed something in Radicale"

echo "the relay refuses a write before it reaches Radicale"
docker exec archiv2 python3 - <<'PY' || fail2 "the relay let a write through"
import base64, re, socket, ssl
cfg = open("/run/tresor/msgvault/config.toml").read()
addr = re.search(r'trusted_addresses = \["([^"]+)"\]', cfg).group(1)
pw = open("/run/tresor/kontakte-relais").read().strip()
ctx = ssl.create_default_context(cafile="/run/tresor/msgvault/kontakte-ca.pem")
for method in ("PUT", "DELETE", "PROPPATCH"):
    s = ctx.wrap_socket(socket.create_connection((addr, 18443), timeout=10), server_hostname="kontakte.archiv.invalid")
    auth = base64.b64encode(b"msgvault:" + pw.encode()).decode()
    s.sendall(f"{method} /anna/handy-kontakte/ben.vcf HTTP/1.1\r\nHost: kontakte.archiv.invalid\r\nAuthorization: Basic {auth}\r\nContent-Length: 1\r\nConnection: close\r\n\r\nx".encode())
    status = s.recv(64).split(b" ")[1]
    s.close()
    assert status == b"403", (method, status)
print("relay: writes refused")
PY

echo "an event deleted at the source stays in the archive, marked"
c=$(code -u kalender:kalender-pw -X DELETE http://radicale:5232/anna/google-arbeit/g-zwei.ics)
[ "$c" = 200 ] || [ "$c" = 204 ] || fail2 "setup: delete gave $c"
nachher=$(stand_radicale)
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X POST "$U2/tresor/abgleich")
[ "$code" = 303 ] || fail2 "Jetzt abgleichen: $code"
warte2 60 "Google-Kalender „Arbeit“: 2 Termine, davon 1 an der Quelle gelöscht"
docker exec archiv2 sh -c 'cat /run/tresor/kalender/google-arbeit.json' | jq -e '.termine["g-zwei|"].an_der_quelle_geloescht_am != null and (.termine["g-zwei|"].daten | contains("SUMMARY:Termin zwei"))' ||
  fail2 "the deleted event is not kept"

echo "a contact deleted on the phone stays in msgvault, marked; back on the phone, unmarked"
[ "$(code -u anna:anna-pw -X DELETE http://radicale:5232/anna/handy-kontakte/ben2.vcf)" = 200 ] || fail2 "setup: delete a card"
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X POST "$U2/tresor/abgleich")
[ "$code" = 303 ] || fail2 "Jetzt abgleichen: $code"
warte2 60 "Ben Mobil <span class=k>(auf dem Handy gelöscht am"
sleep 2 # the sync after the store
curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -e '.people | length == 2' || { curl -sS "${H[@]}" "$U2/api/v1/people"; fail2 "the deleted contact's person left msgvault"; }
bm=$(curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -r '.people[] | select(.display_name == "Ben Mobil") | .id')
[ -n "$bm" ] || fail2 "no person Ben Mobil"
# msgvault 0.22.0 projects only FN, EMAIL and TEL of a card into the person
# (store/carddav_resources.go): the served CATEGORIES line stays in the card
# it keeps, but the person's profile does not show it. The mark the person
# sees is the archive's page; here: the person and its numbers stay.
curl -fsS "${H[@]}" "$U2/api/v1/people/$bm/profile" | jq -e '[.contact_points[].original_value] | index("+4915188888888") != null' || fail2 "the deleted contact's person lost its number"
docker exec archiv2 sh -c 'cat /run/tresor/kontakte/handy-kontakte.json' | jq -e '[.karten[] | select(.auf_dem_handy_geloescht_am != null)] | length == 1' || fail2 "the store does not mark the card"
[ "$(code -u anna:anna-pw -X PUT -H 'Content-Type: text/vcard' --data-binary "$(vcard 0c0c0c0c-0000-4000-8000-000000000002 'Ben Mobil' +4915188888888)" http://radicale:5232/anna/handy-kontakte/ben2.vcf)" = 201 ] || fail2 "setup: card back"
nachher=$(stand_radicale)
code=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X POST "$U2/tresor/abgleich")
[ "$code" = 303 ] || fail2 "Jetzt abgleichen: $code"
for _ in $(seq 60); do curl -fsS "${H[@]}" "$U2/tresor" | grep -q "auf dem Handy gelöscht" || break; sleep 1; done
if curl -fsS "${H[@]}" "$U2/tresor" | grep -q "auf dem Handy gelöscht"; then fail2 "a card back on the phone is still marked"; fi
docker exec archiv2 sh -c 'cat /run/tresor/kontakte/handy-kontakte.json' | jq -e '[.karten[] | select(.auf_dem_handy_geloescht_am != null)] | length == 0' || fail2 "the store still marks the card"
curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -e '.people | length == 2' || fail2 "the card back made a second person"

echo "two persons merged by hand: preview, then the merge"
ids=$(curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -r '[.people[].id] | sort | map(tostring) | join(" ")')
read -r A B <<<"$ids"
vs=$(curl -fsS "${H[@]}" "$U2/tresor/personen/vorschau?a=$A&b=$B")
grep -q "Ja, zusammenführen" <<<"$vs" || fail2 "no preview: $vs"
grep -q "+4915188888888" <<<"$vs" || fail2 "the preview lacks a number: $vs"
ar=$(grep -o 'name=ar value="[0-9]*"' <<<"$vs" | grep -o '[0-9]*')
br=$(grep -o 'name=br value="[0-9]*"' <<<"$vs" | grep -o '[0-9]*')
sk=$(grep -o 'name=schluessel value="[0-9a-f]*"' <<<"$vs" | sed 's/.*value="//;s/"//')
curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -e '.people | length == 2' || fail2 "the preview merged"
code=$(curl -s -o "$R/zusammen.html" -w '%{http_code}' "${H[@]}" --data "a=$A&ar=$ar&b=$B&br=$br&schluessel=$sk" "$U2/tresor/personen/zusammen")
[ "$code" = 303 ] || { cat "$R/zusammen.html"; fail2 "merge: $code"; }
curl -fsS "${H[@]}" "$U2/api/v1/people" | jq -e '.people | length == 1' || fail2 "not merged"
[ "$(stand_radicale)" = "$nachher" ] || fail2 "the archive changed something in Radicale"

docker stop -t 90 archiv2 >/dev/null
docker logs archiv2 >"$R/log" 2>&1
if grep -q "$RPW" "$R/log"; then fail2 "the Radicale password in the log"; fi
docker rm archiv2 >/dev/null
docker rm -f radicale >/dev/null
docker network rm archivnet >/dev/null
sudo rm -rf "$R"
ende
echo "integration: ok"
