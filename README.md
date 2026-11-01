# archiv

Das Nachrichten-Archiv je Person — for the household on a Laib box
([laib ADR 0029](https://github.com/mdopp/laib/blob/main/docs/adr/0029-one-message-archive-per-person.md),
laib#985, laib#986). One container per person: a small Go supervisor keeps
the person's [msgvault](https://github.com/wesm/msgvault) archive in a
gocryptfs vault that only their password opens, imports the Signal backups
Haus brings from the phone, and closes the vault after two idle hours.
Made in the Werkstatt (`laib-app new archiv`).

- `apps/archiv/` — the program, its tests, its `Containerfile` and
  `integration.sh`; its [README](apps/archiv/README.md) says what it does,
  what is proven and what is not, and **what the Laib template must give**.
- `templates/` — empty for now: the template waits for laib#986 (a template
  installed once per person, one route for all instances).
- CI (`.github/workflows/apps.yml`): `go vet` + `go test`, then the image
  is built and `integration.sh` runs it with a real FUSE mount, real
  signalbackup-tools and real msgvault; on main the image is published as
  `ghcr.io/mdopp/archiv:latest` and `:<sha>`, only after both are green.

## The template's contract, in short

| | |
|---|---|
| env | `ARCHIV_PERSON` (the person's login, required), `PROXY_SECRET` (the route's proxySecret; Caddy sends it as `X-Archiv-Proxy`), `TZ`; optional `PUBLIC_URL`, `LEERLAUF` (seconds, default 7200) |
| mounts | `/data` ← `${DATA_DIR}/archiv/<person>` (the cipher folder `tresor/`, `laib-status.json`, `archiv-stand.json`); `/eingang` ← `${DATA_DIR}/archiv-eingang/<person>` (read and delete) |
| FUSE | `AddDevice=/dev/fuse`, `AddCapability=SYS_ADMIN`; the supervisor runs as the container's root |
| port | `8080` in the container (publish on `127.0.0.1` only, `lan: false`) |
| health | `GET /healthz` → `ok`, in every state |
| status | `/data/laib-status.json` |
| route | browser: Authelia, then the person's instance with the proxy secret and `Remote-User`; agents: `/p/<person>/` stripped, past Authelia, `Authorization: Bearer <msgvault key>` |

Details and the open points (rootless podman with FUSE on Fedora CoreOS is
unproven; what only the first real Signal backup can tell) are in
[apps/archiv/README.md](apps/archiv/README.md).
