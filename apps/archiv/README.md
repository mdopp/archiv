# archiv

Für die Familie in einem Satz: Dein Nachrichten-Archiv — Signal, SMS,
Anrufe und E-Mail (später WhatsApp und Discord) durchsuchbar auf der Box, in
einem Tresor, den nur dein Archiv-Passwort öffnet.

One container per person ([laib ADR 0029](https://github.com/mdopp/laib/blob/main/docs/adr/0029-one-message-archive-per-person.md),
laib#985, laib#986). Inside: a small Go supervisor (`archiv`, this folder),
[msgvault](https://github.com/kenn-io/msgvault) 0.22.0 with `import-signal`
(our fork's image `ghcr.io/mdopp/msgvault:0.22.0-signal`, pinned by digest,
until upstream PR kenn-io/msgvault#1722 is released), gocryptfs (Debian
package), [signalbackup-tools](https://github.com/bepaald/signalbackup-tools)
20261014 and wa-crypt-tools 0.1.0 (WhatsApp); IMAP through
[go-imap](https://github.com/emersion/go-imap) v1.2.1.

- Code: `*.go`, tests: `go test ./...`; `integration.sh <image>` runs the
  built image with a real FUSE mount (CI does, on every push and PR).
- Image: `ghcr.io/mdopp/archiv:latest` and `:<sha>`, built by CI on main.
- Laib template: `templates/archiv` in the laib repo, once per person
  (laib#986). What it must give is below.

## The vault (Tresor)

The whole archive — msgvault's database, its API key, the Briefkasten's
private key — lives in a gocryptfs cipher folder, `/data/tresor`, and is
mounted at `/run/tresor` only while open.

| state | what is there |
|---|---|
| uneingerichtet | no `/data/tresor/gocryptfs.conf`. The page asks for the Archiv-Passwort twice, says losing it loses the archive (so: Vaultwarden) and that an open archive is readable by the box's admin. `gocryptfs -init`, then open; the first opening makes the age X25519 key pair (private: `/run/tresor/age-identitaet`, public: `/data/briefkasten.pub`). |
| verschlossen | ciphertext only. msgvault is stopped. One password field. |
| offen | mounted; `msgvault serve` runs with `MSGVAULT_HOME=/run/tresor/msgvault`, loopback `127.0.0.1:8090`, its key in `api-key` (generated on the first opening), telemetry off (config and env). The Postbote brings in the Signal backups, WhatsApp, the phone's SMS and the Briefkasten's packets. |

Closing: „Abschließen“, two hours without use (`LEERLAUF`), or the container
stopping (SIGTERM): the Postbote stops and deletes its plaintext, msgvault
stops, `fusermount3 -u`. A packet being delivered holds the idle close off. What
counts as use: every request except health checks and msgvault's own
background polling (`/status`, `/stats`, `/operations`, …), so a tab left
open does not keep the vault open forever.

**Secrets.** The Archiv-Passwort reaches gocryptfs on stdin (gocryptfs reads
it there when stdin is not a terminal), never argv, never an env var, never
a log; the handler wipes its copy. The Signal passphrase reaches
signalbackup-tools by a pseudo-terminal (it reads a passphrase only from a
terminal; on a pipe it fails), typed in once the program has switched echo
off. Every child gets a fresh environment, nothing inherited. A program's
error output is scrubbed of the secret before it is logged. Tests:
`befehl_test.go` (also through real exec and a real pty), `seite_test.go`
(`TestPasswordNeverInTheLog`).

Keys the person enters (Signal passphrase, own number, IMAP host/user/app
password) are owner-only files in `/data/schluessel/`, **outside** the
vault: the Mailholer fetches mail while it is closed. (The Signal
passphrase is used only while the vault is open since 2026-11-02, so it
could move inside, as the WhatsApp key is; not done yet.) They are
accepted only while the vault is open (so only the person sets them) and
never shown again: the page shows „hinterlegt am …“ and says the price in
plain words: whoever has root on the box can decrypt the person's Signal
backups with the passphrase, and read the person's mail with the app
password (which the provider can revoke); what is already in the archive
stays readable only with the Archiv-Passwort.

### Opening with a passkey

A passkey can open the vault instead of the typed password (WebAuthn's PRF
extension; see `passkey.go`). Ticking „Künftig mit Passkey öffnen“ when
opening a closed vault with the password creates a passkey for this site
(user verification required) and asks it for a secret for a random salt;
the box keeps the password encrypted under HKDF(secret) with AES-GCM in
`/data/schluessel/passkeys.json`. „Mit Passkey öffnen“ asks the passkey for
the same secret and the box unwraps the password and opens the vault. The
box does not verify WebAuthn signatures: it grants nothing by itself, only
the secret unwraps, and the browser gives it only to this site after a
fingerprint or PIN. The password stays the way in without the phone; a
passkey can be removed on the open page. Needs a passkey store with PRF
(Google Password Manager and iCloud Keychain have it; others may not — the
page says so).

## Who gets in

- `/healthz` — anyone (laib's health check).
- The person: a request with Caddy's proxy secret (`X-Archiv-Proxy`) **and**
  `Remote-User` = `ARCHIV_PERSON`. Another signed-in person: 403. Neither: 401.
  A form posted from another origin: 403.
- `/tresor…` — the supervisor's pages (`/tresor/stand` is the state as JSON,
  for Haus' control).
- Every other path, while open: msgvault's own web UI and API (it lives at
  the root: absolute asset paths, no frames). The supervisor adds
  `X-API-Key` itself; Caddy's headers and Authelia's cookie stay behind.
  While closed, `/` is the page to open it and other GETs redirect there.
- An agent (`/p/<person>/…`, the prefix stripped by Caddy, past Authelia):
  `Authorization: Bearer <msgvault's key>` goes to msgvault unchanged while
  open; while closed, 423 `{"fehler": "Das Archiv ist abgeschlossen. …"}`.
  A forged `Remote-User` on this way gets no key. How the agent learns the
  key (it is in the vault) is laib#986's question.

## The inbox

Haus puts the backups into `/eingang/<quelle>/…` (haus#207); `.teil/`,
every dot-entry and `letzte/` are never read as inbox. A file counts once
its size and mtime stayed the same for a minute (as this process saw it —
Haus sets the phone's mtime).

### Signal: read while open, by msgvault's import-signal

Since 2026-11-02 (owner's decision; before, signalbackup-tools exported an
SMS Backup & Restore XML into a named pipe while the vault was closed, and
the messages went in as packets under msgvault's SMS account). msgvault's
`import-signal` reads the decrypted backup as a folder — its SQLite
database and the attachment files — and that plaintext may exist only
inside the vault. So Signal backups wait, still encrypted by Signal, in
the inbox until the vault is open. Then the Postbote takes each stable
`signal/**/signal-*.backup`, **oldest first** (by mtime), once the Signal
passphrase and the person's number are stored:

1. A new, empty folder `/run/tresor/arbeit/signal-<rand>/` (inside the
   vault). `signalbackup-tools <backup> --output <folder>/ --no-showprogress`,
   the passphrase typed in by pty, writes `database.sqlite` and one
   `Attachment_*.bin` per attachment there. The trailing slash and the
   empty existing folder matter: given anything else, signalbackup-tools
   writes an encrypted backup file and asks a second time, for that file's
   passphrase (its `main.cc`, tag 20261014).
2. `msgvault import-signal <folder> --phone <number>` (through the running
   daemon), under msgvault's own Signal account. Its `\r` progress line
   (`Processed: N | Added: M`) feeds the page while it runs; its summary
   (`Messages: N processed, M added, …`) gives the number of new messages
   for the page and the status.
3. Attachments over 100 MiB (msgvault's limit for import-signal; it skips
   them) move to `/run/tresor/grosse-anhaenge/signal-Attachment_….bin`,
   the same name for the same attachment in every backup.
4. The folder is removed on every path — success, failure, the vault
   closing in the middle (a crash's leftovers go at the next opening).
5. The backup moves to `/eingang/letzte/signal/` and the older one there
   goes; an older backup that arrives late is deleted after its import.
6. On failure the backup stays in the inbox, the page and the status say
   why, and it is tried again in half an hour and at every opening. A wrong
   passphrase („…IT IS LIKELY AN INCORRECT PASSPHRASE…“) becomes „Die
   Signal-Passphrase passt nicht zu dieser Sicherung. Gib sie neu ein.“,
   and no backup is tried with that key until a new one is entered. A
   backup of another number than the stored one (msgvault refuses it) says
   so, and the person checks the number.

Each backup holds the whole history: msgvault keys a message by its
author's Signal id and its sent time, so the daily backup adds only what
is new and updates edits and reactions. While the vault stays closed, every
night's backup waits in the inbox (each the size of the whole Signal
history); after three days the status says so. Reading one needs its
decrypted size free on `/data` (the work folder is inside the vault) for
a few minutes.

### The Briefkasten: what arrives while the vault is closed

The Briefkasten runs every 20 s whether the vault is open or closed, as
soon as the vault has been made once (it needs the public key). It writes
the public key into the inbox for the Haus app, keeps WhatsApp's newest
database in `letzte/` and encrypts WhatsApp's media there (below). Mail
comes as packets (below). A packet is ONE file
`/data/briefkasten/<UTC time>-<quelle>.age`, age-encrypted to the person's
public key (written under a dot-name, moved into place when whole); the
set of messages already packed is `/data/gesehen/<quelle>`, HMAC-SHA256 of
each message's identity under the key `/data/gesehen/schluessel` — 32
bytes per message, nothing readable. Signal packets made before 2026-11-02
may still wait; they go in as before.

When the vault is open, the **Postbote** takes the packets, oldest first:
decrypts with the private key from the vault, unpacks the attachments into
`/run/tresor/arbeit/`, writes them as an SMS Backup & Restore XML there,
`msgvault import-synctech-sms <xml> --owner-phone <number>` (through the
running daemon), deletes the packet, empties the work folder (always, also
on failure; a crash's leftovers go at the next opening). A packet that
fails stays and is tried again.

**Additive only.** msgvault upserts every message by a stable id; its
`import-synctech-sms` has no flag that deletes or mirrors deletions (only
`--sms/--mms/--calls/--attachments`), `import-signal` has none either
(`--phone`, `--attachments-dir`, `--display-name`, `--limit`,
`--no-default-identity`), and nothing here runs another msgvault command.
A message deleted on the phone is simply not in the next backup; what is
in the archive stays. CI imports two XMLs into the real msgvault (the
second lacking a message, then the first again: still 2, no duplicates),
then a packet of the same messages: still no duplicates.

### letzte/: the newest backup stays for a new phone

| source | in `/eingang/letzte/` |
|---|---|
| Signal | the newest imported `signal-*.backup`, as the phone wrote it (encrypted by Signal) |
| WhatsApp | `whatsapp/Databases/msgstore.db.crypt15`: the newest database, as the phone wrote it (encrypted by WhatsApp; a hard link — the inbox copies wait for the WhatsApp import), and `whatsapp/Media/<path>.age`: **every** media file, age-encrypted to the person's key — on the phone they are plaintext, so the plaintext leaves the inbox as soon as it is encrypted |

Haus serves the database files straight from its own mount of the inbox.
Media need the private key: Haus asks the archive,
`GET /intern/whatsapp/medien/<path>` with `X-Archiv-Haus:
<ARCHIV_HAUS_SECRET>`, answered only while the vault is open (423 while
closed). `GET /intern/empfaenger` gives Haus the public key.

### Mail: fetched every hour, vault closed or open

Owner, 2026-11-02 („A“). Once the IMAP access is stored (host, user, app
password), the Mailholer runs at start, every hour (`MAIL_TAKT`, seconds)
and at once when the access is entered anew:

1. TLS to port 993 (or the port given; 143 only with STARTTLS — a server
   without TLS is refused), LOGIN with the app password.
2. Folders: a server with a `\All` folder (Gmail's „Alle Nachrichten“) is
   read there only, so every message comes once and Spam and Trash stay
   out; Gmail's labels are lost. Elsewhere every selectable folder but
   `\Junk` and `\Trash`.
3. Per folder, read-only (`EXAMINE`): `UID SEARCH` above the last UID
   fetched, then `UID FETCH BODY.PEEK[] FLAGS` in bundles of 100 — nothing
   on the server changes, not even \Seen. A changed UIDVALIDITY starts the
   folder again.
4. A message whose raw bytes were packed before (HMAC-SHA256 of their
   SHA-256 in `/data/gesehen/mail`, as above) is left out — also a
   copy in a second folder.
5. The new messages become ONE packet `<UTC>-mail.age` (another one each
   256 MiB, so a first fetch of a big mailbox arrives in pieces). The
   fetch state `/data/gesehen/mail-stand.json` (per folder, keyed by an
   HMAC of its name: UIDVALIDITY and the last UID) moves on only once the
   messages up to it are in a packet in place; a break fetches them again.
6. A refused login: „Der Mail-Server hat die Anmeldung abgelehnt …“, and no
   further try with that password until a new one is entered. A server out
   of reach or a break: the sentence, and the next hour tries again.

At the opening, the Postbote unpacks the packet's Maildir into the vault's
work folder and runs `msgvault import-maildir <dir> --identifier <konto>`:
folders become labels, a message is keyed by the hash of its raw bytes, a
copy in another folder only gets the label. Additive as above.
`archiv paket-aus-mails -konto <adresse> <ordner>` packs a folder of
`.eml` files the same way (an old export; CI uses it).

**Discord** is not imported: its data package has no importer in msgvault.
`archiv paket-aus-xml -telefon <number> [-quelle sms] < backup.xml` packs
any SMS Backup & Restore XML (an old Android SMS backup too).

### SMS and calls: encrypted on the phone

The Haus app (switch „SMS und Anrufliste übertragen“, ADR 0029 §9) writes
the phone's SMS, MMS and call log as SMS Backup & Restore XML, encrypts
each file with age to the Briefkasten's public key, and uploads it to
`/eingang/sms/<name>.xml.age`. The key reaches the phone this way: the
Briefkasten writes it into the inbox as `.briefkasten.pub` (a dot-file,
never a backup), Haus serves it at `GET /napi/sicherung/schluessel`.

These files need no packing: they are already unreadable on the box. When
the vault opens, the Postbote decrypts each into the vault's work folder
and runs `msgvault import-synctech-sms` with the person's number
(`--owner-phone`; without the number the files wait). msgvault keeps one
of each message and call, so a file sent twice adds nothing. Imported,
the file leaves the inbox; one that cannot be decrypted stays, and the
page says so. Attachments over 25 MiB fail msgvault's SMS import here
(the packet path sets them aside, this one does not yet).

Contacts take another way: Haus mirrors the phone's contacts into the
person's Radicale address book `handy-kontakte`, outside the vault.

### WhatsApp: only while open, the key inside the vault

The person enters the 64-digit key of their WhatsApp backup on the page
while the vault is open; it is stored inside the vault
(`whatsapp-schluessel`), unlike the Signal passphrase. WhatsApp's
`msgstore.db.crypt15` holds the whole history every day and stays in the
inbox anyway (it is also the newest backup for a new phone), so nothing is
lost by waiting for the opening, and the key is never readable on a closed
box. On opening, and whenever a newer database arrives while open, the
Postbote decrypts the newest one with `wadecrypt` into the vault's work
folder, runs `msgvault import-whatsapp --phone <number>` and removes the
plaintext; `whatsapp-stand` notes which database went in. msgvault keeps
one of each message, so the daily database adds only what is new. A wrong
key is said on the page and tried again once a new key is entered. Media
(photos, videos) are not imported yet.

### Paket format, version 1 (for the box and, later, the phone)

A packet is a file `<YYYYMMDDTHHMMSSZ>-<quelle>.age` in the Briefkasten:
the creation time in UTC (seconds; a taken name moves on by a second), the
source (`signal`, `whatsapp`, …). A producer writes it under a name
beginning with `.` and renames it into place when complete.

- **Encryption:** [age](https://age-encryption.org/v1) v1, one X25519
  recipient: the person's public key (`age1…`, from
  `/data/briefkasten.pub`, or for Haus `GET /intern/empfaenger`). Any
  age-compatible library works (Kotlin: e.g. kage / jagged).
- **Inside:** a POSIX tar (ustar or PAX), UTF-8 names, regular files only:
  1. `manifest.json` — **the first entry**:
     `{"format":"archiv-paket","version":1,"quelle":"signal","erstellt":"2026-11-01T22:00:00Z","herkunft":"box"|"telefon","sicherung":"signal-….backup"}`
     (`sicherung` optional; a mail packet has `"quelle":"mail"` and
     `"konto"`, the account). A reader refuses another format or version.
  2. `anhaenge/<name>` — one file per attachment, raw bytes; `<name>` is
     any file name without `/` (the box writes `000001`, `000002`, …).
  3. `nachrichten.jsonl` — one JSON object per line, one message each.
  Entries 2 and 3 may come in any order (the box writes attachments first).
  A **mail packet** instead carries `maildir/<folder>/…/cur/<name>`, one
  raw RFC 5322 message each (`<name>` = `<uidvalidity>.<uid>.<16 hex>:2,<flags>`,
  Maildir's flags D F R S T), and an empty `nachrichten.jsonl`. No level of
  the path is empty or begins with a dot; a reader refuses one that does.
- **A message:**

  | field | type | meaning |
  |---|---|---|
  | `id` | string, required | stable for this message within its source, the same in every packet (the box: `<quelle>:` + 32 hex of SHA-256 of its identity) |
  | `zeit` | integer, required | ms since 1970-01-01 UTC |
  | `richtung` | `"ein"` / `"aus"` | received / sent by the person |
  | `absender` | string | E.164 of the sender of a received message |
  | `teilnehmer` | array of strings, required | E.164 of everyone in the conversation except the person, sorted |
  | `name` | string | the contact's or group's name |
  | `text` | string | the text (several text parts joined by `\n`) |
  | `gelesen` | bool | read |
  | `quell_id` | string | the source's own message id, if any |
  | `anhaenge` | array of `{datei, typ, name}` | `datei` = the tar entry (`anhaenge/<name>`), `typ` = MIME type, `name` = original file name |

  Unknown fields are ignored by readers of version 1; a change that
  readers must understand is version 2.
- **The person's number** is the owner msgvault files the messages under
  (`--owner-phone`): msgvault keeps one account per owner number, so a
  changed number in „Schlüssel“ starts a second account and the same
  messages appear twice. Enter it once, in E.164.
- **New only:** a producer leaves out what it packed before. The box keeps
  that as the HMAC set above; the phone keeps its own. A message that comes
  twice does no harm (msgvault keeps one) — only the size suffers.
- **Into msgvault:** the Postbote writes each message as an SMS Backup &
  Restore element: no attachment and one other participant → `<sms>`
  (`type` 1/2 from `richtung`), else `<mms>` (`msg_box` 1/2, `addrs` with
  137 = sender and 151 = recipients including the person), attachments as
  base64 `part`s, `m_id` = `quell_id`.

## Status for laib

`/data/laib-status.json` (outside the vault, nothing secret): `at`,
`detail`, `findings[{severity, detail, fix}]`, plus `person`, `zustand`,
`bereit`, `wartend.<quelle>.{wartend, aelteste_seit}` (the inbox),
`briefkasten.{wartend, aeltestes}` (packets waiting for the opening),
`letztes_paket.<quelle>.{am, sicherung, paket, nachrichten, anhaenge}`,
`letzter_import.<quelle>.{am, datei, zahlen{sms,mms,anrufe,anhaenge,mails}, anzahl}`,
`letzter_fehler{am, quelle, datei, satz}`, `packt`, `liefert`
(`zahlen.signal`, `zahlen.whatsapp`: new messages of a direct import).
Findings (gelb): packets waiting more than three days while the archive is
not open („Dein Archiv ist seit … zu, X Pakete mit neuen Nachrichten
warten.“), Signal backups waiting more than three days while it is closed
(„Dein Archiv ist zu, X Signal-Sicherungen warten seit …“), a backup
waiting three days for an archive not set up, an error with its fix,
missing keys while backups wait, msgvault not starting. The record lives
beside it in `/data/archiv-stand.json`.


### When something fails

A packet or inbox file that fails stays where it is (nothing is lost) and
is tried again every half hour while the vault is open, and at every
opening — a cause that passes (a restart in the middle of an import, a busy
database) heals without anyone. The page and the status say the sentence.
The same file failing with the same sentence three times in a row
(`DreimalRot`) turns the status finding red: no key the person could give
fixes it, and a red wakes the box's Operator, which reads `laib logs` and
the file named in the finding. Failures a person can fix (a wrong
passphrase, a refused mail login) stay yellow and say what to enter.

## What the template must give (laib#986)

| what | value |
|---|---|
| image | `ghcr.io/mdopp/archiv:<sha>` (a database inside: pin, no `AutoUpdate`) |
| env | `ARCHIV_PERSON=<login>` (required), `PROXY_SECRET=!secret …` (the route's proxySecret, sent as `X-Archiv-Proxy`), `ARCHIV_HAUS_SECRET=!secret …` (shared with Haus, for `/intern/…`), `TZ`, optional `PUBLIC_URL` (used in the status' fix sentences), `LEERLAUF` (s, default 7200) |
| mounts | `${DATA_DIR}/archiv/<person>:/data:z` (the cipher folder, the Briefkasten, the keys, the status file; backed up), `${DATA_DIR}/archiv-eingang/<person>:/eingang:z` (read, write **and delete**: the archive removes packed backups and writes `letzte/`) |
| backup: false | `${DATA_DIR}/archiv-eingang/<person>/letzte` **must not** be in restic (`backup: false`): it is itself a backup of the phone, and a 2.7 GB Signal file that changes every night would add 2.7 GB to restic every night. (The rest of the inbox is transient and need not be backed up either.) |
| tmpfs | none needed: plaintext is only ever written inside the vault (its work folder). |
| disk | reading a Signal backup needs its decrypted size free on `/data` for a few minutes; Signal backups wait in the inbox while the vault is closed, one per night. |
| Haus' way in | `/intern/…` is on the same port 8080; Haus must reach it (host side, as Gesundheit's MCP port) with `X-Archiv-Haus: <ARCHIV_HAUS_SECRET>`. |
| FUSE | `AddDevice=/dev/fuse`, `AddCapability=SYS_ADMIN` (mount in the container's own user and mount namespace). On the runner Docker also needed `--security-opt apparmor=unconfined`; on Fedora CoreOS SELinux may need its own label option — unproven. |
| user | the supervisor runs as the container's root (it mounts, and msgvault's key file must belong to the process user). Rootless podman maps that root to the service user; no `UserNS=keep-id`. The inbox folder is shared with Haus: the archiv container must be able to delete what Haus wrote there (write permission on the folders Haus makes, which it makes 0775). |
| port | the container listens on `8080` (pages, msgvault proxy); publish `127.0.0.1:<port>:8080`, `lan: false`. msgvault's `8090` stays inside. |
| health | `http://127.0.0.1:<port>/healthz` (answers in every state) |
| status | `status: file: ${DATA_DIR}/archiv/<person>/laib-status.json` |
| stop | `TimeoutStopSec` ≥ 90: closing stops msgvault (≤ 30 s) and unmounts |
| backup | do not quiesce by stopping the unit (that locks the vault); while open, a snapshot catches a live SQLite in ciphertext — the restore is only as good as that moment. |

## Limits, honestly

- **Proven in CI** (rootful Docker, Ubuntu runner): gocryptfs init, mount,
  wrong password (exit 12), unmount; msgvault serving and importing from
  inside the gocryptfs mount; the supervisor's whole cycle; a Signal backup
  made by signalbackup-tools itself (its generated dummy, two messages
  added) decrypted by the archive with the passphrase typed into a pty and
  `--output <folder>/`, read by the real `import-signal` (2 new messages),
  kept in `letzte/`, the work folder empty after; packets made
  while the vault is closed (`archiv paket-aus-xml`), counted in the status,
  taken into the real msgvault on opening — an SMS and an MMS with a picture
  arrive, and a packet of messages msgvault already has adds nothing; a mail
  packet through the real `import-maildir`; WhatsApp media encrypted into
  `letzte/` and given back to Haus only while open; a non-backup in
  `signal/` fails with the sentence, stays, and leaves nothing in the
  vault's work folder or the Briefkasten.
- **Proven on the box** (the Kraftwerk, 2026-11-02): rootless podman on
  Fedora CoreOS mounts the vault with `/dev/fuse` and `SYS_ADMIN`. The
  first real Signal backup went through: signalbackup-tools read it into a
  packet of 41,437 messages and 6,202 attachments (2.6 GB) in about three
  minutes, and msgvault took all 41,437 in about 40 minutes. On the way it
  showed two things the tests now hold: signalbackup-tools writes a lone
  `></part>` for a missing attachment (skipped), and msgvault's SMS import
  refuses any attachment over 25 MiB and then fails the whole import (such
  files now go to `grosse-anhaenge/` in the vault, the message says where).
  The Gmail mailbox comes in over IMAP, oldest first, a few thousand mails
  per packet.
- **Unproven**: a real phone's Signal backup through `import-signal` (CI's
  backup is generated and tiny; the box's first one is the proof, with its
  time, the RAM signalbackup-tools needs and the disk the decrypted folder
  takes); SMS and calls from the Haus app (built, not yet on a
  phone); WhatsApp on a real backup (the decryption is proven on a
  generated one in `integration.sh`, msgvault's import of a real msgstore
  is not); how much RAM signalbackup-tools needs for a larger backup (it
  holds the backup's database in memory); WhatsApp (needs the 64-digit key;
  a passkey cannot be read out).
- **What import-signal leaves out** (msgvault's own list): SMS, MMS and
  calls Signal kept as the phone's SMS app (the phone's SMS come through
  the Haus app instead), group and safety-number updates, timer changes,
  payments and other events; stories and story replies; messages deleted
  for everyone; scheduled messages not yet sent; earlier versions of an
  edited message (the newest text stays, marked as edited); legacy groups
  never migrated to new groups, and MMS groups. Signal Android database
  version 185 and newer only.
- **Signal twice, for now**: messages that came in by the old way (XML
  packets, before 2026-11-02) are in msgvault's SMS account; the first
  `import-signal` puts the same history in the Signal account again. They
  stay untouched until a separate cleanup with the owner.
