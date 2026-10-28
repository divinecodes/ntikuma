import type { Chat, ChatDetail, SearchHit } from "./types";

interface HistoryActions {
    recent(limit: number): Promise<Chat[]>;
    search(query: string): Promise<SearchHit[]>;
    get(id: string): Promise<ChatDetail>;
    remove(id: string): Promise<void>;
    open(id: string): Promise<void>;
    deleted(id: string): Promise<void>;
}

const message = (e: unknown) => e instanceof Error ? e.message : String(e);

export class HistoryBrowser {
    query = $state("");
    hits = $state<SearchHit[]>([]);
    selected = $state(-1);
    detail = $state<ChatDetail | null>(null);
    loading = $state(true);
    previewLoading = $state(false);
    error = $state("");
    previewError = $state("");
    deleting = $state("");
    opening = $state(false);

    private searchVersion = 0;
    private previewVersion = 0;
    private disposed = false;
    private timer: ReturnType<typeof setTimeout> | undefined;

    constructor(private actions: HistoryActions) {}

    search(query: string, delay = 150) {
        this.query = query;
        clearTimeout(this.timer);
        const version = ++this.searchVersion;
        this.previewVersion++;
        this.hits = [];
        this.selected = -1;
        this.detail = null;
        this.previewLoading = false;
        this.previewError = "";
        this.error = "";
        this.loading = true;
        this.timer = setTimeout(() => { void this.load(version, query.trim()); }, delay);
    }

    private async load(version: number, query: string) {
        try {
            const hits = query
                ? await this.actions.search(query)
                : (await this.actions.recent(50)).map((chat) => ({ chat, snippet: "" }));
            if (this.disposed || version !== this.searchVersion) return;
            this.hits = hits;
            this.loading = false;
            if (hits.length) void this.select(0);
        } catch (e) {
            if (this.disposed || version !== this.searchVersion) return;
            this.error = message(e);
            this.loading = false;
        }
    }

    async select(index: number) {
        const hit = this.hits[index];
        if (!hit || this.loading || this.disposed) return;
        this.selected = index;
        this.detail = null;
        this.previewError = "";
        this.previewLoading = true;
        const version = ++this.previewVersion;
        try {
            const detail = await this.actions.get(hit.chat.id);
            if (this.disposed || version !== this.previewVersion) return;
            this.detail = detail;
        } catch (e) {
            if (this.disposed || version !== this.previewVersion) return;
            this.previewError = message(e);
        } finally {
            if (!this.disposed && version === this.previewVersion) this.previewLoading = false;
        }
    }

    async openSelected() {
        const hit = this.hits[this.selected];
        if (!hit || this.loading || this.opening || this.deleting === hit.chat.id) return;
        this.opening = true;
        this.error = "";
        try {
            await this.actions.open(hit.chat.id);
        } catch (e) {
            if (!this.disposed) this.error = message(e);
        } finally {
            if (!this.disposed) this.opening = false;
        }
    }

    async removeSelected() {
        const hit = this.hits[this.selected];
        if (!hit || this.loading || this.deleting || this.opening) return;
        const version = this.searchVersion;
        this.deleting = hit.chat.id;
        this.error = "";
        try {
            await this.actions.remove(hit.chat.id);
            await this.actions.deleted(hit.chat.id);
            if (this.disposed) return;
            if (version !== this.searchVersion) {
                this.search(this.query, 0);
                return;
            }
            const selectedID = this.hits[this.selected]?.chat.id;
            this.hits = this.hits.filter((h) => h.chat.id !== hit.chat.id);
            if (selectedID !== hit.chat.id) {
                this.selected = this.hits.findIndex((h) => h.chat.id === selectedID);
            } else if (this.hits.length) {
                void this.select(Math.min(this.selected, this.hits.length - 1));
            } else {
                this.previewVersion++;
                this.selected = -1;
                this.detail = null;
                this.previewLoading = false;
                this.previewError = "";
            }
        } catch (e) {
            if (!this.disposed) this.error = message(e);
        } finally {
            if (!this.disposed) this.deleting = "";
        }
    }

    keydown(key: string): boolean {
        if (this.loading || this.opening || !this.hits.length) return false;
        if (key === "ArrowDown") {
            void this.select(Math.min(this.selected + 1, this.hits.length - 1));
        } else if (key === "ArrowUp") {
            void this.select(Math.max(this.selected - 1, 0));
        } else if (key === "Enter") {
            void this.openSelected();
        } else if (key === "Delete" && !this.query) {
            void this.removeSelected();
        } else {
            return false;
        }
        return true;
    }

    dispose() {
        this.disposed = true;
        clearTimeout(this.timer);
        this.searchVersion++;
        this.previewVersion++;
    }
}
