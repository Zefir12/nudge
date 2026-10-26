# Web UI design

The look and layout of the web UI. The approved mockups are in the design canvas
(https://claude.ai/artifact/BydkfDeZjqbppy8vjAgjn4: global dashboard, environment dashboard,
containers list, sidebar, style sheet). The style is taken from szaszki.com: dark, glassy,
small radii, an orange accent. Tokens live in `frontend/src/style.css`; components use
only the variables, never raw colours.

## Layout
- **Top bar** (56px, sticky, translucent with blur): logo (links to `/`), breadcrumb
  (Dashboard / environment / section; the current page is plain text). A hairline along the bottom
  edge fades from transparent to `--accent-border` and back.
- **Sidebar** (248px, sticky), `AppSidebar.vue`:
  - Global part, the same on every page: account box (avatar initial, name, role, log out),
    environment picker (`EnvSwitcher`), then Dashboard, Environments, Registries,
    Notifications, API tokens, Audit log.
  - Per-environment part below a divider: headed by the open (or last used) environment's
    name and lamp; Overview, Stacks, Containers, Images, Volumes, Networks, Events. Pages that
    belong to one environment go here; pages that span environments go in the global part.
- **Global dashboard** (`/`): one large tile per environment (`EnvironmentTile`), plus an
  "Add environment" tile for admins. A tile opens `/env/:env`.
- **Environment dashboard** (`/env/:env`): lamp, name and kind chip, a mono host line,
  stat tiles, live usage, host details.
- At 720px and below the sidebar stacks above the content and its links wrap.

## Tokens
| Token | Value | Use |
|---|---|---|
| `--bg` | `#0b0b0c` | page |
| `--sidebar` | `#0f0f11` | sidebar |
| `--surface` | `rgb(24 24 27 / 55%)` | glass panels (`.card`, tiles) |
| `--surface-solid` | `#17171a` | menus, where translucency would be unreadable |
| `--border` / `--border-strong` | white at 8% / 14% | hairlines / controls, dashed placeholders |
| `--text` / `--text-soft` / `--muted` | `#ededee` / `#b4b4b9` / `#97979d` | text; muted passes 4.5:1 on `--bg` |
| `--faint` | `#5c5c62` | off lamps only, never text |
| `--accent` | `#ff7a30` | accent: active nav, lamps of the brand, focus |
| `--accent-strong` | `#ff9152` | accent text on dark (links, active nav, eyebrows) |
| `--accent-deep` | `#ea580c` | end of the primary button gradient |
| `--accent-hover` | `#ffb07f` | link hover |
| `--accent-soft` / `--accent-border` | accent at 14% / 35% | selected backgrounds / accent borders, hover |
| `--ok`, `--danger`, `--warn` | `#56d364`, `#f0605a`, `#d4a72c` | lamps; `*-text` and `*-soft` (`--warn-soft`) for badges |
| `--radius-sm` / `--radius` / `--radius-lg` | 2 / 3 / 6px | tags / controls / panels |
| `--shadow` | `0 4px 24px rgb(0 0 0 / 40%)` | panels |

Dark only for now (`color-scheme: dark`); a light theme would redefine the same tokens.

## Type
Self-hosted in `frontend/src/assets/fonts/` (SIL OFL, licences next to the files), latin subset,
variable weight. No requests to font CDNs.
- **Space Grotesk** 600 (`--font-display`): headings, big numbers, the environment picker.
- **Inter** 400–600 (`--font`): body 14px, nav 13.5px, buttons 13.5px/600.
- **JetBrains Mono** (`--mono`): IDs, digests, ports, addresses, versions.
- Labels above groups: 11px, 600, uppercase, letter-spacing 0.12–0.16em (`.eyebrow` in accent,
  table headers and nav groups in `--muted`).

## Components
- **Buttons**: default is a faint fill with `--border-strong`; `.primary` is the orange
  gradient with an inner highlight and a soft orange shadow; `.danger` is red text on
  `--danger-soft`. 3px radius.
- **Lamps** (`.lamp`, `.on`, `.bad`, `.warn`): 8px dots that glow in their colour; off is `--faint`.
- **Chips** (`.chip`): uppercase pill naming a kind (socket, agent).
- **Status badges**: 2px radius, tinted background (`--ok-soft`, `--accent-soft`, …).
- **Tiles and cards**: glass panel, 6px radius; links lift 2px and take `--accent-border` on hover.
- **Icons**: inline 24×24 stroke SVG, 1.8 stroke, `currentColor`; no icon font or emoji.
