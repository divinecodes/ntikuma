import {
    chatApi,
    contextApi,
    errorText,
    events,
    historyApi,
    popupApi,
    prefsApi
} from "./api";
import type {ApprovalEvent, Attachment, Chat, DeltaEvent, DoneEvent, ErrorEvent, SelectionCapture, SelectionSupport, Settings, ToolActivity, ToolEvent, UIMessage,View} from "./types";

// How long a chat stays "current" after popup hides
const RESUME_WINDOW_MS = 10 * 60 * 1000;
// Sent by Continue. It shows in the chat as the user's message.
const continuePrompt = "Continue your last answer from exactly where it stopped, without repeating what you already wrote.";

class AppState {
    view = $state<View>("bar");
    returnTo = $state<"bar" | "chat">("bar");
    voiceSession = $state(0);

    settings = $state<Settings | null> (null);
    hasKey = $state(true);

    //Composedr
    input = $state("");
    attachments = $state<Attachment[]>([]);
    autoAttach = $state(false);
    screenshotPending = $state(false);
    selectionSupport = $state<SelectionSupport | null>(null);
    selectionPending = $state(false);
    selectionMessage = $state("");
    clipboardMessage = $state("");
    modelOverride = $state("");
    web = $state(true);
    //bumped whenever the focused view should focus its main input
    focusTick = $state(0);

    //current chat
    chatId = $state("");
    chatTitle = $state("");
    messages = $state<UIMessage[]>([]);
    streaming = $state(false);
    answerWeb = $state(false);
    private approvalRequests = $state<ApprovalEvent[]>([]);
    private decidingApproval = $state("");
    approvalError = $state("");

    get approval() {
        return this.approvalRequests.find((a) => a.chatId === this.chatId &&
            this.messages.some((m) => m.id === a.messageId && m.role === "assistant" && m.streaming));
    }

    get approvalBusy() {
        return Boolean(this.approval && this.decidingApproval === this.approvalKey(this.approval));
    }

    get searchingWeb() {
        return this.streaming && this.answerWeb && !this.messages.some((m) => m.streaming && m.content);
    }

    get toolHint() {
        const answer = this.messages.find((m) => m.streaming && m.role === "assistant");
        const running = answer?.activity?.find((a) => a.status === "started" || a.status === "waiting");
        return running?.status === "waiting" ? "Waiting for command approval…" : running ? `${running.summary}…` : "";
    }

    toggleWeb() {
        this.web = !this.web;
    }

    recent = $state<Chat[]>([]);
    error = $state("");

    private lastActivity = 0;
    private clipboardVersion = 0;
    private chatVersion = 0;
    // Chat loads must not overwrite a newer request or user navigation.
    private navigationVersion = 0;
    private screenshotVersion = 0;
    private selectionVersion = 0;
    private selectionOnFocus = false;
    private preparedSelection: SelectionCapture | undefined;
    private dismissedSelection = false;
    private stopPending = false;
    private focused = false;
    private captureOnFocus = false;
    //clipboard chips the user removed this session;
    private dismissedClipboard = false;
    //Deltas that arrived before Send() returned the message id
    private early = new Map<string, string>();
    private earlyTerminal = new Map<string, DoneEvent | ErrorEvent>();
    private earlyTools = new Map<string, ToolActivity[]>();
    private pendingSend = false;
    private closedApprovals = new Set<string>();

    async init() {
        events.open(({ mode, selection }) => this.onOpen(mode, selection));
        events.focus(() => this.onFocus());
        events.hidden(() => this.onHidden());
        events.delta((d) => this.onDelta(d));
        events.done((d) => this.onTerminal(d));
        events.error((d) => this.onTerminal(d));
        events.tool((d) => this.onTool(d));
        events.approval((d) => this.onApproval(d));

        await this.reloadSettings();
        await this.loadSelectionSupport();
        this.captureWhenFocused();
        await this.refreshRecent();
    }

    private async loadSelectionSupport() {
        try {
            this.selectionSupport = await contextApi.selectionStatus();
        } catch (e) {
            this.selectionSupport = { available: false, beforeFocus: false, message: errorText(e) };
        }
        this.captureWhenFocused();
    }

    async reloadSettings() {
        this.error = "";
        try{
            this.applySettings(await prefsApi.get());
            this.hasKey = await prefsApi.hasKey();
        } catch(e){
            this.error = errorText(e);
        }
    }

