# Installing typehaus

typehaus is one container with one data volume. You need:

- a Linux host with Docker (amd64 or arm64), e.g. a VM or a NAS
- a DNS name for it, e.g. `fonts.example.com`
- a reverse proxy with HTTPS in front of it (strongly recommended: device tokens and passwords travel over this connection)

Contents: [Dockge](#install-with-dockge) · [Docker Compose](#install-with-docker-compose) · [Reverse proxy](#reverse-proxy-and-https) · [First steps](#first-steps) · [Computers](#install-the-app-on-computers) · [Updating](#updating) · [Backup](#backup-and-restore) · [Troubleshooting](#troubleshooting)

## Install with Dockge

1. In Dockge, click **+ Compose** and name the stack `typehaus`.
2. Replace the editor content with [`docker-compose.yaml`](../docker-compose.yaml).
3. Paste [`.env.example`](../.env.example) into the stack's **.env** field and adjust it:
   - `TYPEHAUS_BASE_URL`: the public address, e.g. `https://fonts.example.com` (required)
   - `TYPEHAUS_ADMIN_PASSWORD`: a password for the first admin. If you leave it empty, a random one is printed in the log.
   - `TYPEHAUS_BIND=127.0.0.1` if your reverse proxy runs on the same host. typehaus is then only reachable through the proxy.
4. Click **Deploy**.
5. If you didn't set a password, open the stack's log and look for the line `created initial admin account … password=…`.

Prefer files? Create `/opt/stacks/typehaus/compose.yaml` and `/opt/stacks/typehaus/.env` (Dockge's default stacks folder; yours may differ), then use **Scan Stacks Folder** in Dockge's menu.

The compose file uses a named Docker volume (`typehaus_typehaus-data`) for the database and fonts. To keep the data in the stack folder instead, see [Storing data in a folder](#storing-data-in-a-folder).

## Install with Docker Compose

```sh
mkdir typehaus && cd typehaus
curl -LO https://raw.githubusercontent.com/4nonX/typehaus/main/docker-compose.yaml
curl -L https://raw.githubusercontent.com/4nonX/typehaus/main/.env.example -o .env
nano .env                      # set TYPEHAUS_BASE_URL and TYPEHAUS_ADMIN_PASSWORD
docker compose up -d
docker compose logs typehaus   # shows the generated admin password if you left it empty
```

### Storing data in a folder

The container runs as an unprivileged user (uid `65532`). A host folder must belong to that user:

```sh
mkdir -p data && sudo chown 65532:65532 data
```

Then in `docker-compose.yaml`, replace `- typehaus-data:/data` with `- ./data:/data` and remove the `volumes:` block at the bottom.

## Reverse proxy and HTTPS

Point the proxy at `http://<host>:8080` (or the container name `typehaus:8080` if both share a Docker network). Three things matter:

- **HTTPS.** Set `TYPEHAUS_BASE_URL` to the `https://` address. Secure cookies are then enabled automatically.
- **Upload size.** Font archives can be large. Allow request bodies up to `TYPEHAUS_MAX_UPLOAD_MB` (default 512 MB). nginx-based proxies only allow 1 MB by default.
- **Forwarded headers.** Keep `TYPEHAUS_TRUST_PROXY=true` so typehaus sees real client IPs (used for login rate limiting and the device list). Set it to `false` only if typehaus is exposed directly without a proxy.

**Caddy**

```caddy
fonts.example.com {
	request_body {
		max_size 512MB
	}
	reverse_proxy typehaus:8080
}
```

**Nginx Proxy Manager:** add a proxy host for `fonts.example.com` → `http`, `typehaus`, `8080`, request a Let's Encrypt certificate, and under *Advanced* add:

```nginx
client_max_body_size 512m;
```

**Traefik** (labels on the `typehaus` service; adjust entrypoint and resolver names):

```yaml
    labels:
      - traefik.enable=true
      - traefik.http.routers.typehaus.rule=Host(`fonts.example.com`)
      - traefik.http.routers.typehaus.entrypoints=websecure
      - traefik.http.routers.typehaus.tls.certresolver=letsencrypt
      - traefik.http.services.typehaus.loadbalancer.server.port=8080
```

Traefik doesn't limit body size by default.

**Pangolin / Cloudflare Tunnel:** create an HTTP resource for `fonts.example.com` pointing at `http://<host>:8080`. If the tunnel puts authentication in front of the site, **exclude `/api/agent/*` and `/downloads/*`**. The desktop app authenticates with its own device token and can't pass a browser login. The same applies to `/auth/oidc/callback` if you use single sign-on.

## First steps

1. Sign in as `admin` and change the password (**Account**).
2. **Library:** drop in fonts (TTF, OTF, TTC or ZIP archives).
3. **People:** create groups (e.g. *Design*, *Marketing*) and users, or set up single sign-on (see the [README](../README.md#single-sign-on-oidc)).
4. **Collections:** create collections, choose who may see them, and add fonts from the Library (select → *Add to collection*).
5. **Licenses** (optional): record purchased licenses and assign them to fonts to see seat usage.

## Install the app on computers

Every user installs the typehaus app on their computers. The Devices page links the right download from your own server:

1. In typehaus open **Devices**, download the app and click **Create enrollment code**.
2. Open the app. Its window asks for the **server address** and the **code**; both are shown on the Devices page with Copy buttons.
3. Click **Connect**. Fonts you switch on in the Library arrive within a minute, and the app starts automatically at login.

The apps aren't code-signed with a paid certificate yet, so the operating system asks once:

- **Windows:** SmartScreen shows "Windows protected your PC". Click **More info → Run anyway**.
- **macOS:** unzip `typehaus-agent-darwin-universal.app.zip` and **move typehaus.app to Applications** first (otherwise it can't start at login). Then open it:
  - **macOS 15 Sequoia and newer:** opening it shows a warning. Click **Done**, go to **System Settings → Privacy & Security**, scroll down and click **Open Anyway** next to typehaus, then confirm.
  - **macOS 14 and older:** right-click typehaus.app → **Open** → **Open**.
- **Linux:** `chmod +x typehaus-agent-linux-amd64` and run it. The tray icon needs a StatusNotifier panel (KDE, Xfce, Cinnamon, or GNOME with the *AppIndicator* extension). Without one, typehaus keeps syncing in the background, and running it again opens its window.

Company-managed computers can also be set up from the command line, for example with a software deployment tool:

```sh
typehaus-agent enroll --server https://fonts.example.com --code ABCD-EFGH-JKLM
typehaus-agent autostart enable
```

## Updating

**Server:** in Dockge, click **Update** on the stack (pulls the image and recreates the container). With plain Compose: `docker compose pull && docker compose up -d`. To stay on a specific release, set `TYPEHAUS_VERSION=0.2.0` in `.env` instead of `latest`. Database migrations run automatically on start.

**Apps:** after a server update, each app shows **Update available** in its menu and window. Clicking it opens the Devices page; download the new version and open it. It replaces the running one and takes over *Start at login*. On macOS, replace typehaus.app in Applications.

## Backup and restore

Everything lives in the data volume: `typehaus.db` (SQLite) and `fonts/` (the font files).

Consistent backup while typehaus is running (writes `typehaus.db` and `typehaus-fonts.tgz` into the current folder):

```sh
docker run --rm -v typehaus_typehaus-data:/data -v "$PWD":/backup alpine \
  sh -c 'apk add --no-cache sqlite >/dev/null && sqlite3 /data/typehaus.db ".backup /backup/typehaus.db" && tar czf /backup/typehaus-fonts.tgz -C /data fonts'
```

To restore, stop the stack, put `typehaus.db` and `fonts/` back into the volume (owned by uid 65532) and start it again. Alternatively, stop the stack and copy the whole volume; that is always consistent.

## Troubleshooting

| Problem | Fix |
|---|---|
| Stack doesn't start: "Set TYPEHAUS_BASE_URL" | Add `TYPEHAUS_BASE_URL` to `.env`. |
| `permission denied` on `/data` | A bind-mounted folder must be owned by uid 65532, see [Storing data in a folder](#storing-data-in-a-folder). |
| Uploads fail with *413 Request Entity Too Large* | Raise the proxy's body size limit (see above). |
| App says "Can't reach that server" | Check the address (including `https://`) from that computer's browser. With Pangolin or Cloudflare Access, exclude `/api/agent/*`. |
| App says the certificate isn't trusted | Private CA: install it in the OS trust store, or paste it under *Advanced* when connecting. |
| Fonts don't show up in an application | Restart the application; some apps only read the font list at start. A few older Windows apps only see fonts installed for all users. |
| Lost admin password | `docker exec typehaus typehaus-server reset-password admin` prints a new password (in Dockge: the stack's terminal, or the host shell). It also re-enables the account and makes it an administrator. |
