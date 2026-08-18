# westward

Sunset alert system: watches Brooklyn's sky, scores sunset color, and yells
when it's worth going outside.

One Go binary, one SQLite file, one volume. The public page is a bare grid
of daily best frames. The admin backend manages cameras, thresholds, and
notifiers. Designed to sit behind a Cloudflare tunnel.

- **Public gallery** `/` — every day's best frame (or an honest dark cell
  when nothing was captured), newest first, keyboard lightbox.
- **Admin** `/admin` — cameras with ROI and publish-crop editors, alerts,
  forecast comparison, runtime settings, JSON status.
- **Alerts** — heads-up ~35 min before sunset when forecast quality clears
  the floor; one GO per day with the triggering frame when live color rises.
- **Forecasts** — Open-Meteo heuristic (`openmeteo-h1`) and SunsetHue,
  stored append-only with raw responses for comparison.
- **Archive** — best frame per day forever (with uncropped originals for
  re-rendering); full frame sequences 5 days.

## Quick start (binary)

```sh
WESTWARD_ADMIN_PASSWORD='choose-12+chars' \
WESTWARD_DB_PATH=/var/lib/westward/westward.db \
WESTWARD_DATA_ROOT=/var/lib/westward \
WESTWARD_LISTEN=127.0.0.1:8080 \
./westward serve
```

Optional `--config /path/config.yaml` (see `config.example.yaml`) for
notifier definitions and location overrides. Runtime settings are edited in
`/admin/settings`; changes apply without restart.

## Docker

```sh
export WESTWARD_ADMIN_PASSWORD='choose-12+chars'
docker compose up -d --build
# loopback only: 127.0.0.1:8080
```

The container is distroless, runs as UID 65532, and names its volume
`westward_data` (auto-owned; no manual chown needed).

## Cloudflare tunnel

The service binds loopback; expose it with cloudflared:

```sh
cloudflared tunnel create westward
cloudflared tunnel route dns westward sunsets.example.com
cloudflared tunnel run --url http://127.0.0.1:8080 westward
```

Recommended ingress:

| Hostname / path | Service | Access |
| --- | --- | --- |
| `sunsets.example.com` (public) | `http://localhost:8080` | none — public grid |
| `sunsets.example.com/admin*` | `http://localhost:8080` | **Cloudflare Access** (email OTP) |

Basic Auth always protects `/admin`; Access is defense-in-depth, never a
dependency.

## Cameras

**NYCTMC (NYC DOT)** — add from admin with the DOT camera id (uuid from
`webcams.nyctmc.org/api/cameras`). Politeness is built in: 15 s floor,
UA `westward-sunset/1.0 (personal project)`, 403/429 backoff, two 404s
marks the camera stale (retry from admin).

The shipped default publishes DOT frames (`publish_eligible=on`) under the
operator's signed data-sharing agreement. If you do **not** have the
agreement, turn this off — the admin checkbox confirms this explicitly.

**Generic HTTP JPEG** — any still-JPEG URL (Amcrest window cams:
`http://cam/cgi-bin/snapshot.cgi`, credentials via
`WESTWARD_CAM_WINDOW=user:password`, never in the URL). For the public
gallery prefer your own sky-oriented camera: the published image is the
full frame (or your publish crop), so frame it at sky.

### Scoring ROI vs publish crop

Two independent rectangles per camera, edited by dragging on the preview
(plain drag = scoring ROI, shift-drag = publish crop; "crop ← ROI" copies).

- **Scoring ROI** — region the sunset-color score reads (default: top 45%).
- **Publish crop** — region cropped for the public grid. Keep the aspect
  constant across edits for a tidy grid. Null publishes the full frame.

Changing the crop affects future days. "re-render" on the dashboard
recrops a past day from its kept original (new URL, old file deleted —
immutable caching stays correct).

## Backup / restore

Nightly `VACUUM INTO` snapshot at 02:30 to `/data/backups`, 7 kept.
Durable set = the latest snapshot **plus** `/data/best/` (images + sidecars).

```sh
westward backup --db /data/westward.db --out /data/backups
westward restore --from /data/backups/westward-<ts>.db --db /data/westward.db
```

`/data/best/*.json` sidecars can rebuild `days` rows even without the DB.

## Operations

- `GET /livez` — engine heartbeat (503 if the loop stalled)
- `GET /readyz` — DB + data dir + scheduler
- `GET /admin/status` (auth) — JSON: revision, cameras, delivery queue
- `westward score --url https://…/image.jpg` — score any frame now
- `westward healthcheck` — container healthcheck (`/readyz` probe)

## Notifiers

ntfy (images attached, text fallback), Pushover (multipart with size-ladder
re-encode), webhook (HMAC-SHA256 signed, dedupe on `event_id`). Enable in
`config.yaml`; tokens come from env, never the config file. Add channels:
one file in `internal/notify/`, one factory case in `internal/engine/alerts.go`.

Exactly one GO event fires per day (atomic latch); a provider may still
show a duplicate after an ambiguous network failure — no HTTP integration
can prevent that.

## Development

```sh
make check   # vet, staticcheck, tests (offline; no network needed)
```

Provider contracts are frozen as fixtures under `testdata/providers/`
(captured 2026-08-18). Frame fixtures are generated by
`testdata/frames/gen/main.go`. Scoring changes bump `score.ScoringVersion`.

## License

MIT — see [LICENSE](LICENSE).
