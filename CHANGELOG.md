# Changelog

All notable changes to typehaus are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/).
The section matching a release tag becomes that release's notes on GitHub.

## [Unreleased]

## [1.0.0] - 2026-11-13

typehaus 1.0: the API is stable – everything under `/api/v1` changes only compatibly from here on. New in this release: the API with webhooks, panels for InDesign, Photoshop and Illustrator, Affinity support, free fonts and font identification, and web fonts for your websites.

### Added
- **API for other tools.** A documented HTTP/JSON API under `/api/v1` for ticket systems, document servers, brand portals and scripts: fonts (list, details, upload, download, licenses), collections, licenses with their documents, font requests, people and groups, computers, usage and inventory reports, the activity log. The server describes every endpoint at `/api/v1/openapi.json` (OpenAPI 3.1, generated from the code, so it always matches the version you run). See [docs/api.md](docs/api.md).
- **API tokens.** Administrators create them on the new *Integrations* page (with tabs for API tokens, webhooks, web fonts and identification services): *read* or *read & write*, with an optional expiry, shown once and stored only as a hash. Tokens act for the organisation and appear in the activity log as `token:<name>`; they only work on `/api/v1` (not for sign-in, accounts, the apps or token management) and stop working when revoked, expired, or when their creator is no longer an active administrator. Font downloads through a token follow the license rules without the administrator exception.
- **API for production use.** Errors carry a stable machine-readable `code`; lists page with `limit`/`offset`, `X-Total-Count` and `Link` headers and filter on every simple field; writes accept an `Idempotency-Key` for safe retries; each token has a rate limit (`TYPEHAUS_API_RATE`, default 600 a minute) with `RateLimit-*` headers and `Retry-After`; tokens can be rotated with a grace period, renamed and given a new expiry. The activity log records the IP address and token of every change. The 1.0.0 contract is kept in the repository and every build is tested against it.
- **Webhooks.** Signed (HMAC-SHA256) event deliveries to your URL: fonts added or deleted, license changes, licenses expiring (30, 14, 7 and 1 days ahead) or over their seats, font requests, copies found outside typehaus, computers, users, tokens about to expire. Retries for about 22 hours, a delivery log with redelivery, secret rotation. Managed under *Integrations → Webhooks* and through the API; the events are described in `openapi.json`.
- **Find fonts.** A new page for everyone. *Free fonts* searches about 2,700 families from Google Fonts, Font Squirrel and Fontsource (refreshed daily); administrators add a family to the library in one click – Google Fonts families complete, with their open license, Font Squirrel families with their freeware license recorded and its text attached for review – and users request them. *Identify a font* sends an image (dropped, pasted or linked) to every identification service with a key – WhatFontIs, Mixfont, Aspose – and checks each match against the library and the catalog; WhatTheFont, Identifont and others are linked. Keys are entered under *Integrations → Services* or set with `TYPEHAUS_WHATFONTIS_KEY`, `TYPEHAUS_MIXFONT_KEY`, `TYPEHAUS_ASPOSE_CLIENT_ID`/`_SECRET`. `TYPEHAUS_CATALOG=false` and `TYPEHAUS_IDENTIFY=false` switch the features off, e.g. on servers without internet access.
- **Panels for InDesign, Photoshop and Illustrator.** A typehaus panel lists the fonts the open document is missing, switches on the ones the person may use (installed while the panel waits) and requests the others. The typehaus app installs the panels with one click (*Install panel*); they are also on the Devices page. The Illustrator extension is signed in the release build.
- **Affinity.** The typehaus app connects to Affinity's built-in MCP server (Affinity 3.3 and later) and switches on the fonts open documents are missing – automatically, or with a click (*Fonts missing in Affinity*: switch on automatically, only show them, or off); fonts the person can't use can be requested. `typehaus-agent affinity` checks the connection.
- **The app's plugin API.** The typehaus app answers plugins on the same computer (`127.0.0.1:47130`): which of a document's fonts are active, available, refused by a license or not in typehaus, switching them on and requesting them – for the person signed in, with the library's rules. Where a host app allows only https, plugins can exchange request and answer files instead. See [docs/api.md](docs/api.md#the-apps-plugin-api).
- **Web fonts.** *Web kits* (*Integrations → Web fonts*) serve library fonts to websites from one stylesheet link, as WOFF2 with a WOFF fallback; variable fonts with their weight range. Only fonts whose license includes web use and hasn't expired are served, and only to the kit's domains. Page views are counted, and licenses can have a monthly page-view limit.
- **Grid view.** The library and the free font search can show families as a grid of tiles with a pangram, besides the list; the choice is remembered per browser.
- **Exact preview sizes.** Next to the size slider, a field takes the preview size in points (4–400 pt, half points allowed), as design apps show text at 100 %. The size now applies to the expanded styles too and is remembered per browser.
- **Switch everything on or off.** A switch above the library list turns on or off every font in the current view – the whole library, a collection or a search result. It shows a half state when only some are on, asks before switching more than ten styles, and leaves fonts installed by the organisation alone.
- **Waterfall.** A family's *Info* shows the sample text from 8 to 72 pt, for any of its styles.
- **Sample texts.** A menu next to the preview field: pangrams in English and German, the alphabet with umlauts, figures and currency, punctuation and quotes, and running text.
- **Request a font from an empty search.** When a search finds nothing, the library offers to request that font, with the name filled in (or points to another collection that has it).
- **Copies outside typehaus in the audit exports.** The inventory section exports every copy found as CSV, and the license CSV gained a *Copies outside typehaus* column.
- Keyboard: `/` jumps to the library search; `Esc` clears the search or preview field.
- **License documents, fully editable.** Each document has a type (EULA, invoice, certificate or other – read from the file name, changeable), can be renamed, and can be replaced by a new file (a renewed invoice, an updated EULA) keeping its type and visibility; removing asks first. The list shows who added or replaced a file and when. Documents can be dropped onto the list, are also managed in *Edit license*, and can be attached right when creating a license. The license CSV lists them with their type.
- **Dialogs close with a click outside** (and Esc, as before). If something was typed or changed that isn't saved yet, typehaus asks: *Save changes*, *Discard changes* or *Keep editing*.

