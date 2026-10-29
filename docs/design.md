# Web UI design

The look and layout of the web UI. The approved mockups are in the design canvas
(https://claude.ai/artifact/BydkfDeZjqbppy8vjAgjn4: global dashboard, environment dashboard,
containers list, sidebar, style sheet). The style is taken from szaszki.com: dark, glassy,
small radii, an orange accent. Tokens live in `frontend/src/style.css`; components use
only the variables, never raw colours.

## Layout
- **Top bar** (56px, sticky, translucent with blur): logo (links to `/`), breadcrumb
  (Dashboard / environment / section; the current page is plain text), and for admins a
  gear on the right (Settings modal: nudge's version and update). A hairline along the bottom
  edge fades from transparent to `--accent-border` and back.
- **Sidebar** (248px, sticky), `AppSidebar.vue`:
  - Global part, the same on every page: account box (avatar initial, name, role, log out),
    environment picker (`EnvSwitcher`: no box or status, an icon picked by hashing the
    environment's name, `envIcon.ts`), then Dashboard, Registries,
    Notifications, API tokens, Audit log.
  - Per-environment part below a divider: headed by the open (or last used) environment's
    name and connection plug; Overview, Stacks, Containers, Images, Volumes, Networks, Events. Pages that
    belong to one environment go here; pages that span environments go in the global part.
- **Global dashboard** (`/`): one large tile per environment (`EnvironmentTile`), two per row,
  plus an "Add environment" tile for admins. Environments are managed here, in modals
  (`SettingsDialog`), for admins: the "Add environment" button and tile open the name form
  (`AddEnvironment`, then the new join token), and a tile's gear top right opens its
  settings (`EnvironmentSettings`: new join token, revoke agent, remove). `/environments`
  redirects here. A tile opens `/env/:env` (its name is the link; a click anywhere else on
  it but its settings opens it too). Inside: CPU,
  memory and disk gauges in a rail on the left (`UsageGauges`); beside them the running,
  images, volumes and stacks counts, CPU and memory over 24h (`UsageHistory`), disk use by
  kind (`DiskUsage`) and events per hour over 7 days (`ActivityHeatmap`, square cells). An
  agent's tile also shows its address (admins only) and last ping. The CPU and memory charts
  are the stored 24h history; disk and activity are mock data (`tileMock.ts`, tagged "mock")
  until the server reports them.
- **Environment dashboard** (`/env/:env`): connection plug, name and kind chip, a mono host line,
  stat tiles, then Needs attention (updates available, stopped containers, untagged images) beside
  Recent events, live usage with the busiest containers, host details.
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
| `--accent-hover` | `#ffb07f` | link hover |
| `--accent-soft` / `--accent-border` | accent at 14% / 35% | selected backgrounds / accent borders, hover |
| `--series-2`, `--series-3` | `#9aa4b8`, `#a51d3b` | chart series after the accent: memory (grey-blue), disk (crimson) |
| `--ok`, `--danger`, `--warn` | `#56d364`, `#f0605a`, `#d4a72c` | lamps; `*-text` and `*-soft` (`--warn-soft`) for badges |
| `--radius-sm` / `--radius` / `--radius-lg` | 2 / 3 / 6px | tags / controls / panels |
| `--shadow` | `0 4px 24px rgb(0 0 0 / 40%)` | panels |

Dark only for now (`color-scheme: dark`); a light theme would redefine the same tokens.

## Type
Self-hosted in `frontend/src/assets/fonts/` (SIL OFL, licences next to the files), latin subset,
variable weight. No requests to font CDNs.
- **Space Grotesk** 600 (`--font-display`): headings, big numbers, the environment picker.
- **Inter** 400–600 (`--font`): body 14px, nav 13.5px, buttons 12.5px/600, fields 13px.
- **JetBrains Mono** (`--mono`): IDs, digests, ports, addresses, versions.
- Labels above groups: 11px, 600, uppercase, letter-spacing 0.12–0.16em (`.eyebrow` in accent,
  table headers and nav groups in `--muted`).

## Components
- **Buttons**: default is a faint fill with `--border-strong`; `.primary` is an orange
  outline with orange text that fills with `--accent-soft` and glows orange on hover; `.danger` is red text on
  `--danger-soft`. 3px radius, compact padding (0.25rem 0.7rem).
- **Fields** (input, select, textarea): sunk into the card (`rgb(0 0 0 / 30%)` background),
  compact padding (0.25rem 0.5rem); no focus ring,
  the border turns `--accent-border`. Selects draw their own chevron, inset as far from the
  right as from top and bottom; where `appearance: base-select` is supported the open list
  is themed (orange border, `--accent-soft` highlight).
- **Tables**: cells centred vertically. Row actions are icon buttons (`ContainerActions`
  with `icons`), each in its own colour (start green, stop amber, restart orange, kill red;
  remove grey until hovered), named by their tooltip.
- **Scrollbars**: a thin rounded grey thumb on no track, everywhere.
- **Settings** (`SettingsDialog`): native `<dialog>` modal, `--surface-solid`, blurred dark
  backdrop; opened by a `GearButton` (top bar for the server, an agent environment's
  dashboard header for its agent).
- **Connection plug** (`ConnPlug`): an environment's connection; plugged in and glowing
  `--accent` when online, pulled apart and `--faint` when not (`--warn` while waiting for its agent).
- **Lamps** (`.lamp`, `.bad`, `.warn`): 8px dots that glow in their colour; off is `--faint`.
- **Hover**: effects come in fast (`--hover-in`, 0.04s) and fade out at 0.12–0.15s.
- **Chips** (`.chip`): uppercase pill naming a kind (socket, agent).
- **Status badges**: 2px radius, tinted background (`--ok-soft`, `--accent-soft`, …).
- **Tiles and cards**: glass panel, 6px radius; links lift 2px and take `--accent-border` on hover.
- **Icons**: inline 24×24 stroke SVG, 1.8 stroke, `currentColor`; no icon font or emoji.
  Sidebar icons turn orange and grow slightly on hover. CPU, memory and disk have their own
  icons (`usageIcons.ts`) in their series colour.
