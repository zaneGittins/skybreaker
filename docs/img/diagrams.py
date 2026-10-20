#!/usr/bin/env python3
"""Draws the docs' diagrams as light and dark SVGs, in the UI's colours and
font. Run from the repository root: python3 docs/img/diagrams.py"""
import base64
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
FONT = base64.b64encode((ROOT / "web/shared/fonts/figtree.woff2").read_bytes()).decode()

THEMES = {
    "dark": dict(bg="#141412", host="#1A1916", card="#24231F", cardb="#37352F", frame="#423F39",
                 text="#F1EEE7", muted="#9A9487", line="#6E695F", accent="#F6C443", deny="#FF8A7A",
                 mark="#F1EEE7", sun="#F6C443", planet="#F08A5D"),
    "light": dict(bg="#F6F4EF", host="#FCFBF8", card="#FFFFFF", cardb="#E5DFD3", frame="#D9D2C4",
                  text="#1C1B18", muted="#6E695F", line="#A39C8E", accent="#C28A12", deny="#C4473A",
                  mark="#1C1B18", sun="#E9AF1F", planet="#E57A4B"),
}

CH = 92  # card height


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def head(c, w, h, label):
    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img" aria-label="{esc(label)}">',
        "<style>",
        f'@font-face{{font-family:"Figtree";src:url(data:font/woff2;base64,{FONT}) format("woff2");font-weight:400 700}}',
        f'text{{font-family:"Figtree",-apple-system,"Segoe UI",Helvetica,Arial,sans-serif;fill:{c["text"]}}}',
        f'.title{{font-size:15.5px;font-weight:650}}.sub{{font-size:13px;fill:{c["muted"]}}}',
        f'.label{{font-size:12.5px;font-weight:550;fill:{c["muted"]}}}.accent-label{{fill:{c["accent"]}}}',
        f'.deny{{font-size:12.5px;font-weight:550;fill:{c["deny"]}}}',
        f'.caps{{font-size:11.5px;font-weight:650;letter-spacing:.09em;fill:{c["muted"]}}}',
        '.frame-title{font-size:14px;font-weight:650}',
        f'.card{{fill:{c["card"]};stroke:{c["cardb"]};stroke-width:1}}',
        f'.line{{stroke:{c["line"]};stroke-width:1.6}}.accent{{stroke:{c["accent"]};stroke-width:2}}',
        f'.denyline{{stroke:{c["deny"]};stroke-width:1.4;stroke-dasharray:3 4}}',
        "</style>",
        "<defs>",
    ]
    for name, col in (("line", c["line"]), ("accent", c["accent"]), ("deny", c["deny"])):
        out.append(f'<marker id="{name}-end" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M1 1.5 9 5 1 8.5z" fill="{col}"/></marker>')
        out.append(f'<marker id="{name}-start" viewBox="0 0 10 10" refX="1" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M9 1.5 1 5 9 8.5z" fill="{col}"/></marker>')
    out += ["</defs>", f'<rect width="{w}" height="{h}" rx="18" fill="{c["bg"]}"/>']
    return out


def card(x, y, w, title, lines):
    out = [f'<rect class="card" x="{x}" y="{y - CH // 2}" width="{w}" height="{CH}" rx="12"/>']
    ty = y - CH // 2 + 30
    out.append(f'<text class="title" x="{x + 18}" y="{ty}">{esc(title)}</text>')
    for i, line in enumerate(lines):
        out.append(f'<text class="sub" x="{x + 18}" y="{ty + 22 + i * 18}">{esc(line)}</text>')
    return "\n".join(out)


def hline(x1, x2, y, label="", cls="line", both=False, lx=None):
    m = "accent" if cls == "accent" else "line"
    start = f' marker-start="url(#{m}-start)"' if both else ""
    out = f'<line class="{cls}" x1="{x1}" y1="{y}" x2="{x2}" y2="{y}"{start} marker-end="url(#{m}-end)"/>'
    if label:
        lx = (x1 + x2) / 2 if lx is None else lx
        out += f'\n<text class="label {cls}-label" x="{lx}" y="{y - 10}" text-anchor="middle">{esc(label)}</text>'
    return out


def mark(c, x, y, scale):
    return (f'<g transform="translate({x} {y}) scale({scale})">'
            f'<circle cx="16" cy="16" r="4.4" fill="{c["sun"]}"/>'
            f'<path d="M24.93 14.9A9 9 0 1 1 17.1 7.07" fill="none" stroke="{c["mark"]}" stroke-width="2.6" stroke-linecap="round"/>'
            f'<circle cx="23.3" cy="8.7" r="1.5" fill="{c["planet"]}"/></g>')