### Changed
- Licenses gain an optional monthly limit of web page views (`web_pageviews`), and report the current month's views.
- Fonts are counted by family (by name) instead of by style file: in the library sidebar, the collections, the switch above the list, selections and messages ("Activated Carlito (3 styles)"). Style counts still appear where they differ.

### Fixed
- License notes and the license text read from font files lost their structure: wrapped lines of indented items fell back to the left edge, so pasted EULAs no longer showed which clause belongs where, and a full EULA filled the whole license card. Each line now keeps its indentation, paragraphs stay apart, and long texts start collapsed with *Show all*.

## [0.5.0] - 2026-11-13

### Added
- **Collections that install themselves.** A collection can now be *installed automatically*: its fonts arrive on every member's computers without being switched on, and leave when someone leaves the group or the collection. Members see them as installed (with the collection's name) and can't switch them off individually. The collection dialog previews how many people, computers and license seats a change affects and refuses changes that would exceed an enforced license.
- **Font requests.** Users request a missing font (with what it's for) from the library; admins see open requests under *Requests* and answer them (added / declined with a note). Requesters get a badge when an answer arrives.
- **Usage history.** typehaus records each license's daily peak of users, computers and all-users installs. The Licenses page shows the 12-month peak, flags licenses that went over their seats during the year, and draws the usage in the license detail; *Usage history* exports it as CSV, and the license CSV gained peak and all-users columns.
- **Offline limit per license.** For licenses that allow use only while connected: after the set number of days without contact to the server, computers remove the license's fonts (also when the clock is set back) and install them again once the server is reachable. The app shows paused fonts.
- **Inventory scan (opt-in, `TYPEHAUS_INVENTORY=true`).** The app reports once a day which fonts are installed on the computer outside typehaus – names, sizes and checksums, never the files. The Licenses page lists copies of library families that are not open source, per computer, and marks byte-identical copies. `typehaus-agent inventory` shows the same list locally.
- **Commercial license suggestions.** *Detect licenses* (Licenses page, formerly *Detect open-source licenses*) still assigns open-source licenses named in the fonts, then lists unassigned families whose files name a commercial foundry: by OS/2 vendor ID, copyright or trademark line (Adobe, Linotype, Monotype, Microsoft, ITC, URW, Apple and others), or end-user-license wording. The suggested license is that vendor's license if there is exactly one, otherwise a new "<Vendor> (commercial)" license whose notes ask for seats, term and invoice. Nothing commercial is assigned without the admin's choice; an open license in the font always wins, and manual assignments are never changed.
- **License review per family and per style.** The suggestions show the evidence from each font file (foundry, vendor ID, copyright, trademark, license text) and let you pick any license per family, the suggested new one, or none. A family's *Info* now shows the same evidence and lets you set the license per style.
- Vendor ID and trademark are stored at upload. Fonts uploaded earlier are read from their files once, the first time licenses are detected, and never again.
- **Merge and split families by hand.** Select styles (or whole families) in the Library → *Move to family…*: pick an existing family to merge them into it, or type a new name to split them off; with a single style the style name can be changed too. Names set this way are kept when fonts are re-read and are not regrouped automatically; such styles show a *manual* badge with the detected names, and *Use detected names* puts them back. Licenses, usage and search follow the new names.
- **Count administrators** (per license, on by default): admins take seats and are held to the license's limits and download setting like everyone else. Switched off, admins use the license's fonts without taking a seat; the seat holder list marks them. Also in the CSV export.
- **New versions of a font.** An upload that shares a PostScript name with a font already in the library is added and then shown in a dialog with both versions and which one is newer. *Replace* (preselected for a newer version from the same vendor) moves collections, everyone's activations, names set by hand and the license to the new version and removes the old one; computers switch at their next sync. *Keep both* leaves them side by side. If the two come from different vendors (by the manufacturer named in the files, else by the registered vendor ID, since IDs change when foundries are bought), the dialog says so, *Keep both* is preselected, and a replacement never carries the license over, because one foundry's license does not cover another foundry's font. A changed license notice in the file is flagged. In the Library, *Make current version* does the same for selected styles. A file skipped as the same font (for example a corrected build that kept its version number) can be uploaded anyway from the same dialog.
- **Light, dark or system theme.** A switch in the header (System → Light → Dark), remembered per browser and applied before the page draws.

### Changed
- **Commercial licenses no longer allow direct downloads by default.** New commercial licenses start with downloads off, and **existing commercial licenses are switched to no-download by this update** – turn it back on per license where the EULA allows it. Open-source licenses are unaffected.
- Computers that install fonts for all users (Windows service) never receive fonts of an enforced per-user license: such an install can't be attributed to a user. Licenses with all-users installs are flagged.
- Admins are no longer exempt from a license's download restriction and seat check by default; switch off *Count administrators* on a license for the previous behaviour. The server-wide `TYPEHAUS_DOWNLOADS` switch still never applies to admins.

### Fixed
- One family was listed as several: fonts without a typographic family name (name ID 16) carry their weight in the family name ("Helvetica Light" / "Regular", "Arial Black" / "Regular"). Trailing weight, width and slope words now move to the style, as font managers do ("Helvetica" / "Light"); other names ("Helvetica Rounded") stay their own family. Fonts already in the library are re-read once at the next start. In the library, optical sizes named as separate families ("DM Sans 18pt", "Minion Pro Display") are listed under their base family with the size on each style, and grouping ignores case and spacing.
- Uploading a font that was already in the library in another file (WOFF/WOFF2 next to the TTF, a re-saved or re-signed copy, the same font from two sources) added it again. Faces are now compared by a content fingerprint: names, version, vendor (OS/2 vendor ID and manufacturer), copyright and license notices, units per em, glyph count, the full character map, every advance width, weight, width, italic and variable axes. Weights, widths, optical sizes, versions and fonts from other vendors that share a name stay separate entries; the name alone never makes a duplicate. Duplicates are reported with the font they match; in a collection (TTC) only the new faces are added. Entries from before this are fingerprinted from their files the first time they are compared.
- *Usage by family* counted administrators even where the license does not count them; they are now shown apart ("+1 admin"), and administrators' own computers are left out of device counts the same way (all-users computers always count).
- Font previews were cut off: wide faces, long sample text, swashes, tall accents and deep descenders were clipped at the column edge or line height. Previews now wrap and take their line height from the font, in the web library and in the desktop app's window.

## [0.4.0] - 2026-11-12

### Added
- **Installers for every system.** typehaus is now installed like any other program instead of running from the downloaded file:
  - **Windows:** `typehaus-setup-windows.exe`, one setup for Intel/AMD and ARM, in English or German. It installs for the current user without administrator rights to `%LOCALAPPDATA%\Programs\typehaus`, with a Start menu entry and an entry under *Settings → Apps* including an uninstaller. Updates close the running app, replace it and start it again. `/S` installs and uninstalls silently for Intune, GPO or scripts.
  - **macOS:** `typehaus-macos.dmg` with drag-to-Applications, signed and notarized when the Apple credentials are configured.
  - **Linux:** `.deb` and `.rpm` packages for amd64 and arm64: `/usr/bin/typehaus` (also as `typehaus-agent`), a menu entry and icons.
  - The Devices page offers the installer for each system first; the standalone programs remain under *Other downloads*.
  - Uninstalling removes the fonts typehaus installed and its autostart entry.
  - *Start at login* always starts the installed program. The private copies older versions kept for autostart are removed.
  - CI installs, updates and uninstalls on real Windows, Linux and macOS systems on every change, and runs the end-to-end test against the installed Linux package.
- `typehaus-agent quit` quits the running app.

## [0.3.1] - 2026-11-12

### Fixed
- Devices page: after updating the agent, the old version was shown until the next font change. The server now records the agent version on every check.

## [0.3.0] - 2026-11-12

### Added
- **Every font format.** Besides TrueType/OpenType (incl. variable, colour and collections), typehaus now imports:
  - WOFF, WOFF2 and EOT web fonts, converted to TTF/OTF on upload
  - Mac `.dfont`, suitcases and LWFN (from ZIPs made on a Mac)
  - PostScript Type 1 (`.pfb`/`.pfa`/`.t1`) with their `.pfm`/`.afm` metrics
  - Windows bitmap and vector fonts (`.fon`, `.fnt`)
  - X11 bitmap fonts (`.pcf`, `.bdf`, also gzip-compressed)
  
  Each computer only receives formats its system can install. The library shows format and platform badges.
- **License management.**
  - Licenses record their type (commercial or open source), allowed use (desktop, web, apps, e-books & PDFs, video, logos), seats counted per user or per device, purchase and expiry dates, order reference, terms URL and notes.
  - Attach license documents (EULA, invoice, certificate) to a license. Each document is either visible to everyone who can see the fonts, or to admins only. PDFs, text and images open in the browser; anything else is downloaded.
  - Open-source licenses (SIL OFL, Apache, Ubuntu Font Licence, GPL with font exception, AGPL, LPPL, GUST, Bitstream Vera, MIT, CC0) are recognised from the fonts' embedded license information and assigned automatically on upload, or for the whole library with *Detect open-source licenses*.
  - Optional enforcement per license: activations beyond the purchased seats are refused, per-device licenses are installed on at most that many computers, and after expiry the fonts are removed from all computers. Without enforcement typehaus only warns.
  - The Licenses page shows seat usage, terms, expiring (30 days) and over-used licenses, and who holds a seat; the menu marks licenses that need attention. *Export CSV* creates a compliance report.
  - Everyone sees a family's license, allowed use, terms and shared documents under *Info* in the library, together with the embedding permission and the license text stored in the font itself.
- **Direct downloads** for installing fonts the traditional way: a single style as its font file, a family or a selection as a ZIP (one folder per family, Type 1 fonts with their `.pfm`/`.afm`, the font's license text and the license's shared documents). Licenses can forbid direct downloads; enforced licenses allow them only to people holding a seat, and not after expiry. `TYPEHAUS_DOWNLOADS=false` switches downloads off for everyone except admins. Every download is logged.
- Drag and drop whole font folders; large uploads are sent in batches.
- CI imports the complete system font folders of Windows, macOS and Linux on every change, and installs real `.fon`, Type 1, PCF and WOFF2 fonts end to end on all three systems.

### Fixed
- Dark mode: the selection bar in the library was barely readable (dark menus on a light bar). It is now a regular card in both themes and stays in view while scrolling. Native controls (menus, date pickers, scrollbars) follow the theme, and the sign-in page text has proper contrast.
- Windows system fonts in `.fon` format (e.g. `8514OEM.FON`) were rejected as "not a TrueType or OpenType font".
- Linux: activated bitmap fonts were hidden by the distribution's fontconfig defaults; typehaus now allows fonts in its own folder.

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
- **Windows: fonts for all users.** `typehaus-agent machine enroll|install|status|sync|uninstall` sets up a Windows service that installs one account's fonts into `%WINDIR%\Fonts` for every user, for older applications that ignore per-user fonts. The device is marked *all users*. The per-user app skips those fonts and loads newly added ones into the running session.
- **Code signing pipeline:** Windows builds are signed via SignPath Foundation, macOS builds are signed with a Developer ID and notarized. Each switches on when its credentials are configured (`docs/signing.md`, `CODE_SIGNING_POLICY.md`). Signed agents are also bundled in the Docker image.
- **End-to-end tests on real Windows, macOS and Linux runners** in CI (`tools/e2e`): install, visibility to applications, removal, revocation, and on Windows the all-users service.

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

[Unreleased]: https://github.com/4nonX/typehaus/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/4nonX/typehaus/compare/v0.5.0...v1.0.0
[0.5.0]: https://github.com/4nonX/typehaus/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/4nonX/typehaus/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/4nonX/typehaus/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/4nonX/typehaus/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/4nonX/typehaus/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/4nonX/typehaus/releases/tag/v0.1.0
