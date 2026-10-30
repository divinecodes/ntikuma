<script lang="ts">
  import { onMount } from "svelte";
  import { app } from "../lib/state.svelte";
  import { historyApi, prefsApi, voiceApi } from "../lib/api";
  import { Preferences } from "../lib/preferences.svelte";
  import { modelName } from "../lib/format";
  import Icon from "../components/Icon.svelte";
  import Switch from "../components/Switch.svelte";

  const prefs = new Preferences({
    ...prefsApi,
    checkVoice: voiceApi.checkSetup,
    pickVoiceFolder: voiceApi.pickModelFolder,
    saved: (settings) => { app.applySettings(settings); void app.refreshProvider(); },
    keyChanged: () => { void app.refreshProvider(); },
    clearHistory: () => historyApi.clear(),
    cleared: () => app.historyCleared(),
    canClear: () => !app.streaming,
  });
  const s = $derived(prefs.settings);
  const models = $derived(prefs.models);
  const active = $derived(prefs.active);
  const target = $derived(prefs.modelTarget);
  const local = $derived(active ? /^http:\/\/(localhost|127\.|\[::1\])/i.test(active.baseUrl) : false);
  let newPreset = $state(0);
  const localModel = $derived(s?.voice.engine === "qwen3-asr" ? s.voice.qwen : s?.voice.engine === "parakeet" ? s.voice.parakeet : s?.voice.engine === "cohere-transcribe" ? s.voice.cohere : null);
  let newFallback = $state("");
  let newToolFolder = $state("");

  onMount(() => {
    void prefs.load();
    void prefs.loadModels();
    return () => prefs.dispose();
  });

  function changed() {
    prefs.changed();
  }

  function addFallback() {
    if (prefs.addFallback(newFallback)) newFallback = "";
  }

  function addToolFolder() {
    if (prefs.addToolRoot(newToolFolder)) newToolFolder = "";
  }
</script>

<header>
  <button type="button" class="icon-btn" aria-label="Back" onclick={() => app.back()}>
    <Icon name="back" size={18} stroke={2} />
  </button>
  <h1>Preferences</h1>
  <span class="status" role="status">{prefs.status}</span>
</header>

