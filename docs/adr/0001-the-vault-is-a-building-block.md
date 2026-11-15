# ADR 0001 — The vault is a building block; the archive's features are programs that work in an open vault

Applies when you add a feature to the archive, when a program wants to
read or write a person's private data, or when you touch unlocking,
sessions or the vault's storage.

- **Status:** Accepted, 2026-11-15 (the owner: „finde deinen Vorschlag gut … setze es so um“)
- **Deciders:** the owner (mdopp), 2026-11-15: „Das Archiv müssen wir
  ähnlich betrachten. Da passiert jetzt auch schon zu viel, das nichts
  miteinander zu tun hat. Es müsste eher eine DB sein, die wir entsperren,
  und andere können dann arbeiten.“
- **Relates to:** laib ADR 0029 (one archive per person), 0032 (agents
  stay out of the family's data), 0035 (three layers).

## Context

The archive is about 25,600 lines in one program: the vault (encryption,
passkey, keys, sessions, protocol), imports and reconciliation (msgvault,
mail, WhatsApp/Signal, Facebook/LinkedIn, phone book, calendar, Immich),
and analyses (Umfeld, Nähe, the Stammbaum, relations, questions, the local
model's hints, the Datenpfleger). The security-critical part sits among
the domain logic, and every feature can reach every kind of data.

## Decision

1. **The vault is a building block (laib layer 1):** one encrypted store
   per person and nothing else. It unlocks with the person's password or
   passkey, opens a session that expires, and serves an API with rights
   **per area**: messages, contacts, relations, photo assignment, notes.
   Read and write per area. Every access is in the vault's
   protocol. Locked, nobody can read it, not Laib, not an admin, not an
   agent.
2. **The features are programs (laib layer 2):** imports, relations and
   the Stammbaum, the Datenpfleger, face matching, later the diary
   (#66). Each runs only while the person has the vault open, with only
   the areas it needs.
3. **Grant per session:** opening the vault, the person grants each
   program its areas for this session („Stammbaum darf Beziehungen lesen
   und schreiben“). The program gets a short-lived key bound to those
   areas; locking the vault, or the session's end, voids it at once.
4. **What leaves the vault** is what the person taps, as today, and the
   questions the programs derive (the Datenpfleger's gaps, the face
   matching's „Wer ist das?“). Tasks are not an area of the vault: a
   program computes them while the vault is open and hands them, with
   their text, to Homes' „Deine Aufgaben“ through the plugin contract
   (homes ADR 0003). The owner chose this on 2026-11-15, knowing that
   the text names people and so lies outside the vault; docs/DATA.md in
   laib records that flow when it is built.
5. **Order:** first the vault's API with areas, sessions and protocol
   inside today's program (archiv#68); then one feature after another moves
   out, the Stammbaum first (the largest, the least sensitive).

## Consequences

- The vault's code is small enough to review on its own.
- A fault in one feature reaches only its areas, and only while the vault
  is open.
- New features need no change to the vault.
- Moving a feature out is a PR with tests per feature; the person's data
  does not move.
