# archiv

Für die Familie in einem Satz: Dein Nachrichten-Archiv — Signal, SMS,
Anrufe und E-Mail (später WhatsApp und Discord) durchsuchbar auf der Box, in
einem Tresor, den nur dein Archiv-Passwort öffnet; mit den Namen aus deinen
Handy-Kontakten und jedem Termin aus deinen Kalendern.

One container per person ([laib ADR 0029](https://github.com/mdopp/laib/blob/main/docs/adr/0029-one-message-archive-per-person.md),
laib#985, laib#986). Inside: a small Go supervisor (`archiv`, this folder),
[msgvault](https://github.com/kenn-io/msgvault) 0.22.0 with `import-signal`
(our fork's image `ghcr.io/mdopp/msgvault:0.22.0-signal.2`, pinned by digest,
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

Where the keys the person enters live:

| key | where | why |
|---|---|---|
| own number | `/data/schluessel/telefon`, outside the vault, 0600 | no secret; the page and the status say it is missing (masked number) while the vault is closed |
| IMAP mailboxes (name, host, port, user, app password; one or more) | `/data/schluessel/imap`, outside the vault, 0600, a JSON list (one object before 2026-11-11, read as the first mailbox) | the Mailholer fetches mail while the vault is closed |
| deleting old mail at the provider: on/off, „älter als“, the person's tap, the last count | `/run/tresor/mail-aufraeumen.json`, **inside** the vault | it runs only while the vault is open: the proof needs msgvault |
| Signal passphrase | `/run/tresor/signal-passphrase`, **inside** the vault, 0600 | Signal is read only while the vault is open (since 2026-11-02) |
| WhatsApp key | `/run/tresor/whatsapp-schluessel`, **inside** the vault, 0600 | WhatsApp is read only while the vault is open |

All of them are accepted only while the vault is open (so only the person
sets them) and never shown again: the page shows „hinterlegt am …“. For
the keys outside it says the price in plain words: whoever has root on the
box can read the person's mail with the app password (which the provider
can revoke); what is already in the archive stays readable only with the
Archiv-Passwort. The keys inside are readable only while the vault is
open; closed, the page cannot tell whether the Signal passphrase is there
and says it is entered while open.

Until 2026-11-02 the Signal passphrase was kept outside, in
`/data/schluessel/signal` (owner: „Bring den Key dann doch in
Sicherheit“). Every opening moves one left there into the vault
(`Schluessel.SignalEinziehen`): written inside atomically and read back,
unless the vault already holds one (that one is newer: the page writes only
inside), then the outside file is overwritten with zeros and removed, and
one log line says so, without the secret. Nothing there: nothing happens.
On an error the outside file stays and the next opening tries again.

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
  for Homes' control). Open, `/tresor` is the workplace (owner 2026-11-10,
  in Homes' look: its tokens, rows, pills and quiet lines, phone width
  first): a top bar with „Abschließen“, the search (`/tresor/suche`: the
  contacts whose name matches — umlauts and their spelled-out form, and
  case, count the same, „Jürgen“ finds „Juergen Burger“ — then msgvault's
  own full-text page, in the other spelling too), then **Fragen** (one
  question at a time, „Ist das dieselbe Person?“ below), **Steht nahe**,
  **Stammbaum** (step 7 below) and **Gruppen**; the four tabs wrap
  rather than scroll, so none is off the screen. The work comes first
  (owner 2026-11-11): when the vault closes by itself and that the box's
  admin can read it while open is one quiet line at the end of the page.
  „Zu wem?“ goes on with the tapped name; nobody by that name offers
  „<Name> anlegen“ first (a person by hand, below), keeps the name in the
  field and leads on (the messages, back to the person).
  `ARCHIV_SCHAU=127.0.0.1:8671 go test -run 'TestSchau$' .` serves the
  fixture archive for looking at the pages in a browser. The sources are one line
  („✓ aktuell“, what runs, or what needs the person); their details and
  setup are `/tresor/quellen`, open or closed: one folded line per source
  with its state („Passphrase fehlt“, „zuletzt am 10.11.2026: 1.234
  Nachrichten“), a tap opens its card and form (owner 2026-11-11); after
  a failed save that card stays open with the error in it. Closed and new, the page
  opens or makes the vault in the same look.
- The start: a browser opening `/` (a top-level navigation, no query)
  goes to `/tresor`, open or closed (owner 2026-11-11). The workplace's
  one link to msgvault, „Alle Nachrichten“, is `/nachrichten`,
  which msgvault gets as `/`; the way back is the browser's.
- Every other path, while open: msgvault's own web UI and API (it lives at
  the root: absolute asset paths, no frames; its shell answers every path
  without an extension, and its state is in the query, so `/?…`, a fetch
  of `/`, its assets and a bearer still reach it). The supervisor adds
  `X-API-Key` itself; Caddy's headers and Authelia's cookie stay behind.
  While closed, other GETs redirect to `/tresor`.
- An agent (`/p/<person>/…`, the prefix stripped by Caddy, past Authelia):
  `Authorization: Bearer <msgvault's key>` goes to msgvault unchanged while
  open; while closed, 423 `{"fehler": "Das Archiv ist abgeschlossen. …"}`.
  A forged `Remote-User` on this way gets no key. How the agent learns the
  key (it is in the vault) is laib#986's question.

## The inbox

Homes puts the backups into `/eingang/<quelle>/…` (homes#207); `.teil/`,
every dot-entry and `letzte/` are never read as inbox. A file counts once
its size and mtime stayed the same for a minute (as this process saw it —
Homes sets the phone's mtime).

### Signal: read while open, by msgvault's import-signal

Since 2026-11-02 (owner's decision; before, signalbackup-tools exported an
SMS Backup & Restore XML into a named pipe while the vault was closed, and
the messages went in as packets under msgvault's SMS account). msgvault's
`import-signal` reads the decrypted backup as a folder — its SQLite
database and the attachment files — and that plaintext may exist only
inside the vault. So Signal backups wait, still encrypted by Signal, in
the inbox until the vault is open. Then the Postbote takes each stable
`signal/**/signal-*.backup`, **oldest first** (by mtime), once the Signal
passphrase (inside the vault, `/run/tresor/signal-passphrase`, entered on
the page while open) and the person's number are stored:

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
the public key into the inbox for the Homes app, keeps WhatsApp's newest
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

Homes serves the database files straight from its own mount of the inbox.
Media need the private key: Homes asks the archive,
`GET /intern/whatsapp/medien/<path>` with `X-Archiv-Homes:
<ARCHIV_HOMES_SECRET>`, answered only while the vault is open (423 while
closed). `GET /intern/empfaenger` gives Homes the public key.

### Mail: fetched every hour, vault closed or open

Owner, 2026-11-02 („A“). Once an IMAP access is stored (host, user, app
password), the Mailholer runs at start, every hour (`MAIL_TAKT`, seconds)
and at once when an access is entered anew. Since 2026-11-11 the person may
store several mailboxes (name, host, port, user, app password); each is
fetched as below, one after the other, with its own fetch state
(`/data/gesehen/mail-stand-<id>.json`; the mailbox stored before keeps
`mail-stand.json` and the id `erstes`, so nothing is fetched twice), its own
refused-login wait and its own error on the page. Another host or user for
an entry starts its state over; removing one forgets its state, never what
it brought. Each mailbox's mail is imported under its own address as
msgvault's account.

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

### The household's document mail (owner, 2026-11-11)

Paperless reads one mailbox, the domain's catch-all, and files what comes
to `<login>+doc@<domain>` (and `<login>+doc+<anything>@`) as that person's
(laib `templates/paperless`, laib#1160). Where the box gives the archive
that mailbox (`DOKUMENTE_MAIL_*`, below), the Mailholer reads it each round
too: only the folder Paperless reads (`DOKUMENTE_MAIL_ORDNER`, INBOX), read
only (`EXAMINE`, `BODY.PEEK`: Paperless' flags stay), `UID SEARCH TO
<login>+doc` to narrow, and then the archive's own check, because IMAP's TO
is a substring (`hanna+doc` holds `anna+doc`): a mail goes in only when a
recipient in To, Cc, Delivered-To, X-Original-To or Envelope-To has the
local part `<login>+doc` or `<login>+doc+…` at exactly the domain. Packed as
any mail, under the account `<login>+doc@<domain>`, label „Dokumente“,
without Paperless' flags. The person enters nothing; the page says it is
on, or that the admin has not given it yet. Nothing is ever deleted there.

The price: the archive holds the document mailbox's login, which reads the
whole mailbox (everyone's document mail, and what else that mailbox gets);
the check above is the archive's, not the provider's. Laib gives it per
person (`DOKUMENTE_MAIL_PASSWORD_<PERSON>`, set by the owner), so the owner
decides for whom, and a read-only login at the provider, where one exists,
is better than the mailbox's own password.

`GET /tresor/mail?id=<Message-ID>` opens that mail in msgvault's own page
(`/messages/<id>`; while closed it sends the person to open the archive
first). Paperless keeps that link on a document that came to a person's
`+doc` address, as a note „Die Mail dazu im Archiv“.

### Old mail deleted at the provider (opt-in, owner 2026-11-11)

Per mailbox, off by default (`aufraeumen.go`). The person switches it on
on the sources' page with „älter als“ N months or years (default 24
months). Then:

1. **A dry run** (EXAMINE, nothing changes): it counts the messages older
   than the limit (the server's INTERNALDATE) that are not flagged, not a
   draft, not marked deleted, outside Drafts, Junk and Trash, and of those
   the ones **proven in the archive**. The page asks: „1.234 Mails älter
   als 2 Jahre, alle im Archiv – jetzt beim Anbieter löschen?“ (or how many
   are not in the archive yet and stay), and says it cannot be undone at
   the provider, while the archive keeps them. Counted again after a day,
   and whenever the limit changes.
2. **The person's tap** (`POST /tresor/mail/loeschen`) on that count
   starts the first deletion; another limit, switching off, or another
   host/user for the entry takes the tap back.
3. **Monthly** after that, while the vault is open (`AUFRAEUMEN_TAKT`, s,
   default 3600, is how often it looks whether a run is due). The page shows
   the last run's count and its error, if any.

Proven in the archive means: msgvault (`POST /api/v1/query`, read only,
over its analytics cache) holds an undeleted message with the same
Message-ID whose content id is `<type>-<SHA-256 of the raw bytes>`, the
SHA-256 of what the server sends now (`import-maildir` keys a message as
`maildir-<sha256>`; `integration.sh` checks that against the real
msgvault). The headers are read first; only a message with a Message-ID
msgvault knows is fetched whole and hashed. Not proven, never deleted: no
Message-ID; fetched but not imported yet; in msgvault with other content;
anything while msgvault does not answer (the vault closed, its index still
being built, a query error): then the run deletes nothing and says so.

Deleting needs a writable session: only this uses `SELECT` (the fetch
stays `EXAMINE`); the app password of the fetch is enough. Gmail (a server
with an `\All` folder) gets a `UID MOVE` into its `\Trash` folder, which
Gmail empties after 30 days; without MOVE or a Trash folder nothing is
deleted. Elsewhere `UID STORE +FLAGS.SILENT (\Deleted)` and `UID EXPUNGE`
of exactly those messages (UIDPLUS); a server without UIDPLUS gets a plain
`EXPUNGE` only when the marked messages in the folder are exactly ours,
else the marks are taken back and nothing goes. The document mailbox is
never cleaned. A removed mailbox forgets its settings.

**Discord** is not imported: its data package has no importer in msgvault.
`archiv paket-aus-xml -telefon <number> [-quelle sms] < backup.xml` packs
any SMS Backup & Restore XML (an old Android SMS backup too).

### SMS and calls: encrypted on the phone

The Homes app (switch „SMS und Anrufliste übertragen“, ADR 0029 §9) writes
the phone's SMS, MMS and call log as SMS Backup & Restore XML, encrypts
each file with age to the Briefkasten's public key, and uploads it to
`/eingang/sms/<name>.xml.age`. The key reaches the phone this way: the
Briefkasten writes it into the inbox as `.briefkasten.pub` (a dot-file,
never a backup), Homes serves it at `GET /napi/sicherung/schluessel`.

These files need no packing: they are already unreadable on the box. When
the vault opens, the Postbote decrypts each into the vault's work folder
and runs `msgvault import-synctech-sms` with the person's number
(`--owner-phone`; without the number the files wait). msgvault keeps one
of each message and call, so a file sent twice adds nothing. Imported,
the file leaves the inbox; one that cannot be decrypted stays, and the
page says so. Attachments over 25 MiB fail msgvault's SMS import here
(the packet path sets them aside, this one does not yet).

Contacts take another way: Homes mirrors the phone's contacts into the
person's Radicale address book `handy-kontakte`, outside the vault; the
archive reads them from there (below, "Contacts and calendars").

### Facebook and LinkedIn: shared from the phone (`sozial.go`)

Owner, 2026-11-11 („Runterladen würde ich ja, aber das ist entweder ein
‚Teilen' mit der Homes-App oder eine E-Mail-Weiterleitung und das System
macht den Rest“).

1. The person downloads Facebook's „Deine Informationen herunterladen“
   (format **JSON**, at least „Freunde“) or LinkedIn's „Kopie deiner Daten
   anfordern“ (with „Kontakte“) on the phone, and shares the file (the
   ZIP as it is, or the friends JSON or `Connections.csv` alone) to the
   Homes app's „Ins Archiv“.
2. The app tells the export by its structure (a ZIP by its entry names,
   a single file by its first bytes), refuses anything else on the spot,
   encrypts it on the phone with age to the Briefkasten's key (as the
   SMS) and uploads it through `/napi/sicherung/uploads` to
   `/eingang/sozial/<facebook|linkedin>-<UTC>.<zip|json|csv>.age`.
   Homes stores ciphertext only and never reads it.
3. While the vault is open, the Postbote decrypts each into the work
   folder (removed on every path), reads it **by its content**: Facebook's
   `connections/friends/your_friends.json` (2024),
   `friends_and_followers/friends.json` (2022) or `friends/friends.json`,
   `{"friends_v2": [{"name", "timestamp"}]}` with Facebook's byte-wise
   escaping of non-ASCII names undone; LinkedIn's `Connections.csv` after
   its „Notes:“ preamble, columns by name (First Name, Last Name, Email
   Address, Company, Position, Connected On; the profile URL is not kept).
4. The entries go into `/run/tresor/umfeld/sozial.json` (0600, format
   `archiv-sozial` version 1), additive: a new one is added, a known one
   (source, name, e-mail) takes the new company and position, none is
   removed. The file leaves the inbox. One that cannot be decrypted stays
   and the card says so; one that is no export (or Facebook's HTML
   format) is taken out with the sentence, since no later try could read it.

What they do in Umfeld: a LinkedIn connection is the card with its e-mail
(if the connection shared one), else, as a Facebook friend, the one node of
the same name (umlauts and case alike; two of that name: neither). There
it is a reason on the person's tie — „Kollege bei ACME GmbH (Entwickler,
LinkedIn seit 2019)“, „kennen sich (Facebook seit 2014)“ — on „Steht nahe“
and the edge page; it adds no strength and makes no tie by itself. An
entry matching nobody becomes a node of its own (`sozial:<name>`, no tie,
no contact), so „+ Beziehung“ suggests and finds it; an answer about it
stays in the vault. Nothing is written to an address book, no contact is
made, nothing goes to Homes (homes ADR 0002: Homes' „Wer ist das?“ keeps
suggesting the phone's contacts only; feeding the social names there too
would be a separate decision).

**Mail forwarding does not work for these:** both platforms send only a
notice with a link to their own download page, which needs their login;
the data is never attached. So there is nothing to forward to
`<login>+doc+import@…`; the sources' card says so.

### WhatsApp: only while open, the key inside the vault

The person enters the 64-digit key of their WhatsApp backup on the page
while the vault is open; it is stored inside the vault
(`whatsapp-schluessel`), as the Signal passphrase is. WhatsApp's
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
  `/data/briefkasten.pub`, or for Homes `GET /intern/empfaenger`). Any
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

## Contacts and calendars (from Radicale, read only)

Owner, 2026-11-02: contacts and calendars go from the phone into the
archive. Both already live in the box's Radicale, outside the vault:

| what | where in Radicale | who writes it there |
|---|---|---|
| the phone's contacts | `<person>/handy-kontakte/` | Homes (the Homes app mirrors the phone's address book daily) |
| the person's Google calendars | `<person>/google-<name>/` (not `google-kontakte`, a Takeout's address book) | laib's `kalender` service, every 15 minutes |
| calendars made in Homes „nur für mich“ | `<person>/privat-<name>/` | Homes |

While the vault is open the **Abgleich** (`abgleich.go`) reads them: at the
opening, then every `KONTAKTE_TAKT` seconds (default 3 hours), and on
„Jetzt abgleichen“. Closed, nothing is read. The archive logs into
Radicale with its own account (`RADICALE_USER`, `archiv-<person>`), which
Radicale lets read exactly these collections of exactly this person and
write nothing (the rights below); the client sends only PROPFIND, REPORT
and GET.

**Contacts → msgvault persons.** msgvault 0.22.0 is a CardDAV client: its
`add-carddav` connects an address book, `sync-carddav` pulls changes, and
a *subscribed* book's cards become msgvault persons whose numbers and
addresses resolve the senders of WhatsApp, Signal, SMS and mail to names
(a *lookup source* keeps the cards for that matching). msgvault never
reads Radicale itself. Its sync mirrors the book it reads: a card gone
from it takes its person away. The owner, 2026-11-02: „Warum sollte der
Kontakt auch gelöscht werden? Warum nicht nur mit gelöscht im Archiv
lassen?“ So the archive keeps its own **contact store** (`kontakte.go`),
inside the vault, `/run/tresor/kontakte/handy-kontakte.json` (format
`archiv-kontakte`, version 1): each round reads the whole book from
Radicale (one REPORT addressbook-query) and merges it in, like the
calendars below —

- a card is keyed by its UID (without one, by its file name) and keeps
  the file name it was first seen under;
- while it exists at the source its newest version wins; earlier
  versions are kept (up to 20; a new REV or PRODID alone is no change);
- a card gone at the source **stays**, marked
  `auf_dem_handy_geloescht_am`; if it comes back (even under another file
  name), the mark goes;
- a card the phone sends under a **new UID** (2026-11-10: six contacts
  were reported deleted and 26 new, all still on the phone) is the same
  contact when, in the same sync, the old UID is gone and the new one
  has the same name (umlauts and their spelled-out form, and case, alike)
  and a number (last nine digits) or an e-mail in common: the old card
  takes the new UID and data and keeps its history, its first sight and
  the file name msgvault knows; it is neither deleted nor new. A card an
  older archive already marked deleted in the sync that first saw its
  new UID is put right the same way. Only a card nothing matches is
  marked deleted;
- the whole book gone from Radicale: every card stays as it was, the
  store marked `quelle_weg_seit`.

msgvault reads this store, not Radicale's book. It could not reach
Radicale anyway, for three reasons in its own code: with a
password it speaks only HTTPS; it dials only public addresses, or a
private one pinned in its `config.toml` (`[carddav] trusted_origin`,
`trusted_addresses`; never loopback, never link-local, and
`host.containers.internal` is link-local under podman); and it finds the
books by RFC 6764 discovery from the login's principal, which for the
archive's account is `/archiv-<person>/`, an empty home. So the
supervisor runs a **relay** (`relais.go`) for msgvault, only while open:

- HTTPS on a private address of the container (port `KONTAKTE_RELAIS_PORT`,
  default 18443, inside the container's own network namespace; nothing is
  published), under the name `kontakte.archiv.invalid` (RFC 2606: it
  resolves nowhere). At every opening, before msgvault starts, the
  supervisor makes a CA that may sign only that name (an X.509 name
  constraint) and the relay's certificate, writes the system's CAs plus
  that CA to `msgvault/kontakte-ca.pem` (msgvault gets it as
  `SSL_CERT_FILE`) and pins the name to today's address in msgvault's
  `config.toml` (only those two keys of `[carddav]`; msgvault writes the
  rest itself).
- It answers the discovery itself (principal and address-book home
  `/<person>/`, one book `handy-kontakte` in it once the store has cards,
  with the privilege `read` only, so msgvault never picks it as the place
  to publish) and serves that book **from the contact store**: PROPFIND
  lists every card with its ETag, REPORT addressbook-query gives every
  card with its data, addressbook-multiget the ones asked for, GET one
  card. sync-collection is not offered (501): msgvault then reads a whole
  snapshot each sync, and a snapshot of the store never loses a card. A
  card deleted on the phone is served with one more line,
  `CATEGORIES:Auf dem Handy gelöscht am <TT.MM.JJJJ>` (its ETag changes
  with the mark). msgvault 0.22.0 keeps that line in the card it stores
  but projects only FN, EMAIL and TEL of a card into the person
  (`internal/store/carddav_resources.go`), so msgvault's own pages do not
  show the mark (checked in CI): the person and its numbers stay, and the
  archive's page says who is deleted on the phone.
- The relay talks to nobody. msgvault never sees the Radicale password;
  it logs into the relay as `msgvault` with the relay's own password, made
  once and kept in the vault (`kontakte-relais`), given to `add-carddav` on
  stdin.
- **Only reading is answered:** PROPFIND, REPORT, GET, HEAD, OPTIONS. PUT,
  DELETE, PROPPATCH, MKCOL, MOVE … get 403; any path outside
  `/<person>/handy-kontakte/` is 404. msgvault cannot publish to the
  person's address book whatever its roles say.

The commands (all through the running daemon, built in `befehl.go`): once,
`msgvault add-carddav https://kontakte.archiv.invalid:18443/ msgvault`
(the relay's password on stdin; no `--schedule`: the supervisor syncs);
every round, if the book's roles are not exactly these,
`msgvault carddav books set-role <id> --subscribed --lookup-source`
(`set-role` replaces all three roles, so it is never the write target);
then `msgvault sync-carddav`. The book's roles are read from msgvault's API
(`GET /api/v1/carddav/books`). The page says how many contacts the archive
holds, which of them are deleted on the phone („Ben (auf dem Handy
gelöscht am 02.11.2026)“, the newest ten), when the last sync went through
and how many persons msgvault has; the merge preview and its lists mark a
person whose card is deleted on the phone (by the card's UID: msgvault's
`carddav_bindings[].remote_uid` or `vcard_uid`).

What a sync does in msgvault: a new or changed card updates its person; a
card deleted on the phone stays in the store, so its person **stays**,
with its name and numbers; the archive's page marks it „auf dem Handy
gelöscht am …“, back on the phone the mark goes. **Messages are never
touched.**

**Calendars → the vault.** msgvault 0.22.0 cannot import CalDAV (its
calendar support is Google's API only), so the archive keeps them itself:
one file per calendar, `/run/tresor/kalender/<collection>.json` (format
`archiv-kalender`, version 1), inside the vault. Each round fetches every
`google-*` and `privat-*` calendar whole (a GET on the collection) and
merges it in:

- an event is keyed by its UID and RECURRENCE-ID (with its TZID), so each
  changed occurrence of a series is its own entry; an event without UID
  by a hash of its content;
- what is stored is the whole VEVENT as the source last had it (with its
  VALARMs, lines unfolded), the time zones it uses, title and start, when
  it was first and last seen;
- a changed event keeps its earlier versions (up to 20); a new DTSTAMP
  alone is no change (Google stamps every export anew);
- an event gone at the source **stays**, marked
  `an_der_quelle_geloescht_am`; if it comes back, the mark goes;
- a calendar gone from Radicale stays with all its events, marked
  `quelle_weg_seit`.

Nothing is ever removed from that store. The page shows per calendar how
many events the archive holds and how many of them are gone at the
source. **Searching events is a later step** (not built): today they are
kept, counted and readable as files in the open vault.

**„Ist das dieselbe Person?“** On the open page (`personen.go`), msgvault's
identity suggestions that are still open (`GET
/api/v1/identity/match-candidates?state=candidate`, then `state=conflict`;
the API takes one state per request, its CLI's `all` it refuses) are put to the person in plain German: who the two are (a
participant's name and address, `GET /api/v1/participants/{id}`; a saved
person's name; „ein Kontakt aus deinem Handy-Adressbuch“), why
(„Beide haben die Telefonnummer +49 …“, „Beide heißen …“ with a warning,
the same e-mail address, account or user name), and where it was found
(in the address book or in the messages). Ja and Nein post the review
token the page showed to `…/{id}/review/accept` or `…/review/reject`. If
msgvault answers `identity_match_review_stale` (the evidence changed), the
page fetches the suggestions again and says „Der Vorschlag hat sich
inzwischen geändert …“, with the new token in the form. A suggestion
between two persons that are both saved already cannot be accepted
(msgvault: `person_merge_required`); it shows „Zusammenführen ansehen“
instead. Suggestions msgvault itself marks as not decidable here are
counted, not shown. At most 10 at a time; the list is fetched again at
most every 30 s and after every decision.

**Two persons by hand.** „Zwei Personen von Hand zusammenführen“: two
lists of the saved persons, then a **preview** (the archive's own:
msgvault 0.22.0 has no merge preview) with both names and every number and
address msgvault knows for each (`GET /api/v1/people/{id}/identities`),
who stays and who goes in, and „Andersherum“. „Ja, zusammenführen“ posts
`POST /api/v1/people/{survivor}/merge` with `If-Match` carrying the two
revisions the preview showed and an `Idempotency-Key`: if either person
changed since, msgvault refuses (`person_merge_revision_conflict`) and the
page shows the preview again with the current state. msgvault records the
merge and can split it again (`msgvault person split`).

Where it lives:

| what | where | read-only? |
|---|---|---|
| contacts | Radicale `<person>/handy-kontakte/` (outside the vault, the admin can read it) → `/run/tresor/kontakte/handy-kontakte.json` → (relay) → msgvault's persons in the vault | Radicale: read only; the store only grows; msgvault reads the store |
| calendars | Radicale `<person>/google-*`, `privat-*` → `/run/tresor/kalender/*.json` | Radicale: read only; the vault's store only grows |
| Radicale login | `RADICALE_PASSWORD_FILE` (a podman secret, mounted), never an environment variable, argv or log | — |
| relay's password, its CA bundle | `/run/tresor/kontakte-relais`, `/run/tresor/msgvault/kontakte-ca.pem` (inside) | — |
| msgvault's CardDAV credential (the relay's password) | `/run/tresor/msgvault/tokens/carddav.json` (inside) | — |

The status file gets `abgleich.{eingerichtet, kontakte_am, kalender_am,
fehler}` (times and a sentence, no names, no counts); a refused Radicale
login is a yellow finding.

## Umfeld: who stands close to whom (archiv#27)

Owner, 2026-11-10 (homes ADR 0002): the contact net moves from Homes
(`internal/netz`) into the person's archive. It is computed and kept only
inside the open vault; closed, there is no net, not even in memory.

**Step 1:** the person's Immich key, „Steht nahe“ and the picture.

- **The key.** The person makes an API key in Immich (Kontoeinstellungen →
  API-Schlüssel) with `asset.read`, `asset.view` and `person.read` and
  pastes it on the vault page under „Umfeld“. The archive asks Immich
  (`GET /api/api-keys/me`) and stores it only if those rights are there,
  else it names the missing ones in German. It lives in the vault,
  `/run/tresor/immich-schluessel` (0600), like the WhatsApp key; never in
  `/data`, argv, the environment or a log. Immich's address comes from
  the box (`IMMICH_URL`); without it the page says Umfeld is not set up.
- **What is read** (`umfeld.go`), only reading: every photo of the key's
  account with at least one person (`POST /api/search/metadata`,
  `withExif`, `withPeople`, 500 a page, `dateTimeOriginal`; a photo
  without it is skipped); the e-mail of the account (`/api/users/me`, if
  the key may: it finds the person's own card, else their number does);
  the phone's book from the archive's contact store (cards deleted on the
  phone left out); and the household book `homes/haushalt-kontakte`
  through the archive's Radicale login (refused until Radicale's rights
  allow it: the page says so and the net is made without it).
- **The net** (`netz.go`, `telefonbuch.go`: Homes' numbers, its tests
  ported with the same fixture, the ties pinned to six places). A photo
  day in a cell of about 1 km is one event; a pair counts 1/(n−1) of it,
  halved every 4 years, plus 0.15 per year they meet in; the rank divides
  by how often both appear at all (hub); a tie from photos alone shows
  from two days on. The phone books add the same address (1.5, groups up
  to 12), the same company (0.4, on up to 8 people's cards, never a
  placeholder like „Privat“) and a relation (3): vCard 4
  `RELATED`, iOS `X-ABRELATEDNAMES` with its `X-ABLabel`, Android
  `X-ANDROID-CUSTOM` relation rows. A contact is the Immich person of the
  same name (Homes keeps them named alike). A shared company alone is no
  tie (owner 2026-11-11): it is the last kind of question, „Kennen sich
  X und Y? (Beide Karten nennen die Firma …)“, Ja a tie the person stated
  (never written to an address book), Nein never asked again. A work
  address (`ADR;TYPE=work`, vCard 2.1 `ADR;WORK`) is a company's
  (owner 2026-11-11): it is never a home, a tie or a neighbour's street,
  only the same question („Beide Karten nennen die Arbeitsadresse …“).
  The employer the box's model reads from mail makes no tie at all. Every
  phone-book reason says its value („Gleiche Adresse auf beiden Karten:
  Mühlenweg 4“, „Max Langs Karte nennt Ute Lang als Schwester“).
  Shared acquaintances („Gemeinsame Bekannte: …“ on the edge and person
  pages) are context only: they never make or strengthen a tie.
- **Kept in the vault**: `/run/tresor/umfeld/netz.json` (format
  `archiv-umfeld`, version 1: the photos' ids, times, places and persons,
  the names and Immich's birthdays, the contacts with their family name,
  birthday and streets, the ties, the household's cards as last read),
  `verborgen.json` (who is hidden), `antworten.json` (step 2),
  `gruppen.json` and `hinweise.json` (step 5), `gruppen-modell.json`,
  `geburtstag-hinweise.json` and `geburtstage.json` (step 8),
  `baum-weissnicht.json` (step 7). Every round reads everything
  again and rebuilds (a merge or a rename in Immich cannot drift); Immich
  out of reach keeps the last photos.
- **When**: at the opening, then every `UMFELD_TAKT` seconds (default an
  hour), and at once after a new key. A round does not hold the vault
  open (an hourly round would outlast `LEERLAUF`); the closing ends it.
- **Pages**: the vault page's card „Umfeld“ (the key, how far the round
  is, what is missing) and `/tresor/umfeld`: „Steht dir nahe“, a radial
  picture and the ranked list, any person into the middle (`?mitte=`),
  the hidden ones restorable. On top one field finds a person by name
  (`?suche=`, `gemeinsam.go`; owner 2026-11-11: „Ich kann nicht nach
  einer Person suchen“): umlauts in every spelling, part of a name, a
  phone book's name for somebody Immich names otherwise („Papa“), a
  card's NICKNAME („Hasi“, kept in the round's table; the person's page
  says it, „auch: Hasi“); one
  hit opens the person, several are offered, the closer first; the
  search on top of the archive lists persons too. A person's page leads
  with who she is to the person: the relation, how close (the picture's
  rings, „Nr. 3 von 212“), when the two were last together (the newest
  shared photo or message in a shared chat or thread), what they share
  in one sentence, and one button to the edge page. Each row of a list
  is one short line (the relation, then at most three reasons) and a
  name always opens that person's page; on another's list a small „mit
  Ben ›“ after it opens what connects the two. The picture scales to
  what it holds (rings as wide as their people need, the frame cut to
  them, labels placed where they meet nothing, at most 40 in it). „Ausblenden“ and „Nicht
  berechnen“ (out before any tie is computed) are folded at the end of a
  person's page, big and apart, each
  with a Rückgängig toast (owner 2026-11-11: „gefährlich platziert, dicke
  Finger“). The person herself is never on her list nor asked about but
  as „du“, nor are her other selves: a card with her name (umlauts
  alike), her number or her address (`ich_auch` in `netz.json`), and an
  Immich person named as she is; in another's list she shows as „Du“.
  Closed, the locked page.

**Step 2 (this):** „Wie steht X zu Y?“, one tap into the address books.

- **The questions** (`fragen.go`; owner 2026-11-11, „Das sind verdammt
  viele Fragen“): only what points to family is asked. Photo days, group
  chats and mail threads together are enough for „kennen sich“: a tie on
  „Steht nahe“ with its reasons, never a question and never written to
  an address book — so a Freund/in, Kollege/Kollegin or Nachbar/in
  between two people is not asked. Asked are, in this order: pairs with
  the person („du“); the active groups (step 5); the derived relations
  (step 6); the family circle
  (the person, her relatives — answered or a phone book's relation, from
  relative to relative — and everyone with the family name of one of
  them, each pair, tie or not); then two others of the same family name
  (umlauts alike), or whose shared address suggests family, with a tie.
  A tie needs two photo days, something a phone book says, or two
  conversations; weaker is never asked. Within each group a derived one
  first, then the strongest tie. A kind is suggested: a relation word a
  name begins with toward the person („Oma Hilde“, „Mama“,
  „Schwiegermutter Hedwig“; `beziehungswoerter.yml`, step 5), such a
  contact asked even without a tie; what the box's model read in their
  messages (step 5); the same
  family name and address with the age gap (Geschwister, Partner/in,
  Eltern/Kind, Großeltern/Enkel, else verwandt); a group named Familie
  (sonstige verwandt); the same address and under 12 years apart
  Partner/in; the same firm Kollege/Kollegin; a group named Arbeit,
  Nachbarschaft or Freundeskreis; the same street, another house
  Nachbar/in; five photo days or more without
  a sign of family Freund/in. The cards give N, BDAY and ADR for it,
  Immich the persons' `birthDate`. The kinds: Partner/in, Ex-Partner/in,
  Eltern, Kind, Geschwister, Großeltern, Enkel/in, Onkel/Tante,
  Neffe/Nichte, Cousin/e, Schwiegereltern, Schwiegerkind,
  Schwager/Schwägerin, Freund/in, Kollege/Kollegin, Nachbar/in,
  Bekannte/r, sonstige verwandt, and „Kennen sich nicht“ (the tie leaves
  the net); „Keine Beziehung“ is never asked again, „Später“ comes last.
- **A portion per visit**: the tab shows at most ten („Fragen 10“, on
  the card „noch 10 Fragen“), less what was answered since the
  visit began; never the backlog. A visit begins when the person comes
  to the page from outside (a link, a bookmark: the browser's
  `Sec-Fetch-Site` is not `same-origin`) and with the first page after
  the vault opened; the archive's own links, answers and live updates
  stay in it. „Später“ puts a question behind the portion. Done: „Alles
  nachgebessert für jetzt.“ and a quiet „weitere Fragen“ (`?mehr=1`, one
  at a time, „Genug für jetzt“ to stop).
- On the main page one at a time: both faces (initials), „X ist für Y
  …“, four kinds as chips (the suggestion marked), „kennen sich nicht“ („kenne ich nicht“ when it asks about the person),
  „mehr …“ for the rest, why it is suggested, and where the answer goes
  („kommt ins Adressbuch der Familie“, „kommt nur auf dein Handy“, or
  „bleibt in deinem Archiv“ with the reason: a person only on photos
  cannot be written).
- **The answers** are kept only in the vault,
  `/run/tresor/umfeld/antworten.json` (0600): a kind becomes the pair's
  relation in the net in place of the phone books' („Kind (von dir
  angegeben)“), and what Homes wrote of it (`eingetragen`: where, the line,
  when). „Deine Antworten (N)“ (`/tresor/umfeld`) is folded by default
  (owner 2026-11-11; `<details>`, no script), the newest first when open,
  each with what became of it and „Vergessen“ (the same as Rückgängig).
  Answers Homes did not take are one line outside the fold, only when
  there are any: „2 Antworten noch nicht eingetragen · erneut versuchen“,
  which retries all of them.
- **One tap** (`fragen_seite.go`, `uebernahme.go`; owner 2026-11-10:
  the tap in the person's own open archive is their consent): a chip is a
  form post — no script needed; the page's script sends it in the
  background and swaps the page in place. It saves the answer in the
  vault and has Homes write it at once, server to server; the page comes
  back with the next question and a toast „✓ … eingetragen ·
  Rückgängig“ for ten seconds. Rückgängig (and Vergessen later) has Homes
  take the written line back, then forgets the answer; if Homes does not
  answer, both stay and the page says so. The archive writes no address
  book itself. Where it goes: both ends in the household book → both
  household cards, each naming the other by UID; otherwise → only the
  person's own phone, on each end that is a phone contact, by name;
  neither → it stays in the vault. A household card never gets a name
  from the phone or the photos; the packet carries a household UID only
  when both ends have one.
- **The packet** (version 2, the contract with Homes'
  `internal/uebernahme`): `POST <ARCHIV_HOMES_INTERN>/intern/uebernahme`,
  form `d`, `s`; `d = base64url(JSON {"person", "am", "nonce", "weg"?,
  "zeilen": [{"x": {"name", "haushalt"?, "handy"?}, "y": {…}, "art"}]})`,
  `s = base64url(HMAC-SHA256(ARCHIV_HOMES_SECRET, "archiv-uebernahme-2." +
  d))`, both without padding; `x` is `art` of `y`, `weg` takes the lines
  back. Homes checks `s` with the secret of the person `d` names, takes
  `am` for two minutes and each nonce once, and refuses anything that
  came through the public route. It goes from the archive's container to
  Homes' own port on the box, never through a browser.

**Step 3:** group chats and mail threads as sources (`gespraeche.go`).

- **What is read**, while the vault is open and msgvault answers: one
  read-only statement through msgvault's `POST /api/v1/query` (its
  analytics views: `conversations`, `conversation_participants`,
  `messages`, `message_recipients`, `participants`). A group chat is a
  WhatsApp, Signal or MMS conversation of 3 to 30 members, a mail thread
  one of 2 to 10 addresses among From, To and Cc. A thread with a
  `List-Id` or a bulk sender (`noreply`, `newsletter`, `info`, …) is left
  out. msgvault still building its analytics (202, 503) or closed: the
  round keeps the conversations read last.
- **Who**: a participant is a contact by their number (last nine digits)
  or address (case aside) on a card of the phone books; one matching no
  card, or cards of differently named contacts (a shared landline), is
  left out, never made a person. `netz.json` keeps per conversation only
  the matched cards' keys, its size, message count, last time and a
  chat's title (for the group suggestions of step 5); no number, no
  address, no subject.
- **Weights**: each conversation counts for each pair in it 2 (chat) or
  0.5 (mail) over size−1, halved every 4 years since its last message; a
  pair gets at most 2 from chats and mail together (below a phone book's
  relation, 3), added to strength and rank. The reasons say „in 3
  Gruppen-Chats“, „12 Mails zusammen“ (the messages of the threads both
  are in).
- **What makes a tie** (`naehe.go`, owner 2026-11-11: fewer, truer
  statements): a phone book or the person says so, or at least two
  occasions (photo days, group chats, mail threads) whose weights without
  the decay add up to 0.5, each 1 (photo day), 2 (chat) or 0.5 (thread)
  over size−1. Two photo days among five, two chats of nine or two
  threads of three are a tie; two weddings, two class chats of thirty or
  two mails with eight in copy are not: the pair's page still counts
  them. A thread only one of its people wrote in (the statement's
  `sender`) is no conversation.
- **The chat of two**: the statement reads the person's chats with one
  other too (size 2, kind `direkt` in `netz.json`): a tie from 10
  messages on when both wrote, up to 3 added to strength and rank (10
  messages 1, 100 2, 1000 3, halved every 4 years), the reason „840
  Nachrichten zu zweit“. Only counts and the last time are kept.
- **How close**: points per channel — photo days (2 days 1, 12 days 2),
  the chat of two (10 messages 1, 300 2), shared chats and threads (1),
  a phone book or the person (partner, parents, children, siblings,
  grandparents 3; another relation or a card's name 1) and one address
  (2). 5 points are „sehr nah“, 3 „nah“, less „bekannt“; the list is in these
  steps, „Bekannt“ folded, within a step by rank.
- **„Lange nichts gehört“**: up to three of the person's ties that are
  nah or sehr nah, not of her address, whose last photo day or message
  in a conversation of at most six is two years or more ago, each with
  when and what there was. Shown only once a chat of two was read
  (without them the archive cannot tell silence). „Nicht mehr erinnern“
  on the person's page keeps it in the vault (`umfeld/ruhen.json`).
- **Verstorben** (owner 2026-11-11): „Ist verstorben“ on a person's page
  marks her, „Markierung entfernen“ takes it back (`umfeld/verstorben.json`).
  She stays in the net, the list and the Stammbaum with „†“ after her
  name; the archive asks nothing about her any more (no question, no
  relation hint, no place in the tree, no birthday, town or employer) and
  „Lange nichts gehört“ never names her.
- `archiv umfeld-gespraeche -sql` prints the statement as msgvault's
  request body; `archiv umfeld-gespraeche < antwort.json` reads an answer
  as a round does (`integration.sh` runs both against the real msgvault).

**Step 5:** relation hints: words, groups, the box's model (owner
2026-11-11: „Kombination aus Schlagwort suche und LLM … Vorher sollten
aktive Gruppen vom Benutzer kategorisiert werden“).

- **The words** are one file, `beziehungswoerter.yml` (built into the
  image; `woerter.go`): kinship and affection words in German, English,
  Spanish, French, Turkish, Polish, Russian (also transliterated) and
  Arabic (transliterated and a few in its script), each with the kinds it
  hints at, the suggestion first; the words of a group title with the
  category they suggest; and the words that are also names (`Anne`,
  `Hala`), which never make a suggestion from a contact's name. Words
  compare as `klar` does and with accents dropped: „Schwägerin“ is
  „SCHWAEGERIN“ and „Schwagerin“, „kardeşim“ is „kardesim“; never inside
  another word („Tomate“, „Europa“). `go test` checks the file.
- **Names and titles (no model):** a contact whose name begins with such a
  word is asked toward the person with that kind („Mama“ → Mutter/Vater,
  „„Mama“ im Namen“); a group's title suggests its category („Familie
  Dopp“ → Familie, „Eltern 3b“ → Elterngruppe, „Oma & Opa“ → Familie).
- **Groups first** (`gruppen.go`): an active group chat (3 to 30 members,
  a title, two members the phone books know, a message in the last 12
  months) is a question after the person's own pairs: „Wie würdest du die
  Gruppe „…“ nennen?“ with its members' initials (no message) and the
  chips Familie, Freundeskreis, Verein, Arbeit, Klasse, Elterngruppe,
  Nachbarschaft, Reisegruppe, Politik, Sonstiges, the title's suggestion
  marked, and Später. One tap keeps it in the vault
  (`/run/tresor/umfeld/gruppen.json`, 0600), never in an address book;
  the toast has Rückgängig, „Deine Gruppen“ (`/tresor/umfeld`) Vergessen.
  What it does: Familie (the person's) puts the members into the family
  circle, so pairs among them are asked, suggested „sonstige verwandt“;
  Arbeit gives „Kollege/Kollegin (Gruppe „…“)“, Elterngruppe „Eltern aus
  „…““, Klasse „aus der Klasse „…““, Verein, Freundeskreis, Nachbarschaft,
  Reisegruppe and Politik their own reason on „Steht nahe“ (at most two a
  tie); Arbeit, Nachbarschaft and Freundeskreis also a suggestion. A
  title's category counts for reasons and suggestions until the person
  answers; only the person's own Familie widens the circle.
- **The model** (`hinweise.go`), only while the vault is open and the box
  has one (`MODEL_URL`, laib's model role): each round asks msgvault (one
  read-only statement, `messages.snippet` filtered by the words, the
  newest 500, no list mail) and keeps the messages the file's words are
  really in, whose sender and at least one recipient the phone books know
  (in a chat the person is a recipient of what she did not send). A round
  sends every such message to the model (archiv#42; before, 10 an hour),
  one after the other, at most 3 per pair, and makes the suggestions anew
  every 10, so the page shows them while it reads:
  `POST $MODEL_URL/chat/completions` (`X-Laib-Background`, the model key,
  `response_format: json_object`, temperature 0) with who wrote it to whom
  as letters (A the sender; the person marked), what is known of them (the
  person's answers, the phone books' relations, shared groups and their
  categories, family names), the words found and the text (at most 600
  characters). The answer must be one JSON object
  `{"beziehungen":[{"x","y","art","sicher"}]}` and nothing else; a
  relation needs two known letters, a kind of the questions and a
  sureness from 0 to 1. Readings of 0.7 or more add up per pair and
  kind; the kind with the most, alone, is the pair's suggestion, the cue
  its reason („Vorschlag, weil Kim dir „Hallo Mama, kommst du“
  schreibt.“), and a family kind makes the pair a question even without a
  tie. Nothing is set without the person's tap.
- **Kept**: per message msgvault's id, when, the sender's and recipients'
  nodes, the cue (a few words around the word) and the readings, in
  `/run/tresor/umfeld/hinweise.json` (0600): no message is sent twice.
  Before each message the round asks the lease (`MODEL_LEASE`, `GET`): any
  window standing, or the proxy refusing (409, 429, 503), or no answer,
  and the rest waits for a later round; nothing piles up. The closing ends
  it (the round never holds the vault open). Nothing of a
  text or an answer is logged (counts only); the text leaves the vault
  only in the request to the box's own model.
- `archiv umfeld-hinweise -sql` prints the statement as msgvault's request
  body (`-wort w`: with one word instead, for the fixtures; `-geburtstag`:
  the birthday wishes', step 8);
  `archiv umfeld-hinweise < antwort.json` reads an answer as a round does,
  one line a message without its text (`integration.sh` runs both against
  the real msgvault).

**Step 6:** derived relations (`abgeleitet.go`).

- From what is known — answers and the phone books' relations — follow
  likely relations: partner of a parent → Eltern; a shared parent →
  Geschwister; parent of a parent → Großeltern; sibling of a parent →
  Onkel/Tante; child of an uncle or aunt → Cousin/e; parent of a partner
  → Schwiegereltern; sibling of a partner or partner of a sibling →
  Schwager/Schwägerin.
- **By definition, with the answer** (owner 2026-11-11): what an answer
  makes follow by definition (Großeltern, Onkel/Tante, Cousin/e,
  Schwiegereltern, Schwager/Schwägerin, and what follows from those) is
  written in the same tap, each line by the same address-book rules,
  kept in the vault as from that answer (`aus`, `weil`). The toast shows
  the chain („✓ Hedwig ist Mutter/Vater von Uwe · daraus: weil …, ist
  Hedwig Schwiegereltern von Silke“); one Rückgängig takes the answer and
  its chain back, from Homes and the vault. A step-family (partner of a
  parent) and half-siblings (a shared parent) stay questions, the kind
  chosen and the chain as the reason, as does anything the phone books
  alone derive.
- An answer is never written over: a derivation that says otherwise, or
  two that disagree, is a question („…; du hattest „Freund/in“
  angegeben“), asked once — the answer to it keeps what the derivation
  said (`trotz`). A pair with a phone book's relation is never asked;
  „Später“ comes last.
- Names compare with umlauts spelled out (`klar`): „Jürgen Groß“ is
  „Juergen Gross“, case and accents aside.

**Step 4:** the edge page, „Was verbindet die beiden?“ (`kante.go`,
`kante_seite.go`; owner 2026-11-11: „von einer Kante schnell auf die
Nachrichten und Bilder kommen … und diese Kante schnell nachbessern“).

- `/tresor/kante?a=…&b=…`, one page per pair, one tap from a „Steht
  nahe“ row (its short line and bar; the face and name still open the
  person), a person's page (its button, and each row of its list), a
  question card („Warum?“), every line of the Stammbaum and „Ansehen“
  under Erkannt (`&aus=erkannt`). Under the relation „Fertig“ leads on:
  back to Erkannt when it came from there, else to the questions.
- On top both faces and the relation as the question card's chips, the
  current kind pressed (an answer, one that followed from an answer, or a
  phone book's), „keine Beziehung“ and „kennen sich nicht“ beside them. A
  pair that is an open question has its suggestion marked and its reason
  said, so one tap confirms. A tap sets the kind whether or not the pair
  is asked (`Festlegen`): what Homes wrote of the pair (and of what
  followed from it) is taken back first, the new kind is kept and written
  with what follows from it by definition, and the vault keeps what it
  replaced (`vorher` in `antworten.json`), so the toast's Rückgängig
  brings that back, into Homes too. The same kind again writes nothing.
- **Gemeinsam** (owner 2026-11-11: „unendliche Ketten von gleich
  klingenden Meldungen … weniger mit mehr“): one sentence that counts
  what the two share, each kind once: „37 gemeinsame Fotos (2014–2025,
  zuletzt im Mai) · 3 Gruppen · 12 Mail-Verläufe · 1.204 Nachrichten“
  (the messages are everybody's in the shared chats and threads). Under
  it the most telling of each kind, the rest folded („alle zeigen“):
  - three thumbnails, the newest photo of the last three shared days;
    folded, every shared day (30 days, 4 photos a day), lazy 88 px. A
    thumbnail comes through the archive (`/tresor/kante/bild?id=`, only
    for a photo of the net's table), read from Immich
    (`GET /api/assets/{id}/thumbnail?size=thumbnail`, `asset.view`) with
    the person's key from the vault for that one answer, never kept on
    the box (the browser may cache it privately for an hour). A tap opens
    the photo in Immich (`IMMICH_PUBLIC_URL/photos/<id>`); without that
    address the thumbnail is all.
  - the first cue the model read in a message between the two („Hallo
    Mama“, a tap opens the message in msgvault's view, `/messages/<id>`),
    the three newest group chats with their category and last line, and
    the mail threads as one row with the newest's last line; folded, the
    other cues and groups and every thread. The last lines are read live
    from msgvault (one read-only statement over `messages`, newest per
    conversation, ids digits only; nothing kept). msgvault closed or
    busy: the conversations without their last line, into msgvault's
    search. `archiv umfeld-kante -sql <id…>` prints the statement,
    `archiv umfeld-kante < antwort.json` reads an answer without its text
    (`integration.sh` runs both against the real msgvault).
  - „Außerdem“: what no count says, in one line (the same address or
    firm, a phone book's word, a social export, the same family name);
    the relation set above is not said again.
- **„+ Beziehung“** (owner 2026-11-11, „Sabine fehlt als Partnerin“): on
  the edge page, a person's detail and under the tree; a name (the
  browser suggests the net's names, the archive compares umlauts and case
  alike, the person's own other cards are her) leads to the edge page of
  the two, where one tap sets the kind; several of that name are offered.

**Step 7:** the Stammbaum (`stammbaum.go`), the workplace's third tab.

- The family circle's Eltern/Kind, Partner/Ex-Partner and Geschwister
  lines: answered, followed from an answer or a phone book's (solid),
  derived and not confirmed (dashed). Generations are rows, partners side
  by side (one with several in the middle), a child under its parents,
  parents moved over their children; no birthday is needed.
- One server-side SVG, no library, no script needed: it scrolls sideways
  inside its box, never the page (the page's script only scrolls „Du“
  into view). Boxes are 96×48 px links to the person's detail; every line
  is a link to its edge page with a 22 px invisible hit stroke. The lines
  are also a folded list.
- Messy data: the generations follow the confirmed lines first (a 0-1
  breadth-first walk where a derived line costs one); a line that does
  not fit — a cycle, someone two generations at once — is not drawn but
  listed under „Passt nicht in den Stammbaum“, its edge page saying why
  (`&pruefen=1`). A step-family gets a line from each parent.
- Relatives a step away (owner 2026-11-11: „Wenn Hedwig meine Tante
  ist, wird das nicht im Stammbaum angezeigt.“; `stammbaum_weitere.go`):
  Großeltern, Onkel/Tante, Cousin/e, Neffe/Nichte, Enkel/in,
  Schwiegereltern, Schwiegerkind, Schwager/Schwägerin and Sonstige
  verwandt, answered or in a phone book, are in the tree at their
  generation. Where the drawn lines give the path (an aunt is a sibling
  of a parent), she stands there by that line; where not, on a side row
  right of her row („Tanten und Onkel“), and „Noch ohne Platz im
  Stammbaum“ asks once, one tap: „Hedwig ist Schwester oder Bruder von …“
  with the ones in between (the parents) and „weiß nicht“. A tap sets
  that pair as the edge page does (Homes writes it, Rückgängig undoes
  it), and the tree derives the rest; „weiß nicht“ keeps her on the side
  row, not asked again (`baum-weissnicht.json`). Without anyone in
  between it says what is missing; Sonstige verwandt opens the edge page
  to say how. Nothing is dropped.
- Asked early (owner 2026-11-11): two of the family circle with one
  family name on at least five photo days together are asked with the
  person's own questions; with both birthdays the age gap suggests the
  kind (a small gap between adults Partner/in), without them none.

**Step 8 (archiv#42):** what the archive recognises by itself, always a
suggestion until the person taps (owner 2026-11-11).

- **Name patterns** (`namensmuster.go`, `beziehungswoerter.yml`
  `namensmuster`; no model): a card saved as „Mama von Lena“, „Papa vom
  Tom“, „Eltern von Lea“ is a parent of the one named after it, not of
  the person; „Freundin von …“, „Kollegin von …“, „Ex-Freund von …“ give
  Freund/in, Kollege/Kollegin, Ex-Partner/in. Any word of `beziehung` or
  of `namensmuster.worte` right before a `von` word counts („Oma von …“
  Großeltern), seen from the one named; names after it split at „und“
  („Mama von Lena und Tom“), marks around it are fine („Sabine (Mama von
  Lena)“). „<Vorname> <Nachname> / <Nachname>“: what follows the slash is
  another family name of the same one, never part of her name („Vater von
  Wilma Gnielczyk-Jazdzejewski / Jazdzejewski“ names Wilma
  Gnielczyk-Jazdzejewski; after a given name alone, „Lena/Tom“, it is an
  „und“), and a word already in the name is not added again. The one named is the person of that name (umlauts alike; a
  single given name the one person whose first name it is, none if
  several), else a person of its own (`name:<name>`, often a child with
  no card; its id is the name as the card wrote it, so answers about her
  stay hers). It gives a tie with „„Mama von Lena“ im Telefonbuch“ as its
  reason and a question with that kind as the suggestion, asked with the
  derived ones, family kind or not, tie or not. „Peter via Lena“ (`ueber`
  words: via, über, durch) is only a tie „kennt über“, never a relation
  nor a question of its own. Such a name never suggests a kind toward the
  person nor a family name, and its words never send a message to the model.
- **Groups by the model** (`gruppenmodell.go`): each round, before the
  messages, every active group (step 5) the person has not named and the
  model has not read goes to it, one after the other, the lease asked
  before each: its title, the members the phone books know (the person
  as herself), its 5 newest texts (200 characters each, one read-only
  statement, `archiv umfeld-gruppen -sql <id…>`). The answer must be
  `{"kategorie","sicher"}`, a category of the chips; a reading of 0.7 or
  more is the group's suggestion, before its title's, said as „Vorschlag
  des Modells deiner Box“, and counts like a title's for reasons until
  the person answers. Kept per group in `gruppen-modell.json`; no group
  is sent twice.
- **Groups by hand**: the workplace's fourth tab „Gruppen“ lists every
  group chat with a title and two known members, the active ones first
  (30, the rest on „auch … ältere Gruppen zeigen“), each folded: its
  category or the suggestion, the chips (the current one pressed), the
  person's own words as chips, and a field „Eigener Name“ (40 characters)
  in a form of its own. A tap sets the category at any time, asked or
  not; an own word is a reason like „Skat (Gruppe „Skatrunde“)“, and
  suggests no kind. The toast's Rückgängig gives back what it replaced.
- **A person by hand** (`angelegt.go`, owner 2026-11-11): a name at
  „Zu wem?“ (or the tree's) that matches nobody offers „Wilma anlegen“
  with „Wilma ist für dich …“ and the kinds as chips; one tap makes her —
  only in the vault (`umfeld/angelegt.json`), never a card in an address
  book — and the relation, back to the page „+ Beziehung“ was on with
  „✓ Wilma angelegt, Kind von dir.“ and Rückgängig, which takes back
  both. Her line goes to Homes as any answer does (by name, on the phone
  card of the other end). Her page renames her (Homes' lines go again
  under the new name) or deletes her with her relations (two taps). When
  a phone-book person of the same name (umlauts alike) appears, Erkannt
  and her page ask once „Ist Wilma aus deinem Telefonbuch dieselbe
  Person?“: Ja moves her relations to that person (an answer that one
  already has stays), Nein keeps both and is not asked again. Never
  merged without that yes.
- **What was recognised**: under Fragen, „7 Vorschläge ansehen“ (a link
  to `/tresor/erkannt`), „5 Gruppen einordnen“ (only the unsorted ones,
  a link to the Gruppen tab) and, quietly, „120 von 480 Nachrichten
  gelesen“; each only when there is something. `/tresor/erkannt`:
  the relations a card's name or the model suggests (pairs not answered
  and not in a phone book; „Stimmt“ sets it and comes back to the list
  with the toast and its Rückgängig; „Ansehen“ opens the edge page). A
  card's name that made a person of its own is „Lena anlegen?“ („Mama:
  „Mama von Lena“ aus deinem Telefonbuch.“): one tap „Anlegen“ makes her
  and the relation, and the toast says „Lena angelegt, Mama: „Mama von
  Lena““ (owner 2026-11-11: „Das Verhältnis sollte aber direkt da sein“);
  the same yes on a question card says the same and goes on with the next
  question. The groups a title
  or the model suggests („Stimmt“, or „Anders nennen“ in the tab), the
  birthdays. At most 20 a section.
- **Birthdays from wishes** (`geburtstage.go`; no model): each round
  reads (the step-5 statement with the wishes as its filter, `archiv
  umfeld-hinweise -sql -geburtstag`) the newest 500 messages wishing a
  happy birthday in the family's languages, never a belated one. A wish
  to one known person alone (a direct chat, a mail to one) is a sign for
  the day it was sent (house time); a person's day with the most signs
  (a sender counts once a year), at least two, from two years or three
  senders, and more than any other day, is the suggestion: „Ben Berg: 2.
  Juli? · 2 Glückwünsche an diesem Tag, 2023 bis 2025“, never for one
  whose birthday a card or Immich has. „Stimmt“ / „Stimmt nicht“ keep the
  answer in `geburtstage.json` (not suggested again); confirmed ones are
  a folded list with Vergessen. They stay in the vault: the Homes packet
  carries relations only, a birthday line needs its next version.
- **Couples from „wir“ messages** (archiv#44, `paare.go`; no model):
  each round reads (the step-5 statement with `paare` of
  `beziehungswoerter.yml` as its filter, `archiv umfeld-hinweise -sql
  -paare`) the newest 500 messages saying „mein Mann Tom“ / „meine Frau
  Lena“ (a mein word, a partner word, a name: one message is enough) or
  ending signed by two in a message with a wir word („Wir kommen gern!
  Anna und Ben“, one of them the writer's first name: from two such
  messages on). The name must be one known person (by first name; among
  several, the one the writer has a tie or a family name with). It is a
  relation suggestion (Partner/in) under „Erkannt“: „Stimmt“ as every
  other one, „Stimmt nicht“ keeps it in `paare-weg.json`, never
  suggested again. What a round found is in `paare.json`.
- **Home town and employer from mail** (archiv#44, `herkunft.go`): each
  round reads (`archiv umfeld-hinweise -sql -herkunft`) the newest 400
  mails from senders (no list, not the person's own), two per address;
  for a sender a card knows whose cards lack an address or an ORG, at
  most two mails each, the body (`GET /api/v1/messages/{id}`) is cut to
  its own end (HTML made text, a quoted mail and `>` lines left out, 700
  characters) and goes, with the address's domain when it is not a free
  mailer's, to the box's own model under the same rules as step 5 (only
  while open, the lease before each mail, a refusal stops the round, no
  text or answer in the log). It answers town and employer with
  sureness; per person and field the readings from 0.7 summed by value,
  the one with the most, alone, is the suggestion: „Kim Berg wohnt in
  Ulm? · Vorschlag aus 2 Mails, die letzte vom 01.10.2026“, never for a
  field a card has. Read mails are kept in `herkunft.json` (never sent
  twice), „Stimmt“ / „Stimmt nicht“ in `herkunft-antworten.json`;
  confirmed ones are a folded list with Vergessen and stay in the vault
  (no Homes packet line yet).

## Status for laib

`/data/laib-status.json` (outside the vault, nothing secret): `at`,
`detail`, `findings[{severity, detail, fix}]`, plus `person`, `zustand`,
`bereit`, `wartend.<quelle>.{wartend, aelteste_seit}` (the inbox),
`briefkasten.{wartend, aeltestes}` (packets waiting for the opening),
`letztes_paket.<quelle>.{am, sicherung, paket, nachrichten, anhaenge}`,
`letzter_import.<quelle>.{am, datei, zahlen{sms,mms,anrufe,anhaenge,mails}, anzahl}`,
`letzter_fehler{am, quelle, datei, satz}`, `packt`, `liefert`, `umfeld_am`
(when the net was last computed: a time, nothing of the net)
(`zahlen.signal`, `zahlen.whatsapp`: new messages of a direct import).
Findings (gelb): packets waiting more than three days while the archive is
not open („Dein Archiv ist seit … zu, X Pakete mit neuen Nachrichten
warten.“), Signal backups waiting more than three days while it is closed
(„Dein Archiv ist zu, X Signal-Sicherungen warten seit …“), a backup
waiting three days for an archive not set up, an error with its fix,
missing keys while backups wait, msgvault not starting. The record lives
beside it in `/data/archiv-stand.json`.

## The Datenpfleger (archiv#58)

While the vault is open, the archive finds what keeps the person's data
from being clean and complete (`pfleger.go`, `pfleger_seite.go`; owner
2026-11-12), with the access it already has and rules only, no model.
The start says „N Dinge zum Aufräumen ›“; `/tresor/pflege` shows one
finding per screen with the step in the app that owns the data, „Passt
so“ and „Nächstes“. „Passt so“ is kept in the vault
(`pflege-passt.json`): that finding never returns; an aggregate keeps
each of its parts, so only a new part brings it back. A finding fixed in
its app is gone at the next Abgleich or Umfeld round.

| Area | Check | Step shown |
|---|---|---|
| Kontakte | a card without a name, or with only a number as its name | name it (or delete it) on the phone |
| Kontakte | a number of 6–8 digits that starts with neither 0 nor + | add the area code on the phone |
| Kontakte | a number twice on a card, in any spelling (all such cards one finding) | the admin runs `homes nummern-bereinigen` (homes#359) |
| Kontakte | a birthday the person confirmed from the wishes that no card and no Immich person has | add it on the phone's card, or in Immich for a person without a card |
| Kalender | the same title at the same start under two UIDs in one calendar | delete one in the calendar it came from |
| Fotos | photos with people but no date (the net skips them) | edit the date in Immich (thumbnails link there) |
| Fotos | photos with people and a date but no place | add the place in Immich |

There is no „Beheben“ yet: the archive writes no book (Radicale is read
only), and its one way into Homes (`/intern/uebernahme`) takes relations
only, so every step is in the owning app. Left out: **Dokumente** (the
archive has no Paperless access; adding one is a new key and flow, the
owner's card first), duplicate cards (Google's job, owner 2026-11-11),
unnamed faces (Homes' „Wer ist das?“), subscribed calendars that stopped
(Radicale does not say which calendar is a subscription). Nothing of a
finding leaves the vault; Homes gets their number (`aufraeumen`, below).

## Counts for Homes

What waits for the person reaches Homes as counts only (owner
2026-11-12, homes#362), for „Deine Aufgaben“ and Start, shown to the
archive's owner alone: `GET /intern/aufgaben` with `X-Archiv-Homes`
answers `{offen, am, fragen, aufraeumen, schluessel_fehlt}` (`aufgaben.go`).
`fragen` are the open questions and suggestions of the workplace
(„Wie steht … zu …?“, „Kennen sich …?“, a group's name, what was
recognised), `aufraeumen` what the Datenpfleger found (below),
`schluessel_fehlt` a key the archive waits for (Signal, WhatsApp, Immich;
the page is `/tresor/quellen`). They are counted only while the vault is
open; the last count stays outside it in `/data/aufgaben.json` with its
time `am`, and `offen: false` says it is not of now. No name, no kind,
nothing of a question; not in `laib-status.json` (the admin's). laib
`docs/DATA.md` describes the flow.


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
| env | `ARCHIV_PERSON=<login>` (required), `PROXY_SECRET=!secret …` (the route's proxySecret, sent as `X-Archiv-Proxy`), `ARCHIV_HOMES_SECRET=!secret …` (shared with Homes, for `/intern/…`), `TZ`, optional `PUBLIC_URL` (used in the status' fix sentences), `LEERLAUF` (s, default 7200) |
| mounts | `${DATA_DIR}/archiv/<person>:/data:z` (the cipher folder, the Briefkasten, the keys, the status file; backed up), `${DATA_DIR}/archiv-eingang/<person>:/eingang:z` (read, write **and delete**: the archive removes packed backups and writes `letzte/`) |
| backup: false | `${DATA_DIR}/archiv-eingang/<person>/letzte` **must not** be in restic (`backup: false`): it is itself a backup of the phone, and a 2.7 GB Signal file that changes every night would add 2.7 GB to restic every night. (The rest of the inbox is transient and need not be backed up either.) |
| tmpfs | none needed: plaintext is only ever written inside the vault (its work folder). |
| disk | reading a Signal backup needs its decrypted size free on `/data` for a few minutes; Signal backups wait in the inbox while the vault is closed, one per night. |
| Homes' way in | `/intern/…` is on the same port 8080; Homes must reach it (host side, as Gesundheit's MCP port) with `X-Archiv-Homes: <ARCHIV_HOMES_SECRET>`. Until the box runs Homes (laib#1021 step 9) the legacy names `ARCHIV_HAUS_SECRET` and `X-Archiv-Haus` are accepted too (`intern.go`). |
| FUSE | `AddDevice=/dev/fuse`, `AddCapability=SYS_ADMIN` (mount in the container's own user and mount namespace). On the runner Docker also needed `--security-opt apparmor=unconfined`; on Fedora CoreOS SELinux may need its own label option — unproven. |
| user | the supervisor runs as the container's root (it mounts, and msgvault's key file must belong to the process user). Rootless podman maps that root to the service user; no `UserNS=keep-id`. The inbox folder is shared with Homes: the archiv container must be able to delete what Homes wrote there (write permission on the folders Homes makes, which it makes 0775). |
| port | the container listens on `8080` (pages, msgvault proxy); publish `127.0.0.1:<port>:8080`, `lan: false`. msgvault's `8090` stays inside. |
| health | `http://127.0.0.1:<port>/healthz` (answers in every state) |
| status | `status: file: ${DATA_DIR}/archiv/<person>/laib-status.json` |
| stop | `TimeoutStopSec` ≥ 90: closing stops msgvault (≤ 30 s) and unmounts |
| Radicale (contacts, calendars) | `RADICALE_URL=http://host.containers.internal:5232` (Radicale's root, no path), `RADICALE_USER=archiv-<person>`, and that account's password as a file: `Secret=<the instance's ARCHIV_RADICALE_PASSWORD>,type=mount,target=radicale-password` (read from `RADICALE_PASSWORD_FILE`, default `/run/secrets/radicale-password`; never an environment variable). Without `RADICALE_URL` and `RADICALE_USER` the page says the box has not set it up. Optional: `KONTAKTE_TAKT` (s, default 10800), `KONTAKTE_RELAIS_PORT` (default 18443, inside the container only). |
| Immich (Umfeld) | `IMMICH_URL=http://host.containers.internal:2283` (Immich's root; `uses: [immich]`). The key is the person's own, entered on the page, never the template's. Optional: `UMFELD_TAKT` (s, default 3600); `IMMICH_PUBLIC_URL` (Immich as the browser reaches it, `https://fotos.<domain>`), where the edge page's photos open. |
| the box's model (Umfeld, step 5) | `MODEL_URL`, `MODEL_LEASE`, `MODEL_KEY` (laib's builtins as a pod reaches them: `http://host.containers.internal:11435/v1`, `…/api/model-lease`, the model key) and `MODEL_NAME` (the household preset, used when the lease names none). Without `MODEL_URL` no message goes anywhere; names, titles and groups still give their hints. |
| The household's document mailbox (optional) | `DOKUMENTE_MAIL_HOST`, `DOKUMENTE_MAIL_USER`, `DOKUMENTE_MAIL_DOMAIN` (of `<login>+doc@<domain>`), optional `DOKUMENTE_MAIL_PORT` (993) and `DOKUMENTE_MAIL_ORDNER` (INBOX), and the password as a file: `Secret=<the instance's secret>,type=mount,target=dokumente-mail-password` (`DOKUMENTE_MAIL_PASSWORD_FILE`, default `/run/secrets/dokumente-mail-password`; never an environment variable). Without host, user or domain, or without the file, there is no document mailbox. |
| Homes (answers) | `ARCHIV_HOMES_INTERN=http://host.containers.internal:8650` (Homes' own port on the box, where an answer is written; laib's template sets it). The packet is signed with `ARCHIV_HOMES_SECRET`; without either, answers stay in the vault and the card says so. |
| Radicale account and rights | an LLDAP account `archiv-<person>` per instance (seeded like `kalender`, its password the instance's generated secret), and in `templates/radicale/pre-start`'s rights, **right after `[root]`** (before every `user: .+`/`(.+)` section): `[archiv-home]` user `archiv-(.+)`, collection `{0}`, permissions `R`; `[archiv-lesen]` user `archiv-(.+)`, collection `{0}/(handy-kontakte\|google-(?!kontakte(/\|$))[^/]+\|privat-[^/]+)(/.*)?`, permissions `r`; `[archiv-sonst-nichts]` user `archiv-.+`, collection `.*`, permissions empty. `integration.sh` runs these rights in a real Radicale. For Umfeld (archiv#27) the account also reads `homes/haushalt-kontakte` (read only; the laib template change and its ADR 0029 amendment bring that right); until then the archive gets 403 there and the page says the household book is missing. |
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
  `letzte/` and given back to Homes only while open; a non-backup in
  `signal/` fails with the sentence, stays, and leaves nothing in the
  vault's work folder or the Briefkasten; the Signal passphrase inside the
  vault (0600) and nowhere in `/data` while closed, and one left in the old
  outside store moved inside at the next opening.
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
- **Contacts and calendars, proven in CI** against a real Radicale 3 (from
  PyPI, htpasswd instead of LLDAP) with the rights above: `archiv-anna`
  sees anna's `handy-kontakte`, `google-arbeit` and `privat-sport`, not
  another of her calendars nor bob's, and every write is 403; the real
  msgvault connects through the relay (`add-carddav`, `set-role`,
  `sync-carddav`), the two cards become two persons, the book is subscribed
  and a lookup source but not the write target; the relay refuses PUT,
  DELETE and PROPPATCH; a card deleted in Radicale leaves its person in
  msgvault with its number and is marked on the page and in the store,
  and back in Radicale the mark goes, still one person (msgvault's profile
  does not show the CATEGORIES mark: 0.22.0 projects only FN, EMAIL, TEL); the calendars are counted on the page, an event
  deleted at the source stays, marked; two persons merged through the
  page's preview; Radicale's files unchanged by the archive. **Unproven:**
  the relay on the box (rootless podman: the container's private address,
  pasta or a bridge), a real phone's address book of hundreds of cards, and
  whether msgvault makes identity suggestions from them (CI's two cards
  produce none, so Ja/Nein are proven against a fake of msgvault's API
  only, with its routes and error codes from 0.22.0's OpenAPI).
- **Unproven**: a real phone's Signal backup through `import-signal` (CI's
  backup is generated and tiny; the box's first one is the proof, with its
  time, the RAM signalbackup-tools needs and the disk the decrypted folder
  takes); SMS and calls from the Homes app (built, not yet on a
  phone); WhatsApp on a real backup (the decryption is proven on a
  generated one in `integration.sh`, msgvault's import of a real msgstore
  is not); how much RAM signalbackup-tools needs for a larger backup (it
  holds the backup's database in memory); WhatsApp (needs the 64-digit key;
  a passkey cannot be read out).
- **What import-signal leaves out** (msgvault's own list): SMS, MMS and
  calls Signal kept as the phone's SMS app (the phone's SMS come through
  the Homes app instead), group and safety-number updates, timer changes,
  payments and other events; stories and story replies; messages deleted
  for everyone; scheduled messages not yet sent; earlier versions of an
  edited message (the newest text stays, marked as edited); legacy groups
  never migrated to new groups, and MMS groups. Signal Android database
  version 185 and newer only.
- **Signal twice, for now**: messages that came in by the old way (XML
  packets, before 2026-11-02) are in msgvault's SMS account; the first
  `import-signal` puts the same history in the Signal account again. They
  stay untouched until a separate cleanup with the owner.
