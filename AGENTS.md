# Working in this repo

Read the Werkstatt guide before you change anything here: `/opt/werkstatt/guide/AGENTS.md`,
then `/opt/werkstatt/guide/apps.md` (it is also Claude Code's managed CLAUDE.md in the Werkstatt).
Outside the Werkstatt, the same guide is `werkstatt/guide/` in github.com/mdopp/laib.

- A new app: `laib-app new <name>`; never copy an app folder by hand.
- Before a push: the app's tests and `laib template lint templates/<name>`.
- Conventional Commits, scope = the app: `feat(<name>): …`.
- No secrets in the tree: a template declares them, the box generates or stores them.
- The gates (laib#1170), in CI; run them before a PR. `scripts/loc.sh`: production lines
  under the one cap in `scripts/loc.txt` (crossing it: raise it there in the same PR, the
  reason beside it, the owner's yes). `scripts/ratchet.py`: deadcode, staticcheck, dupl and
  gocognit (over 30), pinned, against today's findings in `scripts/ratchet/`. A new or higher
  finding fails: fix it, or `scripts/ratchet.py --update --accept <tool>` with the reason in
  the PR and the owner's yes; a finding that went away fails until the same PR lowers the
  baseline (`scripts/ratchet.py --update`, commit `scripts/ratchet/`).
- `ratchet.py`, `test_ratchet.py`, `pins.sh`, `copies.sh` and `loc.sh` are copies of laib's
  `werkstatt/scaffold/repo/scripts/`: `scripts/copies.sh` fails CI when one differs from
  laib's main. A change goes to laib first, then here as a copy.

## archiv

- The template waits for laib#986 (per-person templates); `apps/archiv/README.md`
  lists what it must give. Do not write one before that design lands.
- A secret (the Archiv-Passwort, a messenger key) never goes into argv, an
  environment variable or a log: stdin or a pseudo-terminal only. Every
  program call is built in `befehl.go`, where `befehl_test.go` checks it.
- The import is additive: nothing here may call a msgvault command that
  deletes. Plaintext of a backup is never a file outside the vault: only
  in the vault's work folder while it is open, emptied on every path (so
  Signal backups, which msgvault's import-signal reads as a decrypted
  folder, are read only while open). The Briefkasten never touches the vault.
- Radicale is only read (owner, 2026-11-02): the archive's own client sends
  PROPFIND/REPORT/GET. msgvault never reads Radicale: the relay
  (`relais.go`) serves it the archive's contact store (`kontakte.go`) and
  refuses every write. Never give the book `--write-target`, never publish.
  A contact deleted on the phone is never removed (owner): it stays in the
  store, marked, and keeps being served, so its person stays in msgvault
  (the mark is on the archive's page; 0.22.0 does not show a card's
  CATEGORIES on the person). Both stores in the vault (contacts,
  calendars) only grow.
- The packet format (`paket.go`, README "Paket format") is a contract with
  the phone: a change readers must understand is a new version.
- `go test` cannot mount FUSE or run signalbackup-tools; `integration.sh`
  does, in CI, against the built image. Run it after any change to the
  vault, the import or the Containerfile.
