# Bundled dependency licenses

- `onnxruntime-LICENSE` and `onnxruntime-ThirdPartyNotices.txt` come from [ONNX Runtime v1.28.2](https://github.com/microsoft/onnxruntime/tree/v1.28.2). `onnxruntime.json` records their exact URLs and SHA-256 hashes. Packaging verifies both hashes and the bundled library's reported version. Update the notices and manifest when changing the runtime.
- `Inter-OFL.txt` comes from [Inter v3.19](https://github.com/rsms/inter/blob/v3.19/LICENSE.txt), matching the Inter 3.019 font shipped in `frontend/public/Inter-Medium.ttf`.
- The package copies sherpa-onnx's license from the pinned Go SDK and collects license/notice files from compiled Go modules and installed frontend dependencies into `THIRD_PARTY_NOTICES.txt`.

Ntikuma's own MIT license is in the repository root. Model files are downloaded separately and retain their own licenses.