    async refreshRecent() {
        try {
            this.recent = await historyApi.recent(5)
        } catch {}
    }

    applySettings(settings: Settings) {
        this.settings = settings;
        if (!settings.localTools || !settings.allowShellCommands) this.clearApprovals();
        this.autoAttach = settings.attachClipboard;
        if (!settings.attachSelection) {
            this.invalidateSelection();
            this.attachments = this.attachments.filter((a) => a.kind !== "selection");
        }
        if (!settings.offerScreenshot) {
            this.invalidateScreenshot();
            this.attachments = this.attachments.filter((a) => a.kind !== "screenshot");
        }
        if (!this.autoAttach) {
            this.clipboardVersion++;
            this.captureOnFocus = false;
            this.clipboardMessage = "";
            this.attachments = this.attachments.filter((a) => a.kind !== "clipboard");
        }
    }

    async historyCleared() {
        this.resetChat();
        this.returnTo = "bar";
        if (this.view === "chat") this.view = "bar";
        await this.refreshRecent();
    }

    // ---- Popup lifecyle ---
    private onOpen(mode: View, selection?: SelectionCapture) {
        // Capture restoration must retain a draft even before it has a chat ID.
        if (this.screenshotPending && mode === "bar") return;
        this.navigationVersion++;
        if (!["bar", "chat", "voice", "history", "settings"].includes(mode)) mode = "bar";
        this.error = "";
        if(mode === "bar"){
            const fresh = Date.now() - this.lastActivity < RESUME_WINDOW_MS;
            if(this.chatId && (this.streaming || fresh)){
                this.view = "chat";
            } else {
                this.resetChat();
                this.view = "bar";
                this.captureOnFocus = this.autoAttach;
                this.selectionOnFocus = true;
                this.preparedSelection = selection;
            }
            void this.refreshRecent();
        } else {
            if (mode === "voice" && this.view !== "chat" && !this.streaming){
                //Dictating from a hidden popup starts fresh questions
                const fresh = Date.now() - this.lastActivity < RESUME_WINDOW_MS;
                if(!this.chatId || !fresh){
                    this.resetChat();
                    this.view = "bar";
                    this.captureOnFocus = this.autoAttach;
                    this.selectionOnFocus = true;
                    this.preparedSelection = selection;
                }
            }
            this.goTo(mode);
        }
        this.focusTick++;
        this.captureWhenFocused();
    }

    private onFocus() {
        this.focused = true;
        this.focusTick++;
        this.captureWhenFocused();
    }

    private captureWhenFocused() {
        if (!this.focused || !this.settings) return;
        if (this.captureOnFocus) {
            this.captureOnFocus = false;
            if (this.autoAttach) void this.captureClipboard();
        }
        if (this.selectionOnFocus && this.selectionSupport) {
            this.selectionOnFocus = false;
            if (this.settings.attachSelection) void this.captureSelection();
        }
    }

    private onHidden() {
        this.navigationVersion++;
        this.focused = false;
        this.captureOnFocus = false;
        this.invalidateSelection();
        this.lastActivity = Math.max(this.lastActivity, Date.now());
    }

    hide() {
        this.navigationVersion++;
        this.invalidateScreenshot();
        this.invalidateSelection();
        void popupApi.hide();
    }

    // ---- Navigation ----
    goTo(view: View) {
        this.navigationVersion++;
        if(this.view === "bar" || this.view === "chat") this.returnTo = this.view;
        if (view === "voice") this.voiceSession++;
        this.view = view;
        this.focusTick++;
    }

    back() {
        this.navigationVersion++;
        this.view = this.returnTo;
        this.focusTick++;
    }

    /** Esc: step back out of side views, otherwise hide. Streams keep going. */
    escape() {
        if (this.view === "chat" && this.approval) {
            if (!this.approvalBusy) void this.answerApproval(false);
            return;
        }
        if (this.view === "history" || this.view === "settings" || this.view === "voice"){
            this.back();
            return;
        }
        this.hide();
    }

    newChat() {
        this.resetChat();
        this.view = "bar";
        this.dismissedClipboard = false;
        this.dismissedSelection = false;
        this.focusTick++;
        if(this.autoAttach) void this.captureClipboard();
        if (this.settings?.attachSelection) void this.captureSelection();
    }

