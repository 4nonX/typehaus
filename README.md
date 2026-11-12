# typehaus

**Self-hosted font management for teams.** A central font library with collections and group permissions, plus a small sync agent that activates fonts on Windows, macOS and Linux **without admin rights**. A free, open-source alternative to Extensis Universal Type Server, FontAgent Server or FontExplorer X Server – in one Docker container.

![Library](docs/library.png)

## Why

Teams share fonts through network drives, USB sticks and "can you send me that font?" messages. Nobody knows who has which licensed font installed, and every new laptop needs an admin to install them. Commercial font servers solve this, but they are expensive and closed.

typehaus does the core of it, free:

- **One library.** Upload TTF, OTF, TTC or whole ZIP archives. Metadata (family, style, weight, designer, foundry, embedding rights) is read straight from the font. Duplicates are detected by content hash.
- **Collections & groups.** Decide who sees what. "Brand fonts" for everyone, the licensed display family for the design group only.
- **Activate from the browser.** Toggle a family or a single style. Every computer you enrolled installs it within a minute and removes it when you turn it off.
- **No admin rights.** Fonts are installed per user: HKCU registration on Windows, `~/Library/Fonts` on macOS, fontconfig on Linux.
- **Revocation that works.** Remove a user from a group, disable them, or revoke a device, and the agent removes the fonts it installed.
- **License tracking.** Record purchased licenses and seat counts, assign them to fonts, and see actual usage per license and per family: users with the font active, devices with it installed.
- **Activity log.** Uploads, activations, enrollments, sign-ins and admin changes.
- **Single sign-on.** Optional OIDC (Authentik, Keycloak, Authelia, Zitadel, Entra ID, Google…) with group sync and admin mapping. Local accounts work out of the box.
- **Small and boring to run.** A single Go binary with an embedded web UI. SQLite plus a content-addressed file store in one `/data` volume. Distroless image, no external services.

## Quick start

```yaml
# docker-compose.yml
services:
  typehaus:
    image: ghcr.io/4nonx/typehaus:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    volumes: ["typehaus-data:/data"]
    environment:
      TYPEHAUS_BASE_URL: "https://fonts.example.com"
      TYPEHAUS_ADMIN_PASSWORD: "change-me-please"   # first start only
volumes:
  typehaus-data:
```

```sh
docker compose up -d
```

Open the UI and sign in as `admin`. If you didn't set `TYPEHAUS_ADMIN_PASSWORD`, a random password is printed in the container log on first start (`docker logs typehaus`).

Then:

1. **Library** → drop your fonts in.
2. **People** → create groups and users, or connect SSO.
3. **Collections** → create a collection, choose who can see it, and add fonts to it from the Library (select → *Add to collection*).
4. **Devices** → each user downloads the agent and enrolls their computer with a one-time code.

Put typehaus behind your reverse proxy (Caddy, Traefik, nginx, Pangolin…) with HTTPS. Set `TYPEHAUS_TRUST_PROXY=true` so client IPs and the scheme are taken from `X-Forwarded-*` headers.

## The agent

