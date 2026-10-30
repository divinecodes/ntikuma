# Ntikuma landing page

The site published at https://divinecodes.github.io/ntikuma/.

It is a static site with no build step: `index.html`, `styles.css`, `main.js` and `assets/`. The popup shots are HTML recreations of the app's real views, using its dark libadwaita tokens. They are not screenshots, so they stay sharp at any size.

`main.js` only adds behavior: the phone menu, the typed hero question, the video's play button and the command approval demo. The page reads the same without it. Scroll animations are CSS scroll-driven animations; browsers without support show the page at rest. All motion is off under the system's reduced-motion setting.

## Preview

```bash
python3 -m http.server 8000
```

Run it from `web/`, then open http://localhost:8000.

## Deploy

`.github/workflows/pages.yml` publishes `web/` to GitHub Pages on every push to `master` that touches it, and can also be run by hand. Turn it on once under Settings > Pages > Source: GitHub Actions. Pages needs the repository to be public on a free plan, so the workflow skips itself while the repository is private.

Every path is relative, so the folder also works on any other static host or subpath. The share-preview URLs in `index.html` (`og:url`, `og:image`, `canonical`) are absolute; update them if the site moves.

## Keeping it in sync with the app

Copy on the page mirrors the Ntikuma README. Check these against the app when they change:

- Version and package name (`v0.1.7`, `ntikuma_0.1.7_amd64.deb`) in the hero and Install sections.
- Keyboard shortcuts, transcription models, and the default and fallback models.
- `assets/og.jpg` (the social preview), `assets/demo-poster.jpg` (the same frame at 1280px, the video's poster) and `assets/ntikuma-demo.mp4` come from `brag-output/` (`brag.jpg`, and `brag.mp4` remuxed with `ffmpeg -c copy -movflags +faststart`). The video is lazy-loaded: `main.js` attaches its poster as it nears the viewport and its source only when someone presses play.

## Fonts

Ubuntu Sans and Ubuntu Sans Mono (Latin subsets) are self-hosted in `assets/fonts/` under the Ubuntu Font Licence; the licence files sit beside them.