    private resetChat() {
        this.clearApprovals();
        this.closedApprovals.clear();
        this.navigationVersion++;
        this.invalidateScreenshot();
        this.invalidateSelection();
        this.chatVersion++;
        this.clipboardVersion++;
        this.stopPending = false;
        this.captureOnFocus = false;
        this.early.clear();
        this.earlyTerminal.clear();
        this.earlyTools.clear();
        this.pendingSend = false;
        this.chatId = "";
        this.chatTitle = "";
        this.messages = [];
        this.streaming = false;
        this.answerWeb = false;
        this.input = "";
        this.attachments = [];
        this.clipboardMessage = "";
        this.modelOverride = "";
    }


    async openChat(id: string) {
        const version = ++this.navigationVersion;
        this.error = "";
        // Stored history has no in-flight answer or unsent follow-up draft.
        // Keep the live conversation, including its terminal state, on return.
        if (id && id === this.chatId) {
            this.lastActivity = Date.now();
            this.view = "chat";
            this.focusTick++;
            return;
        }
        try{
            const detail = await historyApi.get(id);
            if (version !== this.navigationVersion) return;
            const messages = detail.messages
                .filter((m) => m.role !== "system")
                .map((m) => ({
                    id: m.id,
                    role: m.role as "user" | "assistant",
                    content: m.content,
                    model: m.model,
                    fallback: m.fallback,
                    attachments: m.attachments,
                    citations: m.citations,
                    searches: m.searches,
                    activity: m.activity,
                    stopped: m.stopped,
                    error: m.error,
                    cutOff: m.cutOff,
                }));
            this.resetChat();
            this.chatId = detail.chat.id;
            this.chatTitle = detail.chat.title;
            this.messages = messages;
            this.lastActivity = Date.now();
            this.view = "chat";
            this.focusTick++;
        } catch(e){
            if (version === this.navigationVersion) this.error = errorText(e);
        }
    }

    async forgetDeletedChat(id: string) {
        if (this.chatId === id) {
            this.resetChat();
            this.returnTo = "bar";
            if (this.view === "chat") this.view = "bar";
        }
        await this.refreshRecent();
    }

    // ---- Context ----
    // Automatic captures hold back secret-looking text (see AutoClipboard in
    // context.go). A manual capture is the user asking, so it always attaches.
    async captureClipboard(manual = false) {
        if (manual) this.dismissedClipboard = false;
        if (this.dismissedClipboard || this.attachments.some((a) => a.kind === "clipboard")) return;
        const version = ++this.clipboardVersion;
        this.clipboardMessage = "";
        try {
            const { attachment, message } = manual
                ? { attachment: await contextApi.clipboard(), message: "" }
                : await contextApi.autoClipboard();
            if (version !== this.clipboardVersion || this.dismissedClipboard) return;
            if (attachment) this.attachments.push(attachment);
            this.clipboardMessage = message;
        } catch(e) {
            if (version === this.clipboardVersion) this.error = errorText(e);
        }
    }

    async captureSelection(manual = false) {
        if (this.selectionPending || !this.settings?.attachSelection || this.attachments.some((a) => a.kind === "selection")) return;
        if (manual) this.dismissedSelection = false;
        if (this.dismissedSelection) return;
        if (!this.selectionSupport?.available) {
            this.selectionMessage = this.selectionSupport?.message ?? "Checking selected-text support…";
            return;
        }
        const version = ++this.selectionVersion;
        const prepared = this.preparedSelection;
        this.preparedSelection = undefined;
        this.selectionPending = true;
        this.selectionMessage = "";
        try {
            const result = prepared ?? await contextApi.selection();
            if (version !== this.selectionVersion || this.dismissedSelection) return;
            if (result.attachment) this.attachments.push(result.attachment);
            this.selectionMessage = result.message;
        } catch (e) {
            if (version === this.selectionVersion) this.selectionMessage = errorText(e);
        } finally {
            this.selectionPending = false;
        }
    }

    private invalidateSelection() {
        this.selectionVersion++;
        this.selectionOnFocus = false;
        this.preparedSelection = undefined;
        this.selectionMessage = "";
    }

    async addScreenshot() {
        if (this.screenshotPending || !this.settings?.offerScreenshot || this.attachments.some((a) => a.kind === "screenshot")) return;
        const version = ++this.screenshotVersion;
        this.screenshotPending = true;
        this.error = "";
        try{
            const a = await contextApi.screenshot();
            if (version === this.screenshotVersion && a) this.attachments.push(a);
        } catch(e){
            if (version === this.screenshotVersion) this.error = errorText(e);
        } finally {
            this.screenshotPending = false;
            this.focusTick++;
        }
    }

