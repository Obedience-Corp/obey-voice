# Application icon

The Obedience Corp cyan/white mark on its dark background is shared with
`Obedience-Corp/obey-voice-apple` at revision
`77ad42ac4482ea54ea75511014b224587250797c`.

- `obediencecorp-logo-dark.svg` is copied from that project's `docs/marketing/brand/`.
- `AppIcon.iconset/*.png` are the unchanged 16–1024 pixel renditions from
  `apps/macos/ObeyVoiceMac/Resources/Assets.xcassets/AppIcon.appiconset/`.
- `scripts/bundle.py` compiles these renditions with macOS `iconutil` and declares
  `AppIcon.icns` in the bundle metadata for Finder and the Dock.
- `src/main.rs` embeds the 512-pixel rendition for the running app, including
  direct `just run` launches, so eframe cannot replace the logo with its default.

Keep the vector and PNG renditions in sync when the corporate mark changes.
The build uses the checked-in PNGs and needs no SVG renderer or sibling checkout.

## Menu bar and Linux panel

`tray-template.svg` and `tray-color.svg` reuse the Corp logo paths without the
square background, cropped for small status icons. The template uses black
alpha artwork for macOS adaptive tinting; Linux uses cyan/white artwork.
Generate the checked-in 32-pixel PNGs with `rsvg-convert -w 32 -h 32` and `-o`.
The Rust tray artwork module adds live microphone, meeting, pause, and error
badges. Native status text also reports these states.