<datalist id="models">
  {#each models as m (m.id)}
    <option value={m.id}>{m.name}</option>
  {/each}
</datalist>
<datalist id="connection-models">
  {#each prefs.connectionModels as m (m.id)}
    <option value={m.id}>{m.name}</option>
  {/each}
</datalist>

<div class="scroll">
  {#if prefs.loading}<p role="status">Loading preferences...</p>{/if}
  {#if prefs.loadError}
    <p class="error" role="alert">{prefs.loadError}</p>
    <button type="button" class="btn" onclick={() => prefs.load()}>Retry loading preferences</button>
  {/if}
  {#if prefs.error}
    <p class="error" role="alert">{prefs.error}</p>
    <button type="button" class="btn" onclick={() => prefs.flush()}>Retry saving</button>
  {/if}

  {#if s && target}
    <section>
      <h2>Provider</h2>
      <p class="desc">Questions go through OpenRouter, or straight to a provider you add here. Web search needs OpenRouter.</p>
      {#if active}
        <p class="desc">
          {#if local}
            A server on this computer can still pass questions on. Ollama's cloud models run on Ollama's servers; to keep everything here, use local models and start Ollama with <code>OLLAMA_NO_CLOUD=1</code>.
          {:else}
            Questions, attachments and tool results go straight to {active.name}. Its own data and retention policies apply.
          {/if}
        </p>
      {/if}
      <div class="list">
        <div class="row">
          <div class="text"><label for="provider">Send questions to</label><small>{active ? active.baseUrl : "openrouter.ai"}</small></div>
          <select id="provider" class="field" value={s.provider || "openrouter"} disabled={prefs.connectionBusy} onchange={(e) => prefs.setProvider(e.currentTarget.value)}>
            <option value="openrouter">OpenRouter</option>
            {#each s.connections as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
          </select>
        </div>
        {#if active}
          <div class="row">
            <div class="text"><label for="provider-name">Name</label></div>
            <input id="provider-name" class="field" bind:value={active.name} oninput={changed} />
          </div>
          <div class="row">
            <div class="text"><label for="provider-url">Address</label><small>The API's base URL. http:// only for this computer.</small></div>
            <input id="provider-url" class="field mono" bind:value={active.baseUrl} oninput={() => prefs.connectionChanged()} spellcheck={false} />
          </div>
          <div class="row">
            <div class="text">
              <label for="provider-key">API key</label>
              <small>{prefs.connectionHasKey ? "A key is saved for this address" : local ? "Optional for a server on this computer" : "Not set for this address"}</small>
            </div>
            <input id="provider-key" class="field" type="password" placeholder={prefs.connectionHasKey ? "••••••••" : ""} bind:value={prefs.connectionKeyInput} autocomplete="off" disabled={prefs.connectionKeySaving} />
            <button type="button" class="btn" disabled={!prefs.connectionKeyInput.trim() || prefs.connectionKeySaving} onclick={() => prefs.saveConnectionKey()}>{prefs.connectionKeySaving ? "Saving..." : prefs.connectionHasKey ? "Replace" : "Save"}</button>
          </div>
          <div class="row">
            <div class="text">
              <label for="provider-context">Context window</label>
              <small>Tokens the model reads at once; 0 if unknown. Required for servers on this computer. For Ollama, see the CONTEXT column of <code>ollama ps</code>, or raise it with OLLAMA_CONTEXT_LENGTH.</small>
            </div>
            <input id="provider-context" class="field narrow" type="number" min="0" step="1" bind:value={active.contextTokens} oninput={changed} />
          </div>
        {/if}
        {#each s.connections as c (c.id)}
          {#if c.id !== active?.id}
            <div class="row">
              <div class="text"><span>{c.name}</span><small class="mono">{c.baseUrl}</small></div>
              <button type="button" class="icon-btn" aria-label="Remove {c.name}" disabled={prefs.connectionBusy} onclick={() => prefs.removeConnection(c.id)}>
                <Icon name="x" stroke={2} />
              </button>
            </div>
          {/if}
        {/each}
        {#if prefs.presets.length && s.connections.length < 8}
          <div class="row">
            <label for="add-provider" class="sr-only">Provider to add</label>
            <select id="add-provider" class="field wide" bind:value={newPreset}>
              {#each prefs.presets as p, i (i)}<option value={i}>{p.name}</option>{/each}
            </select>
            <button type="button" class="btn" disabled={prefs.connectionBusy} onclick={() => prefs.addConnection(prefs.presets[newPreset])}><Icon name="plus" size={14} stroke={2.2} />Add provider</button>
          </div>
        {/if}
        {#if prefs.connectionError}<p class="error" role="alert">{prefs.connectionError}</p>{/if}
      </div>
    </section>

    <section>
      <h2>Models</h2>
      <p class="desc">{active ? `Every chat starts on ${active.name}'s default model. Fallbacks are tried in order when it's down, rate-limited or doesn't have the model.` : "Every chat starts on the default. Fallbacks are tried in order when it's down, rate-limited or refuses."}</p>
      {#if active && prefs.connectionModelsError}
        <p class="desc">No model suggestions from {active.name}: {prefs.connectionModelsError}. You can still enter a model ID.</p>
      {/if}
      {#if !active && prefs.modelsLoading}<p class="desc" role="status">Loading model suggestions...</p>{/if}
      {#if !active && prefs.modelsError}
        <p class="error" role="alert">Couldn't load model suggestions: {prefs.modelsError}. You can still enter a model ID.</p>
        <button type="button" class="btn" onclick={() => prefs.loadModels()}>Retry model suggestions</button>
      {/if}
      <div class="list">
        <div class="row">
          <div class="text"><label for="default-model">Default model</label><small>{modelName(target.defaultModel)}</small></div>
          <input id="default-model" class="field" list={active ? "connection-models" : "models"} placeholder={active ? `Model ID, e.g. ${active.kind === "anthropic" ? "claude-opus-5-5" : active.kind === "openai" ? "gpt-6.1-sol" : "llama3.3"}` : ""} bind:value={target.defaultModel} oninput={changed} />
        </div>
        {#each target.fallbacks as f, i (i)}
          <div class="row">
            <span class="num">{i + 1}</span>
            <div class="text"><span>{modelName(f)}</span><small class="mono">{f}</small></div>
            <button type="button" class="icon-btn" aria-label="Move {f} up" disabled={i === 0} onclick={() => prefs.moveFallback(i, -1)}>
              <Icon name="up" stroke={2} />
            </button>
            <button type="button" class="icon-btn" aria-label="Move {f} down" disabled={i === target.fallbacks.length - 1} onclick={() => prefs.moveFallback(i, 1)}>
              <Icon name="down" stroke={2} />
            </button>
            <button type="button" class="icon-btn" aria-label="Remove {f}" onclick={() => prefs.removeFallback(i)}>
              <Icon name="x" stroke={2} />
            </button>
          </div>
        {/each}
        {#if target.fallbacks.length < 3}
          <div class="row">
            <label for="add-fallback" class="sr-only">Add fallback model</label>
            <input
              id="add-fallback"
              class="field wide"
              list={active ? "connection-models" : "models"}
              placeholder={active ? "Add a fallback model ID" : "Add a fallback, e.g. openai/gpt-5.1"}
              bind:value={newFallback}
              onkeydown={(e) => { if (e.key === "Enter") { e.preventDefault(); addFallback(); } }}
            />
            <button type="button" class="btn" onclick={addFallback}><Icon name="plus" size={14} stroke={2.2} />Add</button>
          </div>
        {/if}
      </div>
    </section>

    <section>
      <h2>OpenRouter</h2>
      <div class="list">
        <div class="row">
          <div class="text">
            <label for="api-key">API key</label>
            <small>{prefs.hasKey ? "An API key is configured" : "Not set. Get one at openrouter.ai/keys"}</small>
          </div>
          <input id="api-key" class="field" type="password" placeholder={prefs.hasKey ? "••••••••" : "sk-or-…"} bind:value={prefs.keyInput} autocomplete="off" disabled={prefs.keySaving} />
          <button type="button" class="btn" disabled={!prefs.keyInput.trim() || prefs.keySaving} onclick={() => prefs.saveKey()}>{prefs.keySaving ? "Saving..." : prefs.hasKey ? "Replace" : "Save"}</button>
        </div>
        {#if prefs.keyError}<p class="error" role="alert">{prefs.keyError}</p>{/if}
        <div class="row">
          <div class="text"><label for="system-prompt">System prompt</label></div>
        </div>
        <div class="row tall">
          <textarea id="system-prompt" rows="3" bind:value={s.systemPrompt} oninput={changed}></textarea>
        </div>
      </div>
    </section>

    <section>
      <h2>Context</h2>
      <p class="desc">Anything attached shows as a chip you can remove before sending.</p>
      <div class="list">
        <div class="row">
          <div class="text"><span>Attach clipboard when Ntikuma opens</span></div>
          <Switch label="Attach clipboard when Ntikuma opens" checked={s.attachClipboard} onchange={(v) => { if (s) { s.attachClipboard = v; changed(); } }} />
        </div>
        <div class="row">
          <div class="text"><span>Attach selected text when Ntikuma opens</span><small>{app.selectionSupport?.message ?? "Checking selected-text support…"}</small></div>
          <Switch label="Attach selected text when Ntikuma opens" checked={s.attachSelection} onchange={(v) => { if (s) { s.attachSelection = v; changed(); } }} />
        </div>
        <div class="row">
          <div class="text"><span>Offer a screenshot</span><small>Choose what to capture in the desktop dialog. Requires an image-capable model.</small></div>
          <Switch label="Offer a screenshot" checked={s.offerScreenshot} onchange={(v) => { if (s) { s.offerScreenshot = v; changed(); } }} />
        </div>
        <div class="row">
          <div class="text"><label for="max-chars">Text context limit</label><small>Maximum bytes per clipboard or selection attachment. Zero means unlimited.</small></div>
          <input id="max-chars" class="field narrow" type="number" min="0" step="1" bind:value={s.maxContextChars} oninput={changed} />
        </div>
      </div>
    </section>

    <section id="local-tools-section">
      <h2>Local tools</h2>
      <p class="desc">Allow the model to list folders and read text files in the folders below. File contents it reads are sent to your chat model through {active?.name ?? "OpenRouter"}.</p>
      <div class="list">
        <div class="row">
          <div class="text"><span>Enable local tools</span><small>Read-only file access in your tool folders</small></div>
          <Switch checked={s.localTools} label="Enable local tools" onchange={(v) => { s.localTools = v; prefs.toolSettingsChanged(); }} />
        </div>
        <div class="row">
          <div class="text"><span>Read hidden files</span><small>Lets file tools read dotfiles such as .bashrc and .config. Key and credential files, such as .env, .ssh and .aws/credentials, stay refused.</small></div>
          <Switch checked={s.allowHiddenFiles} label="Read hidden files" onchange={(v) => { s.allowHiddenFiles = v; prefs.toolSettingsChanged(); }} />
        </div>
        <div class="row">
          <div class="text"><span>Allow shell commands</span><small>Requires local tools. Ask before every command. Commands run as your user and can access files outside the tool folders.</small></div>
          <Switch checked={s.allowShellCommands} label="Allow shell commands" onchange={(v) => { s.allowShellCommands = v; prefs.toolSettingsChanged(); }} />
        </div>
        {#each s.toolRoots as folder, i (i)}
          <div class="row tool-folder-row">
            <label for={`tool-root-${i}`} class="sr-only">Tool folder {i + 1}</label>
            <input id={`tool-root-${i}`} class="field wide tool-folder" bind:value={s.toolRoots[i]} oninput={() => prefs.toolSettingsChanged()} spellcheck={false} />
            <button type="button" class="btn" disabled={prefs.toolPicking || prefs.voicePicking} aria-label={`Browse tool folder ${i + 1}`} onclick={() => prefs.pickToolFolder(i)}>Browse</button>
            <button type="button" class="icon-btn" aria-label={`Remove tool folder ${i + 1}`} onclick={() => prefs.removeToolRoot(i)}><Icon name="x" stroke={2} /></button>
          </div>
        {/each}
        {#if s.toolRoots.length === 0}<p class="desc no-tool-folders">No folders are allowed. Add a folder to permit file access.</p>{/if}
        {#if s.toolRoots.length < 32}
          <div class="row tool-folder-row">
            <label for="new-tool-folder" class="sr-only">New tool folder</label>
            <input id="new-tool-folder" class="field wide" placeholder="/home/you/folder or ~/folder" bind:value={newToolFolder} spellcheck={false}
              onkeydown={(e) => { if (e.key === "Enter") { e.preventDefault(); addToolFolder(); } }} />
            <button type="button" class="btn" disabled={!newToolFolder.trim()} onclick={addToolFolder}>Add</button>
            <button type="button" class="btn" disabled={prefs.toolPicking || prefs.voicePicking} onclick={() => prefs.pickToolFolder()}>{prefs.toolPicking ? "Choosing..." : "Browse"}</button>
          </div>
        {/if}
        {#if prefs.toolError}<p class="error" role="alert">{prefs.toolError}</p>{/if}
      </div>
      <p class="desc">Each folder includes its subfolders. Removing every folder denies all file access. Files must be UTF-8 text and at most 1 MiB.</p>
    </section>

    <section>
      <h2>Voice</h2>
      <p class="desc">Choose a transcription model for dictation. Qwen, Parakeet, and Cohere run on your CPU using downloaded model files. Changes apply to the next recording.</p>
      <div class="list">
        <div class="row">
          <div class="text"><label for="voice-engine">Transcription model</label></div>
          <select id="voice-engine" class="field" bind:value={s.voice.engine} onchange={() => prefs.voiceChanged()}>
            <option value="whisper-server">Whisper · whisper.cpp server</option>
            <option value="parakeet">Parakeet TDT 0.6B v3</option>
            <option value="qwen3-asr">Qwen3-ASR 0.6B</option>
            <option value="cohere-transcribe">Cohere Transcribe 2B</option>
          </select>
        </div>
        {#if localModel}
          <div class="row">
            <div class="text"><label for="voice-model-dir">Model folder</label><small>Select the extracted bundle</small></div>
          </div>
          <div class="row">
            <input id="voice-model-dir" class="field wide" bind:value={localModel.modelDir} oninput={() => prefs.voiceChanged()} placeholder="Path to the downloaded model folder" />
            <button type="button" class="btn" disabled={prefs.voicePicking || prefs.toolPicking} onclick={() => prefs.pickVoiceFolder()}>{prefs.voicePicking ? "Choosing..." : "Browse"}</button>
          </div>
          <div class="row">
            <div class="text"><label for="voice-threads">CPU threads</label><small>{s.voice.engine === "cohere-transcribe" ? "Speech detection filters silence before transcription" : "Language is detected automatically"}</small></div>
            <input id="voice-threads" class="field narrow" type="number" min="1" max="64" step="1" bind:value={localModel.threads} oninput={() => prefs.voiceChanged()} />
          </div>
          {#if s.voice.engine === "cohere-transcribe"}
            <div class="row">
              <div class="text"><label for="cohere-language">Spoken language</label><small>Choose the language you will dictate</small></div>
              <select id="cohere-language" class="field" bind:value={s.voice.cohere.language} onchange={() => prefs.voiceChanged()}>
                <option value="en">English</option><option value="fr">French</option><option value="de">German</option>
                <option value="it">Italian</option><option value="es">Spanish</option><option value="pt">Portuguese</option>
                <option value="el">Greek</option><option value="nl">Dutch</option><option value="pl">Polish</option>
                <option value="zh">Chinese</option><option value="ja">Japanese</option><option value="ko">Korean</option>
                <option value="vi">Vietnamese</option><option value="ar">Arabic</option>
              </select>
            </div>
          {/if}
          <details class="voice-advanced">
            <summary>Advanced runtime settings</summary>
            <div class="row"><div class="text"><label for="voice-helper">Speech runtime executable</label><small>Optional. Ntikuma finds its installed runtime automatically.</small></div></div>
            <div class="row"><input id="voice-helper" class="field wide" bind:value={s.voice.nativeHelper} oninput={() => prefs.voiceChanged()} placeholder="Automatic" /></div>
          </details>
        {:else}
        <div class="row">
          <div class="text"><label for="whisper-url">whisper-server address</label></div>
          <input id="whisper-url" class="field" bind:value={s.voice.whisperUrl} oninput={() => prefs.voiceChanged()} />
        </div>
        <div class="row">
          <div class="text"><label for="whisper-bin">Start it with</label><small>Optional. Path to whisper-server</small></div>
          <input id="whisper-bin" class="field" placeholder="/usr/local/bin/whisper-server" bind:value={s.voice.whisperServerBin} oninput={() => prefs.voiceChanged()} />
        </div>
        <div class="row">
          <div class="text"><label for="whisper-model">Whisper model</label><small>Optional. Used when Ntikuma starts the server</small></div>
          <input id="whisper-model" class="field" placeholder="/home/you/models/ggml-base.en.bin" bind:value={s.voice.whisperModelPath} oninput={() => prefs.voiceChanged()} />
        </div>
        <div class="row">
          <div class="text"><label for="lang">Language</label></div>
          <input id="lang" class="field narrow" bind:value={s.voice.language} oninput={() => prefs.voiceChanged()} />
        </div>
        <p class="desc">An already-running Whisper server controls its own model. The model path applies when Ntikuma starts the server.</p>
        {/if}
        <div class="row">
          <button type="button" class="btn" disabled={prefs.voiceChecking || prefs.voicePicking} onclick={() => prefs.checkVoice()}>{prefs.voiceChecking ? "Checking..." : "Check setup"}</button>
          <span class="desc">Checks configuration without recording.</span>
        </div>
        {#if prefs.voiceReadiness}
          <p class:error={!prefs.voiceReadiness.ready} class="desc" role={prefs.voiceReadiness.ready ? "status" : "alert"}>{prefs.voiceReadiness.message}</p>
        {/if}
        {#if prefs.voiceError}<p class="error" role="alert">{prefs.voiceError}</p>{/if}
      </div>
    </section>

    <section>
      <h2>Shortcuts</h2>
      <p class="desc">Set in GNOME Settings › Keyboard › Custom Shortcuts. <code>scripts/install-shortcuts.sh</code> does it for you.</p>
      <div class="list">
        <div class="row">
          <div class="text"><span>Show or hide Ntikuma</span><small class="mono">ntikuma toggle</small></div>
          <span class="keys"><kbd>Super</kbd><kbd>Space</kbd></span>
        </div>
        <div class="row">
          <div class="text"><span>Start dictation</span><small class="mono">ntikuma voice</small></div>
          <span class="keys"><kbd>Super</kbd><kbd>Shift</kbd><kbd>Space</kbd></span>
        </div>
        <div class="row">
          <div class="text"><span>Use the GlobalShortcuts portal instead</span><small>Experimental. Takes effect after restarting Ntikuma</small></div>
          <Switch label="Use the GlobalShortcuts portal" checked={s.usePortalShortcuts} onchange={(v) => { if (s) { s.usePortalShortcuts = v; changed(); } }} />
        </div>
      </div>
    </section>

    <section>
      <h2>History</h2>
      <div class="list">
        <div class="row">
          <div class="text"><label for="keep">Keep chats</label><small>Old chats are removed when Ntikuma starts</small></div>
          <select id="keep" class="field narrow" bind:value={s.keepChatsDays} onchange={changed}>
            <option value={0}>Forever</option>
            <option value={30}>30 days</option>
            <option value={90}>90 days</option>
            <option value={365}>A year</option>
            {#if ![0, 30, 90, 365].includes(s.keepChatsDays)}<option value={s.keepChatsDays}>{s.keepChatsDays} days</option>{/if}
          </select>
        </div>
        <div class="row">
          <div class="text"><span>Clear all chats</span></div>
          <button type="button" class="btn destructive" disabled={prefs.clearing || app.streaming}
            title={app.streaming ? "Stop the answer before clearing history" : "Clear all chats"}
            onclick={() => prefs.clearAll()}>{prefs.clearing ? "Clearing..." : prefs.confirmClear ? "Click again to clear" : "Clear"}</button>
        </div>
      </div>
    </section>
  {/if}
</div>

<style>
  .tool-folder { min-width: 0; }
  .tool-folder-row { gap: 8px; }
  .no-tool-folders { padding: 12px 14px; }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 48px;
    padding: 0 10px;
    border-bottom: 1px solid var(--border);
  }
  h1 {
    flex: 1;
    margin: 0;
    text-align: center;
    font-size: 15px;
    font-weight: 700;
  }
  .status {
    width: 90px;
    text-align: right;
    font-size: 12px;
    color: var(--dim);
  }
  .scroll {
    max-height: 600px;
    overflow-y: auto;
    padding: 20px 32px 28px;
    display: flex;
    flex-direction: column;
    gap: 24px;
  }
  .scroll .error {
    margin: 0;
  }
  section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 700;
  }
  .desc {
    margin: 0 0 2px;
    font-size: 13px;
    line-height: 1.45;
    color: var(--dim);
  }
  .desc code,
  .mono {
    font-family: var(--mono);
    font-size: 12px;
  }
  .list {
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--card);
    overflow: hidden;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 56px;
    padding: 8px 12px 8px 14px;
  }
  .row + .row {
    border-top: 1px solid var(--border);
  }
  .row.tall {
    border-top: none;
    padding-top: 0;
  }
  .text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .text small {
    font-size: 12px;
    color: var(--dim);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .num {
    width: 22px;
    height: 22px;
    border-radius: 11px;
    background: var(--hover);
    font-size: 12px;
    font-weight: 700;
    display: grid;
    place-items: center;
    flex: none;
  }
  .field {
    width: 260px;
    height: 34px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--view);
    font-size: 13px;
    user-select: text;
  }
  .voice-advanced { padding: 10px 14px; }
  .voice-advanced summary { cursor: pointer; color: var(--dim); font-size: 12px; }
  select.field { color: var(--fg); }
  .field.wide {
    flex: 1;
    width: auto;
  }
  .field.narrow {
    width: 120px;
  }
  textarea {
    width: 100%;
    resize: vertical;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--view);
    font-size: 13px;
    line-height: 1.45;
    user-select: text;
  }
  .keys {
    display: inline-flex;
    gap: 4px;
  }
  .keys kbd {
    font-size: 12px;
    padding: 4px 8px;
    border-radius: 6px;
  }
  .icon-btn:disabled,
  .btn:disabled {
    opacity: 0.4;
    cursor: default;
  }
</style>