    private invalidateScreenshot() {
        this.screenshotVersion++;
        if (this.screenshotPending) void contextApi.cancelScreenshot().catch(() => {});
    }

    removeAttachment(i: number) {
        if (this.attachments[i]?.kind === "selection") {
            this.dismissedSelection = true;
            this.invalidateSelection();
        }
        if (this.attachments[i]?.kind === "clipboard") {
            this.dismissedClipboard = true;
            this.clipboardVersion++;
        }
        this.attachments.splice(i, 1);
    }

    toggleAutoAttach() {
        this.clipboardVersion++;
        this.autoAttach = !this.autoAttach;
        if(!this.autoAttach){
            this.clipboardMessage = "";
            this.attachments = this.attachments.filter((a) => a.kind !== "clipboard");
        } else {
            this.dismissedClipboard = false;
            void this.captureClipboard();
        }
    }

    // ---- Chat ----
    async send(textArgs?: string){
        const text = (textArgs ?? this.input).trim();
        if (this.screenshotPending || this.selectionPending) return;
        await this.submit(text, true);
    }

    /** The last answer stopped early with some text, and nothing is answering. */
    get canContinue(): boolean {
        const last = this.messages[this.messages.length - 1];
        return !this.streaming && last?.role === "assistant" && !last.streaming && !!last.cutOff;
    }

    /** Asks the model to pick up a cut-off answer. The draft and chips stay for the next question. */
    async continueAnswer() {
        if (this.canContinue) await this.submit(continuePrompt, false);
    }

    // composer: the text and chips come from the composer, which is cleared on
    // send and restored if the send fails.
    private async submit(text: string, composer: boolean){
        if(!text || this.streaming) return;
        this.error = "";
        if (!this.hasKey){
            this.error = "Add your OpenRouter key in Preferences first";
            this.goTo("settings");
            return;
        }

        const attachments = composer ? $state.snapshot(this.attachments) as Attachment[] : [];
        const web = this.web;
        this.answerWeb = web;
        const version = this.chatVersion;
        this.navigationVersion++;
        this.stopPending = false;
        this.messages.push({id: `local-${Date.now()}`, role: "user", content: text, attachments: attachments });
        if (composer) {
            this.clipboardVersion++;
            this.invalidateSelection();
            this.input = "";
            this.attachments = [];
            this.clipboardMessage = "";
        }
        this.view = "chat";
        this.streaming = true;
        this.pendingSend = true;

        try {
            const  res = await  chatApi.send({
                chatId: this.chatId, text, attachments, model: this.modelOverride, web,
            });
            if (version !== this.chatVersion) return;
            this.pendingSend = false;
            if (!this.chatId){
                this.chatId = res.chatId;
                this.chatTitle = res.title;
            }
            this.messages.push({
                id: res.messageId,
                role: "assistant",
                content: this.early.get(res.messageId) ?? "",
                activity: this.earlyTools.get(res.messageId),
                streaming: true
            });
            this.early.delete(res.messageId);
            const terminal = this.earlyTerminal.get(res.messageId);
            if (terminal) {
                this.earlyTerminal.delete(res.messageId);
                this.onTerminal(terminal);
            }
            this.early.clear();
            this.earlyTerminal.clear();
            this.earlyTools.clear();
            this.clearApprovals(res.messageId);
            if (this.stopPending && this.streaming) void chatApi.stop(res.chatId);
            this.lastActivity = Date.now();
        } catch(e){
            if (version !== this.chatVersion) return;
            this.pendingSend = false;
            this.early.clear();
            this.earlyTerminal.clear();
            this.earlyTools.clear();
            this.clearApprovals();
            this.streaming = false;
            this.answerWeb = false;
            this.error = errorText(e);
            this.messages.pop();
            //put the text back so nothing typed is lost
            if (composer) {
                this.input = text;
                this.attachments = attachments;
            }
            if(!this.messages.length) this.view = "bar";
        }
    }

    stop() {
        if (!this.streaming) return;
        this.stopPending = true;
        if (this.chatId) void chatApi.stop(this.chatId);
    }

