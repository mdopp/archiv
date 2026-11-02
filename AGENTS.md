# Working in this repo

Read the Werkstatt guide before you change anything here: `/opt/werkstatt/guide/AGENTS.md`,
then `/opt/werkstatt/guide/apps.md` (it is also Claude Code's managed CLAUDE.md in the Werkstatt).
Outside the Werkstatt, the same guide is `werkstatt/guide/` in github.com/mdopp/laib.

- A new app: `laib-app new <name>`; never copy an app folder by hand.
- Before a push: the app's tests and `laib template lint templates/<name>`.
- Conventional Commits, scope = the app: `feat(<name>): …`.
- No secrets in the tree: a template declares them, the box generates or stores them.

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
- The packet format (`paket.go`, README "Paket format") is a contract with
  the phone: a change readers must understand is a new version.
- `go test` cannot mount FUSE or run signalbackup-tools; `integration.sh`
  does, in CI, against the built image. Run it after any change to the
  vault, the import or the Containerfile.
