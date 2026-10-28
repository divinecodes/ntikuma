# Installation and local voice models

This guide covers Ntikuma on Linux with GNOME, including Parakeet, Qwen3-ASR, Cohere Transcribe, and whisper.cpp. Run commands from the Ntikuma repository root unless a step changes directory.

- [Install and build Ntikuma](#install-and-build-ntikuma)
- [Debian package](#debian-package)
- [Move a source installation to the Debian package](#move-a-source-installation-to-the-debian-package)
- [Set up native transcription](#set-up-native-transcription)
- [Parakeet](#parakeet)
- [Qwen3-ASR](#qwen3-asr)
- [Cohere Transcribe](#cohere-transcribe)
- [Whisper through whisper.cpp](#whisper-through-whispercpp)
- [Install for daily use](#install-for-daily-use)
- [Check dictation](#check-dictation)
- [Compare models on your recordings](#compare-models-on-your-recordings)
- [Troubleshooting](#troubleshooting)

Downloads and builds require internet access. Once configured, these models can transcribe on your device without an OpenRouter key. Sending the resulting question uses OpenRouter and requires a key. Ntikuma does not download models from Preferences.

## Install and build Ntikuma

Install Go 1.26+ and Node.js 24 with npm. On Ubuntu, install the system dependencies:

```bash
sudo apt update
sudo apt install build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev \
  git cmake curl pipewire-bin python3 desktop-file-utils
```

`pipewire-bin` provides `pw-record` for microphone capture. Ntikuma can also use `parecord` from `pulseaudio-utils`. Use the GTK4/WebKitGTK 6.0 packages above for the default build. [Wails Linux prerequisites](https://v3.wails.io/contributing/setup).

Check your tools, then bootstrap and build:

```bash
go version
node --version
npm --version
export PATH="$(go env GOPATH)/bin:$HOME/.local/bin:$PATH"
./scripts/bootstrap.sh
wails3 build
```

Bootstrap installs the Wails CLI version pinned in `go.mod`, restores dependencies, and generates missing local build files. Do not substitute a different Wails release. The application is built at `bin/ntikuma`; `wails3 dev` runs development mode.

Open Preferences:

```bash
./bin/ntikuma settings
```

If another Ntikuma instance is running, commands go to that instance. Quit it from the tray or with `./bin/ntikuma quit` before launching a newly built version.

Save your OpenRouter key in Preferences to send questions. It is stored in GNOME Keyring. `NTIKUMA_OPENROUTER_KEY` overrides the saved key when set before launch. See [README](README.md) for chat, context capture, and web search controls.

## Debian package

The Linux amd64 package targets Ubuntu 24.04 and Debian 13 with GNOME. It installs the app at `/usr/bin/ntikuma` and the native speech helper and libraries under `/usr/lib/ntikuma/`. Parakeet, Qwen, and Cohere model downloads remain separate. Whisper still requires its separately installed server.

To build a package, first complete the source prerequisites and bootstrap above, then install the packaging tools:

```bash
sudo apt install binutils dpkg-dev patchelf desktop-file-utils
python3 scripts/package-deb.py --version 0.1.0
```

The build creates `bin/packages/0.1.0/ntikuma_0.1.0_amd64.deb` and `SHA256SUMS`. `--version` accepts `X.Y.Z`; `NTIKUMA_VERSION` supplies its default, otherwise `0.1.0` is used. The package and executable report the same version. `--output-dir` chooses another artifact directory. `--skip-build` packages existing binaries only after checking the version, architecture, bundled notices, and helper runtime. Freshly bootstrapped checkouts also route `wails3 task linux:create:deb` through this packager. Bootstrap preserves existing local build files; use the Python command above in those checkouts.

Verify and install a downloaded or locally built package:

```bash
cd bin/packages/0.1.0
sha256sum -c SHA256SUMS
sudo apt install ./ntikuma_0.1.0_amd64.deb
/usr/bin/ntikuma show
```

APT resolves the package's runtime dependencies. Quit Ntikuma before upgrading an installed package, then start it again afterward. Open Preferences to save your OpenRouter key and choose a downloaded model folder. Leave Speech runtime executable empty to discover `/usr/lib/ntikuma/ntikuma-asr` automatically. A saved custom helper path continues to take precedence. Return to the repository root before running its model-download commands below.

The package does not change user shortcuts or enable login startup. To configure shortcuts, run `ntikuma-install-shortcuts` as your normal user; it asks before reassigning layout-switching keys. To opt into login startup:

```bash
install -Dm644 /usr/share/ntikuma/autostart.desktop \
  "${XDG_CONFIG_HOME:-$HOME/.config}/autostart/dev.divinetettey.ntikuma.desktop"
```

Remove that user autostart entry to disable it. Package upgrades and removal preserve chats, preferences, models, and keyring credentials. Dependencies' notices are installed under `/usr/share/doc/ntikuma/`.

An existing `scripts/install.sh` installation needs the migration below. Its `~/.local/bin` executable and user launcher can override the system package, while shortcuts and an explicit helper setting can still target old files. Installing the `.deb` alone preserves those user files.

## Move a source installation to the Debian package

Run the migration as your normal desktop user, without `sudo`, using the same `HOME` and XDG directories as your existing installation. Preview it from the checkout before installing the package:

```bash
python3 scripts/migrate-deb.py --source-checkout "$PWD"
```

Preview reads the current installation without changing files, GNOME settings, or the running app. It lists the old executable, existing launcher/autostart entries, Ntikuma shortcut commands, and any known old speech-helper path that will change. `--source-checkout` recognizes that checkout's app and helper paths without deleting them. A nonstandard executable in a user bin directory can be included with `--source-bin /absolute/path/to/ntikuma`; that executable will be backed up and removed. Repeat these options in preview and apply. The source checkout itself and its speech libraries are kept.

Quit Ntikuma from its tray menu, install the verified `.deb` as described above, then return to the repository root and use the packaged migration command:

```bash
/usr/bin/ntikuma-migrate-deb --source-checkout "$PWD"
/usr/bin/ntikuma-migrate-deb --source-checkout "$PWD" --apply
hash -r
/usr/bin/ntikuma show
```

Apply refuses to proceed while a Ntikuma process is running. It checks the installed application and speech runtime, then makes a private backup under `${XDG_DATA_HOME:-$HOME/.local/share}/ntikuma/migration-backups/`. Backup directories use mode `0700` and files use `0600`. Backups contain old executables, changed desktop entries, preferences, affected shortcut commands, and a consistent SQLite copy including committed WAL data. Keep them private; they contain your chats and settings.

The migration removes the old user executable after backing it up, retargets existing launchers and shortcuts to `/usr/bin/ntikuma`, and switches recognized old helper paths to `/usr/lib/ntikuma/ntikuma-asr`. It preserves disabled or absent autostart, shortcut keys, unrelated shortcuts, and layout-switching settings. Custom helper paths are kept for you to check in Preferences. `--skip-shortcuts` explicitly leaves shortcut commands alone for systems without GNOME settings access.

Chats stay in the same database. Model folders, source runtimes, Whisper settings, and keyring credentials are neither moved nor deleted. Keep a checkout that still holds model files. After migration, check your chats, a shortcut, and Voice > Check setup. Repeating apply after a successful migration makes no further changes.

If an operation fails, the command attempts to undo the migration changes. You can also quit Ntikuma and restore using the exact backup directory printed by apply:

```bash
/usr/bin/ntikuma-migrate-deb --restore /absolute/path/to/the/printed/backup
```

Restore refuses to overwrite files or shortcuts edited since migration. It restores installation references and the prior preferences file, while keeping the current chat database and model files. The SQLite backup is retained for separate recovery; restore does not replace newer chats with the backup.

## Set up native transcription

Parakeet, Qwen, and Cohere share an optional native speech helper. The current helper supports Linux x86_64 and ARM64 and uses CPU inference. Install only the models you want to try; each has separate saved settings.

The Debian package already supplies the helper. Package users can skip the helper build and continue with [Prepare the download tools](#prepare-the-download-tools).

### Build the speech helper

```bash
python3 scripts/build-asr-helper.py
./bin/ntikuma-asr --version
```

The build uses Go, a C compiler, and the pinned sherpa-onnx 2.0.0 binding. Keep these outputs together:

```text
bin/
  ntikuma
  ntikuma-asr
  asr-lib/
    libonnxruntime.so
    libsherpa-onnx-c-api.so
    ...
```

Python drives the build/download scripts. Transcription itself runs through the Go helper and needs neither a Python environment nor a transcription server. Rebuild the helper when updating its source or pinned runtime.

### Prepare the download tools

Install `uv` if it is not already available, then install Python 3.12:

```bash
curl -LsSf https://astral.sh/uv/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
uv python install 3.12
python3.12 --version
```

These follow the [uv installation](https://docs.astral.sh/uv/getting-started/installation/) and [Python installation](https://docs.astral.sh/uv/guides/install-python/) instructions. The commands below use `python3.12` explicitly so they do not depend on your system's `python3` version. The download script requires an existing Python 3.12+ interpreter and disables automatic interpreter downloads during preparation.

Choose one of the individual model sections below, or download all three:

```bash
python3.12 scripts/evaluate-asr.py prepare --models parakeet qwen cohere
```

Preparation creates `build/asr-eval/.venv/`, installs the evaluation dependencies pinned in `scripts/asr-eval/requirements.txt`, and downloads verified model bundles. It does not change your selected model. Without `--models`, preparation downloads Qwen and Parakeet only.

| Model | CLI name | Compressed download | Extracted model folder |
| --- | --- | ---: | ---: |
| Parakeet TDT 0.6B v3 int8 | `parakeet` | About 487 MB | About 641 MiB |
| Qwen3-ASR 0.6B int8 | `qwen` | About 879 MB | About 954 MiB |
| Cohere Transcribe 03-2026 2B int8 | `cohere` | About 1.70 GB | About 2.7 GiB |

Allow at least 8 GiB of free disk space for all three archives, extracted models, and runtime caches, plus space for application builds and recordings. Exact archives, URLs, required files, and SHA-256 checksums are pinned in [models.json](scripts/asr-eval/models.json). Partial downloads resume when the server supports byte ranges; completed archives are verified before extraction.

### Configure a native model

For any of the three native models:

1. Open Preferences > Voice and choose the corresponding Transcription model.
2. Click Browse and select the extracted model folder listed below. Select the bundle itself, not its parent `models/` directory or the `.tar.bz2` archive.
3. Start with four CPU threads. Valid values are whole numbers from 1 to 64. More threads are not guaranteed to improve latency.
4. Leave Advanced runtime settings > Speech runtime executable empty for the Debian package, or when the helper is beside the running app or on its PATH. Otherwise enter the helper's absolute path.
5. For Cohere, choose Spoken language. Qwen and Parakeet detect language automatically.
6. Click Check setup and wait for Saved. Setup checks files and helper compatibility without recording or loading the full model. Dictation checks model loading.

Each model remembers its own folder and thread count. Cohere also remembers its language. Changes apply to the next recording. Successful native transcription keeps one model loaded; switching models replaces it on the next recording. Esc during recording or transcription cancels the active operation.

## Parakeet

Download:

```bash
python3.12 scripts/evaluate-asr.py prepare --models parakeet
```

Select Parakeet TDT 0.6B v3 in Preferences and browse to:

```text
build/asr-eval/models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8
```

Keep `encoder.int8.onnx`, `decoder.int8.onnx`, `joiner.int8.onnx`, and `tokens.txt` together. Use the complete pinned int8 bundle. [Upstream Parakeet configuration](https://github.com/k2-fsa/sherpa-onnx/blob/master/sherpa-onnx/c-api/docs/offline-asr.dox).

## Qwen3-ASR

Download:

```bash
python3.12 scripts/evaluate-asr.py prepare --models qwen
```

Select Qwen3-ASR 0.6B in Preferences and browse to:

```text
build/asr-eval/models/sherpa-onnx-qwen3-asr-0.6B-int8-2026-03-25
```

Keep `conv_frontend.onnx`, `encoder.int8.onnx`, `decoder.int8.onnx`, and the entire `tokenizer/` directory. The tokenizer must include `vocab.json`, `merges.txt`, and `tokenizer_config.json`. A model downloaded for another runtime is not a substitute for this bundle.

## Cohere Transcribe

Download:

```bash
python3.12 scripts/evaluate-asr.py prepare --models cohere
```

Select Cohere Transcribe 2B in Preferences and browse to:

```text
build/asr-eval/models/sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01
```

Keep `encoder.int8.onnx`, `encoder.int8.onnx.data`, `decoder.int8.onnx`, `tokens.txt`, and `silero_vad.onnx`. The `.data` file holds the large encoder weights and must remain beside the encoder graph. Preparation also downloads and verifies the small Silero model used for local speech detection. [Upstream Cohere configuration](https://github.com/k2-fsa/sherpa-onnx/blob/master/go-api-examples/non-streaming-cohere-transcribe-decode-files/main.go).

Set Spoken language to English for English dictation. Available languages are English, French, German, Italian, Spanish, Portuguese, Greek, Dutch, Polish, Chinese, Japanese, Korean, Vietnamese, and Arabic. Cohere needs an explicit language; it does not automatically detect it. Mixed-language dictation may be less reliable. [Official model card](https://huggingface.co/CohereLabs/cohere-transcribe-03-2026).

Ntikuma applies local speech detection before Cohere recognition to reduce invented text from silence. If no speech is detected, dictation shows a retry message. Test quiet speech with your own microphone as well as ordinary speech.

## Whisper through whisper.cpp

Whisper uses a separate `whisper-server` process. It does not use `ntikuma-asr`, the sherpa-onnx bundles above, or the evaluation virtual environment.

### Build and download a Whisper model

These commands create a checkout at `~/src/whisper.cpp`. If one already exists, use that checkout instead of cloning again:

```bash
mkdir -p "$HOME/src"
git clone https://github.com/ggml-org/whisper.cpp.git "$HOME/src/whisper.cpp"
cd "$HOME/src/whisper.cpp"
sh ./models/download-ggml-model.sh base.en
cmake -B build -DCMAKE_BUILD_TYPE=Release -DWHISPER_BUILD_SERVER=ON
cmake --build build --config Release --target whisper-server -j2
```

This downloads the English `base.en` model to `models/ggml-base.en.bin` and builds `build/bin/whisper-server`. Choose other supported sizes or multilingual models using the [whisper.cpp model documentation](https://github.com/ggml-org/whisper.cpp/tree/master/models), then update the configured model path. The example establishes a working setup; compare recognition quality on your own recordings.

### Let Ntikuma manage the server

Choose Whisper in Preferences > Voice and set:

| Setting | Value |
| --- | --- |
| whisper-server address | `http://127.0.0.1:8178` |
| Start it with | `~/src/whisper.cpp/build/bin/whisper-server` |
| Whisper model | `~/src/whisper.cpp/models/ggml-base.en.bin` |
| Language | `en` |

Ntikuma expands `~/` in these paths. Wait for Saved, then check setup. Managed startup begins when transcription starts after you stop recording. It requires a loopback HTTP address with an explicit port and waits up to 15 seconds for readiness. A successful transcription keeps the owned server warm; canceling an active operation or quitting stops it.

### Run the server yourself

Alternatively, leave this running in another terminal:

```bash
cd "$HOME/src/whisper.cpp"
./build/bin/whisper-server \
  -m models/ggml-base.en.bin \
  --host 127.0.0.1 --port 8178 --language en
```

Choose Whisper and set the address to `http://127.0.0.1:8178`. Executable and model fields may be empty when you manage the server yourself. An already-running server controls its own model; changing the path in Preferences does not reload its weights. Stop and restart that server to change models. Ntikuma leaves an externally started server running when it quits. [Server options](https://github.com/ggml-org/whisper.cpp/blob/master/examples/server/README.md).

Return to the Ntikuma repository root before using its scripts below.

## Install for daily use

Build the app, then run the user installer:

```bash
wails3 build
./scripts/install.sh
```

This installs `~/.local/bin/ntikuma`, the launcher/icon, and login autostart. It offers GNOME shortcuts and starts the app. No `sudo` is needed. Re-running it upgrades the app while preserving preferences, chats, and keyring credentials. See [installer options](README.md#install-for-daily-use) for custom paths and startup controls.

The installer currently copies the main application only. For native voice models, set Speech runtime executable to the absolute path of your checkout's `bin/ntikuma-asr` and keep that checkout available. The helper's adjacent `asr-lib/` directory is required.

To keep the native runtime outside the checkout instead, install without starting the app, then copy the complete runtime:

```bash
./scripts/install.sh --no-start
mkdir -p "$HOME/.local/lib/ntikuma-asr"
cp --remove-destination bin/ntikuma-asr "$HOME/.local/lib/ntikuma-asr/"
cp -a --remove-destination bin/asr-lib "$HOME/.local/lib/ntikuma-asr/"
"$HOME/.local/bin/ntikuma" settings
```

Set Speech runtime executable to `~/.local/lib/ntikuma-asr/ntikuma-asr` in Preferences. Repeat these commands after rebuilding the helper, with Ntikuma stopped. `--remove-destination` replaces existing files instead of opening them for writing, so upgrades work with the read-only libraries. Keep the executable and libraries together.

Model files can remain under the checkout, or you can copy the extracted folders to a permanent location:

```bash
mkdir -p "$HOME/.local/share/ntikuma/models"
cp -a build/asr-eval/models/. "$HOME/.local/share/ntikuma/models/"
```

Update each Model folder setting to its new absolute path and check setup again. Copying uses additional disk space. Keep the old files until you have confirmed the new paths work. App updates do not download models or change these saved paths.

## Check dictation

1. Open dictation from the microphone button or tray. With the provided GNOME shortcuts, use Super+Shift+Space.
2. Confirm the selected model's name appears in the voice view, then speak normally.
3. Press Tab to stop and review the transcript. Edit it before sending, or press Enter during recording to stop, transcribe, and send immediately.
4. Try Record again and another short phrase to check warm reuse. Press Esc during recording or transcription to cancel.
5. For Cohere, try silence and then quiet speech. Silence should show a retry message; speech should remain available to review.

For a microphone check independent of transcription, run this and press Ctrl+C to finish the WAV:

```bash
pw-record --rate 16000 --channels 1 --format s16 /tmp/ntikuma-mic-test.wav
```

Select the intended input in GNOME Sound settings if audio is empty or too quiet. Delete that test WAV yourself when finished. Application recordings are temporary and removed after transcription or cancellation. Native transcription splits recordings longer than 30 seconds into shorter model inputs.

Local recognition does not require an OpenRouter key. Sending a transcript sends its text and attached context to your configured chat model. Public sample success does not establish accuracy for your accent; compare names, numbers, missing words, and the editing effort on your own speech.

## Compare models on your recordings

The evaluation script uses the same local model bundles without changing Preferences. Prepare every model you list in `--models`. On Linux, evaluation also needs `libseccomp.so.2`, supplied by Ubuntu's `libseccomp2` package, to block IPv4/IPv6 socket creation during inference.

Create a personal manifest once:

```bash
python3.12 scripts/evaluate-asr.py init --directory build/asr-eval/personal
```

If that directory already contains a manifest or recordings, keep it and skip initialization. Record from an interactive terminal:

```bash
python3.12 scripts/evaluate-asr.py record \
  --manifest build/asr-eval/personal/manifest.json
```

Press Enter to start and stop each recording, or enter `q` before a take to pause. Existing WAV files are kept. Keep clips under 30 seconds. Review every `reference` in the JSON so it matches what you actually said, including any changes to the prompt.

Compare all three with the same audio:

```bash
python3.12 scripts/evaluate-asr.py run --models parakeet qwen cohere \
  --manifest build/asr-eval/personal/manifest.json \
  --output build/asr-eval/results/personal-three-models \
  --threads 4 --repeat 3 --language en
```

Use a new output folder for each run; earlier results are not overwritten. Reports include word error rate, timing, memory, silence behavior, and transcripts. `--language en` configures Cohere; Qwen and Parakeet continue to detect language automatically. Cohere evaluation includes its speech detector. Formatting differences such as digits versus written numbers count as word errors, so also review meaning yourself.

The default workspace is `build/asr-eval/`. To use another location, pass `--work /absolute/path` before the subcommand on every invocation. Keep personal audio and reports private. `build/`, `bin/`, model downloads, and generated artifacts are ignored by Git.

## Troubleshooting

| Problem | What to check |
| --- | --- |
| `wails3` is missing or the version differs | Put Go's executable directory on PATH and rerun `./scripts/bootstrap.sh`; it installs the version in `go.mod`. |
| `uv` or `python3.12` is missing | Complete the download-tools setup above and add the installation's executable directory to PATH. `prepare` does not download Python automatically. |
| No recorder or no captured audio | Install `pipewire-bin` or `pulseaudio-utils`, select the microphone in GNOME Sound settings, and try the recorder command above. |
| Model folder is missing files | Select the extracted bundle directory and rerun preparation for that model. Keep the tokenizer, external `.data` weights, and detector with their respective model. |
| Download checksum/size mismatch | Keep the checksum check enabled. Remove only the named corrupt download and rerun that model's preparation; do not delete your recordings. |
| Native speech runtime is missing | Build the helper. Set its executable path when running an installed app, and keep `asr-lib/` beside it. |
| A shared library cannot be loaded | Restore the complete `asr-lib/` directory next to the helper, then run that helper with `--version` to verify startup. |
| Helper does not support Cohere | Rebuild `scripts/build-asr-helper.py` and ensure Preferences points to the rebuilt copy. |
| Whisper server is not ready | Match the port in Preferences and the server command. Configure both managed startup paths, or start your own server and wait for model loading. Installing the recorder does not install Whisper. |
| Whisper takes longer than the startup timeout | Start `whisper-server` yourself, wait until ready, then retry dictation. |
| Changes seem to have no effect | Wait for Saved and start a new recording. Quit the previous app instance before launching a new build. Restart an externally managed Whisper server to change its model. |
| Super+Space is unavailable | Follow [GNOME shortcut setup](README.md#gnome-shortcuts). The script asks before reassigning GNOME's layout-switching keys. |

Preferences default to `~/.config/ntikuma/config.json`; chats use `~/.local/share/ntikuma/ntikuma.db`. XDG environment variables can override these locations. Prefer the UI for voice settings; quit Ntikuma before editing the configuration file manually.
