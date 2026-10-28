import type { ModelInfo, Settings, VoiceSettings, VoiceReadiness } from "./types";

interface PreferenceActions {
    get(): Promise<Settings>;
    save(settings: Settings): Promise<void>;
    models(): Promise<ModelInfo[]>;
    hasKey(): Promise<boolean>;
    setKey(key: string): Promise<void>;
    saved(settings: Settings): void;
    keyChanged(hasKey: boolean): void;
    clearHistory(): Promise<void>;
    cleared(): Promise<void>;
    canClear(): boolean;
    checkVoice?(settings: VoiceSettings): Promise<VoiceReadiness>;
    pickVoiceFolder?(engine: string, current: string): Promise<string>;
    pickToolFolder?(current: string): Promise<string>;
}

const message = (e: unknown) => e instanceof Error ? e.message : String(e);

export class Preferences {
    settings = $state<Settings | null>(null);
    models = $state<ModelInfo[]>([]);
    loading = $state(true);
    loadError = $state("");
    modelsLoading = $state(false);
    modelsError = $state("");
    error = $state("");
    status = $state("");
    saving = $state(false);
    hasKey = $state(false);
    keyInput = $state("");
    keySaving = $state(false);
    keyError = $state("");
    confirmClear = $state(false);
    clearing = $state(false);
    voiceReadiness = $state<VoiceReadiness | null>(null);
    voiceError = $state("");
    voiceChecking = $state(false);
    voicePicking = $state(false);
    toolPicking = $state(false);
    toolError = $state("");

    private version = 0;
    private savedVersion = 0;
    private modelVersion = 0;
    private loadVersion = 0;
    private disposed = false;
    private voiceVersion = 0;
    private toolVersion = 0;
    private toolPicker = 0;
    private saveTimer: ReturnType<typeof setTimeout> | undefined;
    private clearTimer: ReturnType<typeof setTimeout> | undefined;
    private activeSave: Promise<void> | null = null;

    constructor(private actions: PreferenceActions) {}

    async load() {
        this.toolVersion++;
        this.toolPicker++;
        this.toolPicking = false;
        this.toolError = "";
        this.voiceVersion++;
        this.voiceReadiness = null;
        this.voiceError = "";
        this.voiceChecking = false;
        const version = ++this.loadVersion;
        this.loading = true;
        this.loadError = "";
        try {
            const [settings, hasKey] = await Promise.all([this.actions.get(), this.actions.hasKey()]);
            if (this.disposed || version !== this.loadVersion) return;
            this.settings = { ...settings, fallbacks: settings.fallbacks ?? [],
                localTools: settings.localTools ?? false,
                toolRoots: [...(settings.toolRoots ?? [])],
                allowShellCommands: settings.allowShellCommands ?? false,
                voice: {
                    ...settings.voice,
                    engine: settings.voice.engine || "whisper-server",
                    nativeHelper: settings.voice.nativeHelper ?? "",
                    qwen: { modelDir: settings.voice.qwen?.modelDir ?? "", threads: settings.voice.qwen?.threads ?? 4 },
                    parakeet: { modelDir: settings.voice.parakeet?.modelDir ?? "", threads: settings.voice.parakeet?.threads ?? 4 },
                    cohere: { modelDir: settings.voice.cohere?.modelDir ?? "", threads: settings.voice.cohere?.threads ?? 4, language: settings.voice.cohere?.language ?? "en" },
                },
            };
            this.hasKey = hasKey;
            this.actions.saved($state.snapshot(this.settings));
            this.actions.keyChanged(hasKey);
        } catch (e) {
            if (!this.disposed && version === this.loadVersion) this.loadError = message(e);
        } finally {
            if (!this.disposed && version === this.loadVersion) this.loading = false;
        }
    }

    async loadModels() {
        const version = ++this.modelVersion;
        this.modelsLoading = true;
        this.modelsError = "";
        try {
            const models = await this.actions.models();
            if (!this.disposed && version === this.modelVersion) this.models = models;
        } catch (e) {
            if (!this.disposed && version === this.modelVersion) this.modelsError = message(e);
        } finally {
            if (!this.disposed && version === this.modelVersion) this.modelsLoading = false;
        }
    }

    changed(delay = 300) {
        if (!this.settings) return;
        this.version++;
        this.status = "Unsaved changes";
        this.error = "";
        clearTimeout(this.saveTimer);
        this.saveTimer = setTimeout(() => { void this.flush(); }, delay);
    }