def architecture(c):
    w, h = 1024, 380
    r1, r2 = 164, 280
    left, lw, host_x = 24, 180, 290
    sky_x, sb_x, fw, cw = 312, 704, 280, 240
    top, bottom = 76, 352
    out = head(c, w, h, "Skybreaker architecture: your phone reaches the app over Tailscale; the app drives agents in a gVisor sandbox per project; every request from a sandbox leaves through the broker, which adds credentials.")
    out += [
        f'<rect x="{host_x}" y="20" width="{w - host_x - 16}" height="{h - 40}" rx="16" fill="{c["host"]}" stroke="{c["frame"]}"/>',
        f'<text class="caps" x="{host_x + 22}" y="48">ONE LINUX HOST</text>',
        f'<rect x="{sky_x}" y="{top}" width="{fw}" height="{bottom - top}" rx="14" fill="none" stroke="{c["frame"]}"/>',
        mark(c, sky_x + 18, top + 12, 0.62),
        f'<text class="frame-title" x="{sky_x + 42}" y="{top + 28}">skybreaker</text>',
        f'<rect x="{sb_x}" y="{top}" width="{fw}" height="{bottom - top}" rx="14" fill="none" stroke="{c["frame"]}" stroke-dasharray="5 5"/>',
        f'<text class="frame-title" x="{sb_x + 20}" y="{top + 28}">gVisor sandbox<tspan class="sub" dx="6">one per project</tspan></text>',
        card(left, r1, lw, "Phone or browser", ["anywhere on", "your tailnet"]),
        card(left, r2, lw, "The internet", ["GitHub, model APIs,", "package registries"]),
        card(sky_x + 20, r1, cw, "App", ["chats, boards, inbox,", "approvals, activity"]),
        card(sky_x + 20, r2, cw, "Broker", ["holds every credential,", "decides what may leave"]),
        card(sb_x + 20, r1, cw, "Claude Code, Codex, pi", ["your subscriptions,", "API keys or local models"]),
        card(sb_x + 20, r2, cw, "Your repo and tools", ["builds, tests, Docker;", "no DNS, no other route"]),
        hline(left + lw, sky_x + 20, r1, "Tailscale", lx=(left + lw + host_x) / 2),
        hline(sky_x + 20 + cw, sb_x + 20, r1, "chats, MCP", both=True),
        hline(sb_x + 20, sky_x + 20 + cw, r2, "every request", cls="accent"),
        hline(sky_x + 20, left + lw, r2, "+ credentials", cls="accent", lx=(left + lw + host_x) / 2),
        "</svg>",
    ]
    return w, h, out


def request_path(c):
    w, h = 1024, 300
    stages = [
        ("Caller", ["sandbox network and", "a live project token"], "407 or 403"),
        ("Destination", ["listed hosts only, as", "tunnel, mitm or route"], "403"),
        ("Rules", ["auto, ask or deny", "per method and path"], "403, or held for you"),
        ("Credentials", ["secret added only for", "the hosts it's bound to"], "fails closed"),
        ("Connect", ["resolved addresses", "checked, then upstream"], "blocked ranges refused"),
    ]
    x0, bw, gap, cy = 24, 172, 31, 116
    out = head(c, w, h, "The broker's request path: caller, destination, rules, credentials, connect. Each stage can deny, and every decision is audited.")
    out.append(f'<text class="caps" x="{x0}" y="44">EVERY REQUEST FROM A SANDBOX</text>')
    for i, (title, lines, deny) in enumerate(stages):
        x = x0 + i * (bw + gap)
        out.append(card(x, cy, bw, title, lines))
        if i < len(stages) - 1:
            out.append(hline(x + bw, x + bw + gap, cy, cls="accent"))
        dx = x + 18
        out.append(f'<line class="denyline" x1="{dx}" y1="{cy + CH // 2}" x2="{dx}" y2="{cy + CH // 2 + 36}" marker-end="url(#deny-end)"/>')
        out.append(f'<text class="deny" x="{dx + 12}" y="{cy + CH // 2 + 34}">{esc(deny)}</text>')
    last = x0 + 4 * (bw + gap) + bw
    out += [
        f'<rect x="{x0}" y="{h - 64}" width="{last - x0}" height="40" rx="10" fill="{c["host"]}" stroke="{c["frame"]}"/>',
        f'<text class="label" x="{x0 + 18}" y="{h - 39}">Audit: every decision, allowed or not, is logged and shown on the Activity page.</text>',
        "</svg>",
    ]
    return w, h, out


for name, draw in (("architecture", architecture), ("request-path", request_path)):
    for theme, colours in THEMES.items():
        _, _, parts = draw(colours)
        (ROOT / f"docs/img/{name}-{theme}.svg").write_text("\n".join(parts) + "\n")