    private onTerminal(d: DoneEvent | ErrorEvent) {
        const m = this.messages.find((x) => x.id === d.messageId);
        if (!m) {
            if (this.pendingSend && this.earlyTerminal.size < 8) this.earlyTerminal.set(d.messageId, d);
            void this.refreshRecent();
            return;
        }
        if (d.chatId !== this.chatId || m.role !== "assistant" || !m.streaming) return;
        this.clearApprovals(undefined, d.messageId);
        m.streaming = false;
        m.citations = d.citations;
        m.searches = d.searches;
        m.activity = d.activity ?? m.activity?.map((a) => a.status === "started" || a.status === "waiting" ?
            { ...a, status: "stopped" in d && d.stopped ? "stopped" : "error" } : a);
        m.model = d.model;
        m.fallback = d.fallback;
        if ("error" in d) {
            m.error = d.error;
        } else {
            m.stopped = d.stopped;
        }
        m.cutOff = d.cutOff ?? false;
        if (d.chatId === this.chatId) {
            this.streaming = false;
            this.answerWeb = false;
        }
        this.lastActivity = Date.now();
        void this.refreshRecent();
    }

    private onDelta(d: DeltaEvent){
        const m = this.messages.find((x) => x.id === d.messageId);
        if (m && m.streaming && d.chatId === this.chatId){
            m.content += d.text;
        } else if (!m && this.pendingSend && (this.early.has(d.messageId) || this.early.size < 8)) {
            this.early.set(d.messageId, (this.early.get(d.messageId) ?? "") + d.text);
        }
    }

    private onTool(d: ToolEvent) {
        const m = this.messages.find((x) => x.id === d.messageId);
        if (m) {
            if (!m.streaming || m.role !== "assistant" || d.chatId !== this.chatId) return;
            m.activity = this.mergeActivity(m.activity ?? [], d.activity);
        } else if (this.pendingSend && (this.earlyTools.has(d.messageId) || this.earlyTools.size < 8)) {
            this.earlyTools.set(d.messageId, this.mergeActivity(this.earlyTools.get(d.messageId) ?? [], d.activity));
        }
    }

    private mergeActivity(current: ToolActivity[], next: ToolActivity) {
        const index = current.findIndex((a) => a.callId === next.callId);
        if (index < 0) return current.length < 24 ? [...current, next] : current;
        if (current[index].status !== "started" && current[index].status !== "waiting") return current;
        return current.map((a, i) => i === index ? next : a);
    }

    private approvalKey(a: ApprovalEvent) {
        return JSON.stringify([a.chatId, a.messageId, a.callId]);
    }

    private decline(a: ApprovalEvent) {
        void chatApi.approve(a.chatId, a.messageId, a.callId, false).catch(() => {});
    }

    private clearApprovals(keepMessage?: string, removeMessage?: string) {
        this.approvalRequests = this.approvalRequests.filter((a) => {
            const remove = removeMessage ? a.messageId === removeMessage && a.chatId === this.chatId :
                a.messageId !== keepMessage || a.chatId !== this.chatId;
            if (remove) this.decline(a);
            return !remove;
        });
        if (!this.approval) { this.approvalError = ""; this.decidingApproval = ""; }
    }

    private onApproval(d: ApprovalEvent) {
        const key = this.approvalKey(d);
        if (d.state === "closed") {
            this.closedApprovals.add(key);
            if (this.closedApprovals.size > 128) this.closedApprovals.delete(this.closedApprovals.values().next().value!);
            const current = this.approval && this.approvalKey(this.approval) === key;
            this.approvalRequests = this.approvalRequests.filter((a) => this.approvalKey(a) !== key);
            if (current) { this.approvalError = ""; this.decidingApproval = ""; this.focusTick++; }
            return;
        }
        if (this.closedApprovals.has(key)) return;
        const message = this.messages.find((m) => m.id === d.messageId);
        if (message ? !message.streaming || message.role !== "assistant" || d.chatId !== this.chatId : !this.pendingSend) {
            this.decline(d);
            return;
        }
        if (!this.approvalRequests.some((a) => this.approvalKey(a) === key)) {
            if (this.approvalRequests.length >= 8) { this.decline(d); return; }
            this.approvalRequests.push(d);
        }
    }

    async answerApproval(allow: boolean) {
        const approval = this.approval;
        if (!approval || this.approvalBusy) return;
        const key = this.approvalKey(approval);
        this.decidingApproval = key;
        this.approvalError = "";
        try {
            await chatApi.approve(approval.chatId, approval.messageId, approval.callId, allow);
        } catch (e) {
            if (this.approval && this.approvalKey(this.approval) === key) this.approvalError = errorText(e);
            if (this.decidingApproval === key) this.decidingApproval = "";
        }
    }
}

export const app = new AppState();
