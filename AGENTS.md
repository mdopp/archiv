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
  deletes, and the plaintext export is deleted on every path.
- `go test` cannot mount FUSE or run signalbackup-tools; `integration.sh`
  does, in CI, against the built image. Run it after any change to the
  vault, the import or the Containerfile.
