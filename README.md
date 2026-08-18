# westward

Sunset alert system: watches Brooklyn's sky, scores sunset color, and yells
when it's worth going outside.

One Go binary, one SQLite file, one frames volume. Public page is a bare grid
of daily best frames. Admin backend manages cameras, thresholds, and
notifiers. Behind a Cloudflare tunnel (public hostname for `/`, Access-gated
route for `/admin`).

## Status

Phase 0 complete. Building per implementation plan (kept out of the repo).

### Operator sign-off notes (Phase 0)

- **DOT posture — deviation from plan §2.1, approved by operator 2026-08-18.**
  Plan shipped default was `publish_eligible=false` for NYCTMC cameras.
  Operator holds a signed data-sharing agreement (greenlight from DOT), so
  the shipped default is `publish_eligible=true` for NYCTMC. The admin
  confirmation warning remains. Operator's own camera (on order) takes over
  as `publish_primary` when it arrives; DOT cams are temporary for testing.
- **Provider fixtures** are real captures under `testdata/providers/`
  (2026-08-18/19, Brooklyn). SunsetHue API key is env-only
  (`WESTWARD_SUNSETHUE_KEY`), never committed.

## Setup

TBD — full setup docs land with the operational release phase.

## License

MIT — see [LICENSE](LICENSE).