    private validate() {
        const s = this.settings;
        if (!s || !s.defaultModel.trim()) return "Pick a default model.";
        if (s.fallbacks.length > 3) return "Keep it to three fallbacks at most.";
        if (!Number.isInteger(s.maxContextChars) || s.maxContextChars < 0) return "Enter a whole text context limit of zero or more.";
        if (!Number.isInteger(s.keepChatsDays) || s.keepChatsDays < 0) return "Enter a valid retention period.";
        for (const profile of [s.voice.qwen, s.voice.parakeet, s.voice.cohere]) {
            if (!Number.isInteger(profile.threads) || profile.threads < 1 || profile.threads > 64) return "Enter between 1 and 64 whole CPU threads.";
        }
        if (!["en", "fr", "de", "it", "es", "pt", "el", "nl", "pl", "zh", "ja", "ko", "vi", "ar"].includes(s.voice.cohere.language)) return "Choose a supported Cohere language.";
        if (s.toolRoots.length > 32) return "Keep at most 32 tool folders.";
        for (const path of s.toolRoots) {
            const error = this.validateToolRoot(path);
            if (error) return error;
        }
        return "";
    }

    private validateToolRoot(path: string) {
        if (typeof path !== "string" || !path.trim() || path.includes("\0") || new TextEncoder().encode(path).length > 4096) return "Enter a nonempty tool folder path of at most 4096 bytes.";
        if (!path.startsWith("/") && !path.startsWith("~/")) return "Use an absolute tool folder path or ~/ path.";
        return "";
    }

    toolSettingsChanged() {
        this.toolVersion++;
        this.toolError = "";
        this.changed();
    }

    addToolRoot(path: string) {
        if (!this.settings) return false;
        this.toolError = this.validateToolRoot(path);
        if (this.toolError) return false;
        if (this.settings.toolRoots.includes(path)) { this.toolError = "That tool folder is already in the list."; return false; }
        if (this.settings.toolRoots.length >= 32) { this.toolError = "Keep at most 32 tool folders."; return false; }
        this.settings.toolRoots.push(path);
        this.toolSettingsChanged();
        return true;
    }

    removeToolRoot(index: number) {
        if (!this.settings || index < 0 || index >= this.settings.toolRoots.length) return;
        this.settings.toolRoots.splice(index, 1);
        this.toolSettingsChanged();
    }

    async pickToolFolder(index?: number) {
        if (!this.settings || !this.actions.pickToolFolder || this.toolPicking || this.voicePicking) return;
        const settings = this.settings;
        if (index !== undefined && (index < 0 || index >= settings.toolRoots.length)) return;
        const version = ++this.toolVersion;
        const picker = ++this.toolPicker;
        this.toolPicking = true;
        this.toolError = "";
        try {
            const path = await this.actions.pickToolFolder(index === undefined ? "" : settings.toolRoots[index]);
            if (!path || this.disposed || version !== this.toolVersion || this.settings !== settings) return;
            if (index === undefined) { this.addToolRoot(path); return; }
            this.toolError = this.validateToolRoot(path);
            if (this.toolError) return;
            if (settings.toolRoots.some((root, i) => i !== index && root === path)) { this.toolError = "That tool folder is already in the list."; return; }
            settings.toolRoots[index] = path;
            this.toolSettingsChanged();
        } catch (e) {
            if (!this.disposed && version === this.toolVersion) this.toolError = message(e);
        } finally {
            if (!this.disposed && picker === this.toolPicker) this.toolPicking = false;
        }
    }

    voiceChanged() {
        this.voiceVersion++;
        this.voiceChecking = false;
        this.voiceReadiness = null;
        this.voiceError = "";
        this.changed();
    }

    async checkVoice() {
        if (!this.settings || !this.actions.checkVoice) return;
        const version = ++this.voiceVersion;
        const draft = $state.snapshot(this.settings.voice);
        this.voiceChecking = true;
        this.voiceReadiness = null;
        this.voiceError = "";
        try {
            const result = await this.actions.checkVoice(draft);
            if (!this.disposed && version === this.voiceVersion) this.voiceReadiness = result;
        } catch (e) {
            if (!this.disposed && version === this.voiceVersion) this.voiceError = message(e);
        } finally {
            if (!this.disposed && version === this.voiceVersion) this.voiceChecking = false;
        }
    }

