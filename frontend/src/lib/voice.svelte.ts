import type { VoiceLevelEvent } from "./types";

interface VoiceActions {
    start(recordingId: string): Promise<void>;
    onLevel(handler: (event: VoiceLevelEvent) => void): () => void;
    stop(): Promise<string>;
    cancel(): Promise<void>;
    send(text: string): Promise<void>;
}

// A replacement view must wait until the previous session has fully stopped.
let cleanup: Promise<void> = Promise.resolve();
let nextRecording = 0;
const emptyLevels = () => Array<number>(56).fill(0);
export class VoiceSession {
    phase = $state<"starting" | "listening" | "transcribing" | "review" | "sending" | "error">("starting");
    seconds = $state(0);
    transcript = $state("");
    problem = $state("");
    level = $state(0);
    levelAvailable = $state(false);
    levels = $state<number[]>(emptyLevels());

    private version = 0;
    private disposed = false;
    private timer: ReturnType<typeof setInterval> | undefined;
    private pending: Promise<void> | null = null;
    private closing: Promise<void> | null = null;
    private offLevel: (() => void) | undefined;

    constructor(private actions: VoiceActions) {}

    start(): Promise<void> {
        if (this.disposed || this.pending) return this.pending ?? Promise.resolve();
        const version = ++this.version;
        const previous = cleanup;
        this.phase = "starting";
        this.problem = "";
        this.transcript = "";
        this.seconds = 0;
        this.stopLevels();
        const pending = (async () => {
            try {
                await previous;
                if (this.disposed || version !== this.version) return;
                const recordingId = `voice-${Date.now()}-${++nextRecording}`;
                this.offLevel = this.actions.onLevel((event) => {
                    if (this.disposed || version !== this.version || event.recordingId !== recordingId || !["starting", "listening"].includes(this.phase)) return;
                    const valid = event.available && Number.isFinite(event.level) && event.level >= 0;
                    const raw = valid ? Math.min(event.level, 1) : 0;
                    // A -60 dB floor makes ordinary speech visible without
                    // drawing activity for digital silence.
                    this.level = raw > 0 ? Math.max(0, (20 * Math.log10(raw) + 60) / 60) : 0;
                    this.levelAvailable = valid;
                    this.levels = [...this.levels.slice(1), this.level];
                });
                await this.actions.start(recordingId);
                if (this.disposed || version !== this.version) return;
                this.phase = "listening";
                const started = Date.now();
                this.timer = setInterval(() => { this.seconds = Math.floor((Date.now() - started) / 1000); }, 1000);
            } catch (e) {
                if (this.disposed || version !== this.version) return;
                this.stopLevels();
                await this.actions.cancel().catch(() => {});
                if (this.disposed || version !== this.version) return;
                this.problem = e instanceof Error ? e.message : String(e);
                this.phase = "error";
            }
        })();
        this.pending = pending;
        void pending.finally(() => { if (this.pending === pending) this.pending = null; });
        return pending;
    }

    stop(mode: "send" | "edit"): Promise<void> {
        if (this.phase !== "listening" || this.pending || this.disposed) return Promise.resolve();
        clearInterval(this.timer);
        this.stopLevels();
        this.phase = "transcribing";
        const version = this.version;
        const pending = (async () => {
            try {
                const text = await this.actions.stop();
                if (this.disposed || version !== this.version) return;
                this.transcript = text.trim();
                if (!this.transcript) throw new Error("Didn't catch anything. Try recording again.");
                this.phase = "review";
                if (mode === "send") await this.deliver(version);
            } catch (e) {
                if (this.disposed || version !== this.version) return;
                await this.actions.cancel().catch(() => {});
                if (this.disposed || version !== this.version) return;
                this.problem = e instanceof Error ? e.message : String(e);
                this.phase = "error";
            }
        })();
        this.pending = pending;
        void pending.finally(() => { if (this.pending === pending) this.pending = null; });
        return pending;
    }

    send(): Promise<void> {
        if (this.phase !== "review" || this.pending || this.disposed || !this.transcript.trim()) return Promise.resolve();
        const pending = this.deliver(this.version);
        this.pending = pending;
        void pending.finally(() => { if (this.pending === pending) this.pending = null; });
        return pending;
    }

    private async deliver(version: number) {
        this.phase = "sending";
        this.problem = "";
        try {
            await this.actions.send(this.transcript.trim());
        } catch (e) {
            if (this.disposed || version !== this.version) return;
            this.problem = e instanceof Error ? e.message : String(e);
        } finally {
            if (!this.disposed && version === this.version) this.phase = "review";
        }
    }

    dispose(): Promise<void> {
        if (this.closing) return this.closing;
        this.disposed = true;
        this.version++;
        clearInterval(this.timer);
        this.stopLevels();
        const pending = this.pending;
        const cancel = this.actions.cancel().catch(() => {});
        this.closing = Promise.all([pending, cancel]).then(() => this.actions.cancel()).catch(() => {});
        cleanup = this.closing;
        return this.closing;
    }

    private stopLevels() {
        this.offLevel?.();
        this.offLevel = undefined;
        this.level = 0;
        this.levelAvailable = false;
        this.levels = emptyLevels();
    }
}
