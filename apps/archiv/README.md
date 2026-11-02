# archiv

Für die Familie in einem Satz: Dein Nachrichten-Archiv — Signal und E-Mail
(später WhatsApp und Discord) durchsuchbar auf der Box, in einem Tresor, den
nur dein Archiv-Passwort öffnet.

One container per person ([laib ADR 0029](https://github.com/mdopp/laib/blob/main/docs/adr/0029-one-message-archive-per-person.md),
laib#985, laib#986). Inside: a small Go supervisor (`archiv`, this folder),
[msgvault](https://github.com/wesm/msgvault) 0.22.0, gocryptfs (Debian
package), [signalbackup-tools](https://github.com/bepaald/signalbackup-tools)
20261014 and wa-crypt-tools 0.1.0 (for WhatsApp, not used yet); IMAP
through [go-imap](https://github.com/emersion/go-imap) v1.2.1.

- Code: `*.go`, tests: `go test ./...`; `integration.sh <image>` runs the
  built image with a real FUSE mount (CI does, on every push and PR).
- Image: `ghcr.io/mdopp/archiv:latest` and `:<sha>`, built by CI on main.
- Laib template: not yet — it waits for laib#986's per-person design. What
  it must give is below.

## The vault (Tresor)

The whole archive — msgvault's database, its API key, the Briefkasten's
private key — lives in a gocryptfs cipher folder, `/data/tresor`, and is
mounted at `/run/tresor` only while open.

| state | what is there |
|---|---|
| uneingerichtet | no `/data/tresor/gocryptfs.conf`. The page asks for the Archiv-Passwort twice, says losing it loses the archive (so: Vaultwarden) and that an open archive is readable by the box's admin. `gocryptfs -init`, then open; the first opening makes the age X25519 key pair (private: `/run/tresor/age-identitaet`, public: `/data/briefkasten.pub`). |
| verschlossen | ciphertext only. msgvault is stopped. One password field. |
| offen | mounted; `msgvault serve` runs with `MSGVAULT_HOME=/run/tresor/msgvault`, loopback `127.0.0.1:8090`, its key in `api-key` (generated on the first opening), telemetry off (config and env). The Postbote brings the Briefkasten's packets in. |

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
vault, so the Briefkasten can read backups while it is closed. They are
accepted only while the vault is open (so only the person sets them) and
never shown again: the page shows „hinterlegt am …“ and says the price in
plain words: whoever has root on the box can decrypt **future** backups with
the passphrase as they arrive, and read the person's mail with the app
password (which the provider can revoke); what is already in the archive
stays readable only with the Archiv-Passwort.

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

## The Briefkasten: importing while the vault is closed

Haus puts the backups into `/eingang/<quelle>/…` (haus#207); `.teil/`,
every dot-entry and `letzte/` are never read as inbox. The Briefkasten runs
every 20 s whether the vault is open or closed, as soon as the vault has
been made once (it needs the public key):

1. Each `signal/**/signal-*.backup` whose size and mtime stayed the same
   for a minute (as this process saw it — Haus sets the phone's mtime),
   **oldest first** (by mtime), once the Signal passphrase and the person's
   number are stored:
   `signalbackup-tools <backup> --exportxml /dev/shm/archiv/<rand>.xml --overwrite --setselfid <number> --no-showprogress`,
   the passphrase typed in by pty. `<rand>.xml` is a **named pipe**: the
   archive reads the XML from it as it is written, so the plaintext is never
   a file, not even in tmpfs. The pipe is removed in every case.
2. Only messages not packed before go into the packet: the set of packed
   messages is `/data/gesehen/signal`, HMAC-SHA256 of each message's
   identity under the key `/data/gesehen/schluessel` — 32 bytes per message,
   nothing readable.
3. ONE packet `/data/briefkasten/<UTC time>-signal.age` (written under a
   dot-name, moved into place when whole; none when nothing is new).
4. The backup moves to `/eingang/letzte/signal/` and the older one there
   goes; an older backup that arrives late is deleted after its packet.
5. On failure the backup stays in the inbox, the page and the status say
   why, no packet, no digest is added. A wrong passphrase („…IT IS LIKELY AN
   INCORRECT PASSPHRASE…“) becomes „Die Signal-Passphrase passt nicht zu
   dieser Sicherung. Gib sie neu ein.“, and no backup is tried with that key
   until a new one is entered.

When the vault is open, the **Postbote** takes the packets, oldest first:
decrypts with the private key from the vault, unpacks the attachments into
`/run/tresor/arbeit/`, writes them as an SMS Backup & Restore XML there,
`msgvault import-synctech-sms <xml> --owner-phone <number>` (through the
running daemon), deletes the packet, empties the work folder (always, also
on failure; a crash's leftovers go at the next opening). A packet that
fails stays and is tried at the next opening.

**Additive only.** msgvault upserts every message by a stable id; its
`import-synctech-sms` has no flag that deletes or mirrors deletions (only
`--sms/--mms/--calls/--attachments`), and nothing here runs another
msgvault command. A message deleted on the phone is simply not in the next
packet; what is in the archive stays. CI imports two XMLs into the real
msgvault (the second lacking a message, then the first again: still 2,
no duplicates), then a packet of the same messages: still no duplicates.

### letzte/: the newest backup stays for a new phone

| source | in `/eingang/letzte/` |
|---|---|
| Signal | the newest packed `signal-*.backup`, as the phone wrote it (encrypted by Signal) |
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
   SHA-256 in `/data/gesehen/mail`, as for Signal) is left out — also a
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
copy in another folder only gets the label. Additive as for Signal.
`archiv paket-aus-mails -konto <adresse> <ordner>` packs a folder of
`.eml` files the same way (an old export; CI uses it).

**WhatsApp's messages and Discord** are not imported yet: WhatsApp needs the
64-digit key (`wadecrypt`, then `msgvault import-whatsapp`) — a next step,
which will write packets as Signal does. Discord's data package has no
importer in msgvault.
`archiv paket-aus-xml -telefon <number> [-quelle sms] < backup.xml` packs
any SMS Backup & Restore XML (an old Android SMS backup too).

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
`letzter_fehler{am, quelle, datei, satz}`, `packt`, `liefert`. Findings
(gelb): packets waiting more than three days while the archive is not open
(„Dein Archiv ist seit … zu, X Pakete mit neuen Nachrichten warten.“), a
backup waiting three days for an archive not set up, an error with its fix,
missing keys while backups wait, msgvault not starting. The record lives
beside it in `/data/archiv-stand.json`.

## What the template must give (laib#986)

| what | value |
|---|---|
| image | `ghcr.io/mdopp/archiv:<sha>` (a database inside: pin, no `AutoUpdate`) |
| env | `ARCHIV_PERSON=<login>` (required), `PROXY_SECRET=!secret …` (the route's proxySecret, sent as `X-Archiv-Proxy`), `ARCHIV_HAUS_SECRET=!secret …` (shared with Haus, for `/intern/…`), `TZ`, optional `PUBLIC_URL` (used in the status' fix sentences), `LEERLAUF` (s, default 7200) |
| mounts | `${DATA_DIR}/archiv/<person>:/data:z` (the cipher folder, the Briefkasten, the keys, the status file; backed up), `${DATA_DIR}/archiv-eingang/<person>:/eingang:z` (read, write **and delete**: the archive removes packed backups and writes `letzte/`) |
| backup: false | `${DATA_DIR}/archiv-eingang/<person>/letzte` **must not** be in restic (`backup: false`): it is itself a backup of the phone, and a 2.7 GB Signal file that changes every night would add 2.7 GB to restic every night. (The rest of the inbox is transient and need not be backed up either.) |
| tmpfs | `/dev/shm` as the container has it is enough: only a named pipe lives in `/dev/shm/archiv` (`KLARTEXT`); no plaintext file is ever written. |
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
  inside the gocryptfs mount; the supervisor's whole cycle; packets made
  while the vault is closed (`archiv paket-aus-xml`), counted in the status,
  taken into the real msgvault on opening — an SMS and an MMS with a picture
  arrive, and a packet of messages msgvault already has adds nothing;
  WhatsApp media encrypted into `letzte/` and given back to Haus only while
  open; a non-backup in `signal/` fails with the sentence, stays, and leaves
  nothing in `/dev/shm/archiv`, the vault's work folder or the Briefkasten.
- **Unproven**: rootless podman on the box (Fedora CoreOS, SELinux) mounting
  FUSE with these flags; signalbackup-tools on a real backup, and writing
  its XML into a named pipe (its code writes forward only, but no real
  backup has gone through it yet).
- **Only the first real Signal backup can tell**: whether this
  signalbackup-tools reads the phone's backup format (Signal changed its
  backup format in 2025; the XML export is, in its author's words, not well
  tested), how many messages the XML carries — **messages of contacts
  without a shared phone number are skipped** by the XML export (Signal has
  made the number optional), quotes, reactions and edits are lost (SMS XML
  cannot hold them), groups with the same members merge — and how long a
  multi-GB backup takes and how much RAM signalbackup-tools needs for it
  (it holds the backup's database in memory; the box's RAM is the limit,
  not the disk). The first packet of a person holds the whole history.