    async pickVoiceFolder() {
        if (!this.settings || !this.actions.pickVoiceFolder || this.voicePicking || this.toolPicking) return;
        const engine = this.settings.voice.engine;
        if (engine !== "qwen3-asr" && engine !== "parakeet" && engine !== "cohere-transcribe") return;
        const version = ++this.voiceVersion;
        this.voiceChecking = false;
        this.voiceReadiness = null;
        this.voiceError = "";
        this.voicePicking = true;
        const profile = engine === "qwen3-asr" ? this.settings.voice.qwen : engine === "cohere-transcribe" ? this.settings.voice.cohere : this.settings.voice.parakeet;
        try {
            const path = await this.actions.pickVoiceFolder(engine, profile.modelDir);
            if (path && !this.disposed && version === this.voiceVersion) {
                profile.modelDir = path;
                this.voiceChanged();
            }
        } catch (e) {
            if (!this.disposed && version === this.voiceVersion) this.voiceError = message(e);
        } finally {
            if (!this.disposed) this.voicePicking = false;
        }
    }

    flush(): Promise<void> {
        clearTimeout(this.saveTimer);
        if (this.activeSave) return this.activeSave;
        this.activeSave = this.drain().finally(() => { this.activeSave = null; });
        return this.activeSave;
    }

    private async drain() {
        while (this.settings && this.savedVersion < this.version) {
            this.error = this.validate();
            if (this.error) return;
            const version = this.version;
            const settings = $state.snapshot(this.settings);
            settings.defaultModel = settings.defaultModel.trim();
            this.saving = true;
            this.status = "Saving...";
            try {
                await this.actions.save(settings);
                this.actions.saved(settings);
                this.savedVersion = version;
                this.error = "";
                this.status = this.savedVersion === this.version ? "Saved" : "Unsaved changes";
            } catch (e) {
                this.error = message(e);
                this.status = "Unsaved changes";
                return;
            } finally {
                this.saving = false;
            }
        }
    }

    addFallback(value: string) {
        const model = value.trim();
        if (!this.settings || !model) return false;
        if (this.settings.fallbacks.length >= 3) { this.error = "Keep it to three fallbacks at most."; return false; }
        if (this.settings.fallbacks.includes(model)) { this.error = "That fallback is already in the list."; return false; }
        this.settings.fallbacks.push(model);
        this.changed();
        return true;
    }

    moveFallback(index: number, direction: -1 | 1) {
        const list = this.settings?.fallbacks;
        const target = index + direction;
        if (!list || index < 0 || index >= list.length || target < 0 || target >= list.length) return;
        [list[index], list[target]] = [list[target], list[index]];
        this.changed();
    }

    removeFallback(index: number) {
        if (!this.settings || index < 0 || index >= this.settings.fallbacks.length) return;
        this.settings.fallbacks.splice(index, 1);
        this.changed();
    }

    async saveKey() {
        if (this.keySaving || !this.keyInput.trim()) return;
        const key = this.keyInput.trim();
        this.keyInput = "";
        this.keySaving = true;
        this.keyError = "";
        try {
            await this.actions.setKey(key);
            this.hasKey = await this.actions.hasKey();
            this.actions.keyChanged(this.hasKey);
            this.status = "Key saved";
        } catch {
            this.keyError = "Couldn't save the API key. Check that your keyring is unlocked and try again.";
        } finally {
            this.keySaving = false;
        }
    }

    async clearAll() {
        if (this.clearing) return;
        if (!this.actions.canClear()) { this.error = "Stop the answer before clearing history."; return; }
        if (!this.confirmClear) {
            this.confirmClear = true;
            this.clearTimer = setTimeout(() => { this.confirmClear = false; }, 3000);
            return;
        }
        clearTimeout(this.clearTimer);
        this.confirmClear = false;
        this.clearing = true;
        this.error = "";
        try {
            await this.actions.clearHistory();
            await this.actions.cleared();
            this.status = "History cleared";
        } catch (e) {
            this.error = message(e);
        } finally {
            this.clearing = false;
        }
    }

    dispose() {
        this.disposed = true;
        this.loadVersion++;
        this.modelVersion++;
        this.voiceVersion++;
        this.toolVersion++;
        this.toolPicker++;
        clearTimeout(this.clearTimer);
        this.keyInput = "";
        void this.flush();
    }
}
