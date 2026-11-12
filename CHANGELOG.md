# Changelog

All notable changes to typehaus are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).
The section matching a release tag becomes that release's notes on GitHub.

## [Unreleased]

## [0.2.0] - 2026-11-12

### Added
- **Tray / menu-bar app.** The agent now lives in the Windows notification area, the macOS menu bar, or a Linux StatusNotifier panel (KDE, Xfce, Cinnamon, GNOME with the AppIndicator extension). The menu shows the sync status and offers *Open typehaus*, *Open font library*, *Sync now*, *Start at login* and *Quit*.
- **Status icon.** The new `t.` mark doubles as a status light: a solid square when up to date, a blinking square while syncing, an outlined square when the server is unreachable, an amber diamond when something needs attention, and a dimmed mark when not connected. Icons are drawn pixel-snapped for every tray size (16–32 px at 100–200 % scaling). Windows switches between light and dark taskbar variants automatically; macOS uses a template image that adapts to the menu bar.
- **Agent window.** A local page, reachable only from this computer and protected by a token, for connecting to a server with an enrollment code (no terminal needed), showing status, installed fonts (previewed in their own typeface) and recent activity, and changing settings or disconnecting.
- Double-clicking the agent starts the tray. On first launch the window opens to connect. Launching it again while it runs reopens the window instead of starting a second copy.
- Without a tray host (e.g. stock GNOME), the agent keeps syncing in the background, and launching it again opens the window.
- macOS: `typehaus.app` (universal, menu-bar only, no Dock icon) in releases and in the server's download list.
- Windows: the executable has the typehaus icon, version information, and a per-monitor-v2 DPI manifest for a sharp tray icon on high-DPI displays.
- New typehaus mark across the web UI favicon, the agent and the app icons, generated from a single renderer (`internal/brand`).
- Devices page: the main flow is now *download, open, enter code*, with the server address and code ready to copy. The command-line route moved into a collapsible section.
- `CHANGELOG.md`; release notes are taken from it.
- **Deployment files:** `docker-compose.yaml` with all settings in `.env` (`.env.example`), ready for Dockge. Install guide in `docs/install.md`: Dockge, reverse proxies (Caddy, Nginx Proxy Manager, Traefik, Pangolin), backup, updates, troubleshooting.
- **Update notice:** apps show *Update available* when the server runs a newer release. A newly downloaded version replaces the running one and takes over *Start at login*.
- **Duplicate font handling:** files with the same PostScript name (e.g. two versions) are marked in the library and on upload. Only one of them can be active per user, and activating one switches the other off.
- **Private certificates:** the app window accepts a custom CA certificate when connecting.
- `typehaus-server reset-password NAME` for recovering admin access (works via `docker exec`).

### Changed
- `autostart enable` registers the tray app (or headless `run` mode where no tray is available). Login launches don't open the window.
- Connecting from the agent window turns on *Start at login* automatically.
- **Disabling a user suspends their computers instead of disconnecting them.** Fonts are removed, the connection is kept, and fonts return when the account is enabled again.
- SSO group sync only manages memberships it created; groups assigned by hand are kept.
- The library renders large collections page by page while scrolling.
- `docker-compose.yml` was replaced by `docker-compose.yaml` + `.env`.

### Fixed
- Linux tray icons no longer show coloured fringes. This works around a pixel-conversion bug in the systray library.
- Windows: a font switched back on while its removal was pending (file in use) stayed inactive.
- macOS: *Start at login* broke when the app was opened from outside Applications (App Translocation); the app now explains how to fix it.
- Linux: the tray icon was missing when typehaus started at login before the panel was ready.
- The server refuses an SSO-only setup without an admin group, which would have locked everyone out of administration.
- The app window now remembers the last server address.
- `typehaus-agent sync`, `enroll` and `unenroll` no longer conflict with a running app: they hand off to it or ask to use its window.

## [0.1.0] - 2026-11-12

### Added
- Self-hosted font server in a single Docker container: font library with TTF/OTF/TTC/ZIP upload, metadata read from the font's name and OS/2 tables, and content-addressed storage with duplicate detection.
- Collections with access for everyone or for selected groups; users and groups; per-user activation of families or single styles.
- Sync agent for Windows, macOS and Linux that installs fonts per user without admin rights, verifies checksums, repairs missing files, and removes fonts when they are deactivated or access is revoked.
- One-time enrollment codes for devices; device list with last-seen time and revocation.
- License records with seat counts and usage by license and by family.
- Activity log; local accounts with rate-limited login; optional OIDC single sign-on with group sync and admin mapping.
- Agent binaries for all platforms bundled in the image and offered on the Devices page.
- CI and release workflows publishing the container image to GHCR and binaries to GitHub releases.

[Unreleased]: https://github.com/4nonX/typehaus/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/4nonX/typehaus/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/4nonX/typehaus/releases/tag/v0.1.0