The agent is a single static binary per platform. The Docker image ships all of them, so users download them from **your** server (Devices page). This also works in air-gapped networks. They are also attached to every [GitHub release](https://github.com/4nonX/typehaus/releases).

```sh
typehaus-agent enroll --server https://fonts.example.com --code ABCD-EFGH-JKLM
typehaus-agent autostart enable
```

That's it. The Devices page generates these commands for you.

| Command | What it does |
|---|---|
| `enroll --server URL --code CODE [--name NAME] [--ca-file FILE]` | Link this computer to your account (code from the web UI, valid 15 min, single use). `--ca-file` trusts an internal/self-signed CA. |
| `autostart enable` / `disable` | Copy the agent to a per-user location and start it at login: Run key on Windows, LaunchAgent on macOS, systemd user service or XDG autostart on Linux. |
| `run` | Sync continuously in the foreground (this is what autostart launches). |
| `sync [--force]` | Sync once. `--force` re-verifies every installed file. |
| `status` | Show the server, user, last sync and installed fonts. |
| `unenroll` | Remove all fonts typehaus installed, disable autostart, forget the enrollment. |

**How fonts are activated (all per user, no elevation):**

| OS | Files | Activation |
|---|---|---|
| Windows 10 1809+ / 11 | `%LOCALAPPDATA%\typehaus\fonts` | Registered under `HKCU\…\Windows NT\CurrentVersion\Fonts`, loaded into the session with `AddFontResourceW`, `WM_FONTCHANGE` broadcast |
| macOS | `~/Library/Fonts/typehaus-*` | Picked up automatically by the system font service |
| Linux / BSD | `~/.local/share/fonts/typehaus/` | `fc-cache` refresh |

Behaviour worth knowing:

- **Offline:** if the server is unreachable, fonts stay active and the agent retries with backoff.
- **Revoked device or disabled user:** the agent removes everything it installed, deletes its enrollment and exits.
- **Fonts in use:** a font that can't be removed because an application has it open (Windows) is retried on every sync.
- **Integrity:** every download is verified against its SHA-256. Missing or damaged files are reinstalled automatically.
- Only files the agent installed itself are ever touched. Your own fonts are left alone.
- Windows ships two binaries: `typehaus-agent-*.exe` (console, for the commands above) and `typehaus-agentw-*.exe` (no console window, used for autostart). Keep them in the same folder when you run `autostart enable`.
- The binaries aren't code-signed yet. On macOS, remove the quarantine flag (`xattr -d com.apple.quarantine typehaus-agent-darwin-*`). On Windows, SmartScreen may ask once.

## Configuration

All settings are environment variables.

| Variable | Default | Description |
|---|---|---|
| `TYPEHAUS_BASE_URL` | – | Public URL, e.g. `https://fonts.example.com`. Required for OIDC; used in enrollment commands. |
| `TYPEHAUS_LISTEN` | `:8080` | Listen address |
| `TYPEHAUS_DATA` | `/data` (image) | Database and font storage |
| `TYPEHAUS_ADMIN_USER` | `admin` | Initial admin username (first start only) |
| `TYPEHAUS_ADMIN_PASSWORD` | random | Initial admin password (first start only; logged if generated) |
| `TYPEHAUS_TRUST_PROXY` | `false` | Trust `X-Forwarded-For` / `X-Forwarded-Proto` |
| `TYPEHAUS_SECURE_COOKIES` | auto | `true` when `BASE_URL` is https |
| `TYPEHAUS_SESSION_HOURS` | `168` | Web session lifetime |
| `TYPEHAUS_POLL_SECONDS` | `60` | How often agents check for changes (min. 10) |
| `TYPEHAUS_MAX_UPLOAD_MB` | `512` | Max size of one upload request |
| `TYPEHAUS_LOCAL_LOGIN` | `true` | Set `false` to allow SSO only |
| `TYPEHAUS_AGENTS_DIR` | `/app/agents` (image) | Agent binaries offered on the Devices page |

### Single sign-on (OIDC)

| Variable | Description |
|---|---|
| `TYPEHAUS_OIDC_ISSUER` | Issuer URL (discovery via `/.well-known/openid-configuration`) |
| `TYPEHAUS_OIDC_CLIENT_ID` / `TYPEHAUS_OIDC_CLIENT_SECRET` | Client credentials |
| `TYPEHAUS_OIDC_NAME` | Button label, e.g. `Authentik` |
| `TYPEHAUS_OIDC_GROUPS_CLAIM` | Claim with group names (e.g. `groups`). Users are placed into typehaus groups **with the same name** at every login. |
| `TYPEHAUS_OIDC_ADMIN_GROUP` | Members of this IdP group become typehaus admins (requires the groups claim) |
| `TYPEHAUS_OIDC_AUTO_CREATE` | `true`: create accounts on first login. `false`: only pre-created users may sign in. |

Redirect URI: `https://fonts.example.com/auth/oidc/callback`. The login uses the authorization code flow with PKCE and a nonce. An existing local user with the same username is linked on their first SSO login.

**Active Directory / LDAP:** put an IdP that speaks OIDC in front of your directory (Authentik, Keycloak, Authelia and Zitadel all federate LDAP/AD) and use the variables above.

## Backups

Everything lives in `/data`: `typehaus.db` (SQLite, WAL mode) and `fonts/` (content-addressed files). Stop the container, or use `sqlite3 typehaus.db ".backup backup.db"`, and copy the directory.

## Security notes

- Passwords are hashed with bcrypt. Failed logins and enrollment attempts trigger an increasing lockout per client.
- Web sessions use HttpOnly, SameSite cookies. State-changing API calls require a custom header (CSRF protection). Strict CSP.
- Device tokens and enrollment codes are stored only as SHA-256 hashes. Enrollment codes are single use and expire after 15 minutes.
- Agents can only download fonts that are currently active **and** visible to their user.
- Fonts are validated as TrueType/OpenType before they are stored. WOFF/WOFF2 web fonts are rejected because desktops can't install them.

Found a vulnerability? Please open a private security advisory on GitHub rather than a public issue.

## Compared to commercial font servers

typehaus covers the core workflow: central library, permissions, per-user activation, revocation and license visibility. Not included (yet):

- Auto-activation plugins for Adobe InDesign / Illustrator / Photoshop, Affinity, QuarkXPress (activating missing fonts when a document opens)
- InDesign Server / prepress integrations
- Font conflict and corruption reports, version history of a font
- Native tray/menu-bar app (today the agent is a background service; activation happens in the web UI)

Contributions are very welcome, especially app plugins and a tray UI.

## Development

```sh
go test ./...
TYPEHAUS_DATA=./data TYPEHAUS_ADMIN_PASSWORD=adminadmin1 go run ./cmd/typehaus-server
# in another terminal
TYPEHAUS_AGENT_HOME=/tmp/th-agent go run ./cmd/typehaus-agent enroll --server http://localhost:8080 --code …
```

Layout:

```
cmd/typehaus-server     server entrypoint
cmd/typehaus-agent      agent CLI
internal/server         HTTP API, auth, storage, embedded web UI (internal/server/web)
internal/agent          sync engine + per-OS activation and autostart
internal/fontmeta       dependency-free OpenType name/OS2 table reader
internal/protocol       agent ⇄ server wire format
```

The web UI is plain HTML/CSS/JS with no build step.

## License

[GNU AGPL-3.0](LICENSE). You can use, self-host and modify typehaus freely, including inside companies. If you offer a modified version to others over a network, you must publish your changes.
