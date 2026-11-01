# archiv

The household's own small programs, each with the Laib template that runs
it on the box. Made in the Werkstatt (`laib-app new <name>`).

- `apps/<name>/` — the program, its tests and its `Containerfile`.
- `templates/<name>/` — the Laib template: `laib.yml`, the Quadlet unit,
  `values.example.yml`.
- CI tests every app, publishes `ghcr.io/mdopp/<name>` from main, and
  lints every template.

The box knows this repo as the template registry `apps` (box.yml
`registries`); `laib template sync` pulls it, `laib propose --add <name>`
installs an app as a proposal the owner approves.
