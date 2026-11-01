# archiv

Für die Familie in einem Satz: Dein Nachrichten-Archiv — Signal (später
WhatsApp, E-Mail und Discord) durchsuchbar auf der Box, in einem Tresor, den
nur dein Archiv-Passwort öffnet.

One container per person ([laib ADR 0029](https://github.com/mdopp/laib/blob/main/docs/adr/0029-one-message-archive-per-person.md),
laib#985, laib#986). Inside: a small Go supervisor (`archiv`, this folder),
[msgvault](https://github.com/wesm/msgvault) 0.22.0, gocryptfs (Debian
package), [signalbackup-tools](https://github.com/bepaald/signalbackup-tools)
20261014 and wa-crypt-tools 0.1.0 (for WhatsApp, not used yet).

- Code: `*.go`, tests: `go test ./...`; `integration.sh <image>` runs the
  built image with a real FUSE mount (CI does, on every push and PR).
- Image: `ghcr.io/mdopp/archiv:latest` and `:<sha>`, built by CI on main.
- Laib template: not yet — it waits for laib#986's per-person design. What
  it must give is below.

## The vault (Tresor)

The whole archive — msgvault's database, its API key, the person's keys —
lives in a gocryptfs cipher folder, `/data/tresor`, and is mounted at
`/run/tresor` only while open.

| state | what is there |
|---|---|
| uneingerichtet | no `/data/tresor/gocryptfs.conf`. The page asks for the Archiv-Passwort twice, says losing it loses the archive (so: Vaultwarden) and that an open archive is readable by the box's admin. `gocryptfs -init`, then open. |
| verschlossen | ciphertext only. msgvault is stopped. One password field. |
| offen | mounted; `msgvault serve` runs with `MSGVAULT_HOME=/run/tresor/msgvault`, loopback `127.0.0.1:8090`, its key in `api-key` (generated on the first opening), telemetry off (config and env). The import runs. |

Closing: „Abschließen“, two hours without use (`LEERLAUF`), or the container
stopping (SIGTERM): the import stops and deletes its plaintext, msgvault
stops, `fusermount3 -u`. A running import holds the idle close off. What
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
password) are files in `/run/tresor/schluessel/`, accepted only while open;
the page shows only „hinterlegt am …“.

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

## The import (Signal)

Haus puts the backups into `/eingang/<quelle>/…` (haus#207); `.teil/` and
every dot-entry are ignored. While open, every 20 s: each
`signal/**/signal-*.backup` whose size and mtime stayed the same for a
minute (as this process saw it — Haus sets the phone's mtime), **oldest
first** (by mtime), once both the Signal passphrase and the person's number
are stored:

1. `signalbackup-tools <backup> --exportxml /run/tresor/arbeit/<rand>.xml --setselfid <number> --no-showprogress`, passphrase by pty;
2. `msgvault import-synctech-sms <xml> --owner-phone <number>` (through the running daemon);
3. the XML is deleted — always, also on failure or when the vault closes
   meanwhile (and anything left in `arbeit/` by a crash is deleted at the
   next opening); the backup is deleted from the inbox only after a
   successful import.

**Additive only.** msgvault upserts every message by a stable id; its
`import-synctech-sms` has no flag that deletes or mirrors deletions (only
`--sms/--mms/--calls/--attachments`), and the importer runs nothing else
(`TestImportRunsNothingThatDeletes`). CI imports two XMLs into the real
msgvault, the second lacking a message, then the first again: still 2
messages, no duplicates.

A wrong passphrase („…IT IS LIKELY AN INCORRECT PASSPHRASE…“ in
signalbackup-tools' output) becomes „Die Signal-Passphrase passt nicht zu
dieser Sicherung. Gib sie neu ein.“; no other backup is tried with that key
until a new one is entered. Any other failure keeps the backup and says so;
it is tried again at the next opening.

**WhatsApp and Discord** are counted, not imported. WhatsApp's database
needs the 64-digit key (`wadecrypt`, then `msgvault import-whatsapp`) — a
next step. Discord's data package has no importer in msgvault. IMAP
credentials are stored, mail is not fetched yet (`msgvault add-imap` takes
the password on stdin; a next step).

## Status for laib

`/data/laib-status.json` (outside the vault, nothing secret): `at`,
`detail`, `findings[{severity, detail, fix}]`, plus `person`, `zustand`,
`bereit`, `wartend.<quelle>.{wartend, aelteste_seit}`,
`letzter_import.<quelle>.{am, datei, zahlen{sms,mms,anrufe,anhaenge}, anzahl}`,
`letzter_fehler{am, quelle, datei, satz}`, `laeuft`. Findings (gelb): a
Signal backup waiting more than three days while the archive is not open
(„Dein Archiv ist seit … zu, X Sicherungen warten.“), an import error with
its fix, missing keys while backups wait, msgvault not starting. The record
of imports lives beside it in `/data/archiv-stand.json`.

## What the template must give (laib#986)

| what | value |
|---|---|
| image | `ghcr.io/mdopp/archiv:<sha>` (a database inside: pin, no `AutoUpdate`) |
| env | `ARCHIV_PERSON=<login>` (required), `PROXY_SECRET=!secret …` (the route's proxySecret, sent as `X-Archiv-Proxy`), `TZ`, optional `PUBLIC_URL` (used in the status' fix sentences), `LEERLAUF` (s, default 7200) |
| mounts | `${DATA_DIR}/archiv/<person>:/data:z` (the cipher folder and the status file; backed up — ciphertext), `${DATA_DIR}/archiv-eingang/<person>:/eingang:z` (read **and delete**: the supervisor removes imported backups) |
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
  inside the gocryptfs mount; the supervisor's whole cycle; signalbackup-tools
  runs and gets its passphrase through the pty (on a non-backup it fails
  and the failure is reported).
- **Unproven**: rootless podman on the box (Fedora CoreOS, SELinux) mounting
  FUSE with these flags; signalbackup-tools on a real backup.
- **Only the first real Signal backup can tell**: whether this
  signalbackup-tools reads the phone's backup format (Signal changed its
  backup format in 2025; the XML export is, in its author's words, not well
  tested), how many messages the XML carries — **messages of contacts
  without a shared phone number are skipped** by the XML export (Signal has
  made the number optional), quotes, reactions and edits are lost (SMS XML
  cannot hold them), groups with the same members merge — and how long a
  multi-GB backup takes and how much room its XML needs inside the vault
  (about 1.3 × the backup).
