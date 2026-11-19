# macOS CLI artifact notices

`just release macos-artifact` packages the following files before generating the
per-file SHA-256 manifest:

| Archive file | Source |
| --- | --- |
| `LICENSE` | Project AGPL-3.0-only license |
| `NOTICE` | Project notice, including vendored ONNX C API header attribution |
| `licenses/sherpa-onnx-go-macos-LICENSE` | Installed `github.com/k2-fsa/sherpa-onnx-go-macos` module license |
| `licenses/onnxruntime-LICENSE` | Pinned ONNX Runtime v1.28.2 upstream license |
| `licenses/onnxruntime-ThirdPartyNotices.txt` | Pinned ONNX Runtime v1.28.2 upstream notices |

The current macOS module v1.13.8 contains its Apache license but omits the ONNX
redistribution files. Upstream's v1.28.2 `LICENSE` and `ThirdPartyNotices.txt`
are checked in under `packaging/licenses/onnxruntime-1.28.2/`, because
`microsoft/onnxruntime` was removed from GitHub on 2026-11-18. The helper
`scripts/package-macos-notices.sh` copies them and checks the same SHA-256 values
as the Linux recipe. It fails on a missing file or digest error, a changed embedded runtime version, missing source notices, or an
unrecognized bundled dylib. Review and update the notice set with dependency
upgrades; do not bypass these checks.

Run `just release macos-notices-test` for isolated failure-path checks. Actual
artifact verification should extract the tarball, compare all manifest digests,
verify project notice bytes, verify binary/library signatures, and run the
relocated executable with `--version`. This does not require model downloads or
audio hardware.

This fixes a missing-notices packaging defect. It does not establish permission
for embedded voice references, clear the public-release asset gate, or certify
every dependency/model's licensing. The native desktop app is a separate bundle
and requires its own distribution review.
