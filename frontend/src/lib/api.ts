// Calls go through Call.ByName with each method's fully qualified Go name
// ("<package path>.<Type>.<Method>"), so the app works without the binding generator.

import { Call, Events } from "@wailsio/runtime"
import type {
    Attachment,
    ClipboardCapture,
    ApprovalEvent,
    ChatDetail,
    Chat,
    Connection,
    DeltaEvent,
    DoneEvent,
    ErrorEvent,
    ModelInfo,
    ProviderStatus,
    SearchHit,
    SendRequest,
    SendResult,
    Settings,
    SelectionSupport,
    SelectionCapture,
    View,
    VoiceLevelEvent,
    VoiceSettings,
    VoiceReadiness,
    ToolEvent
} from "./types"

const services = "ntikuma/internal/services"
const call = <T>(method: string, ...args: unknown[]) => Call.ByName(method, ...args) as Promise<T>;

export const chatApi = {
    send: (req: SendRequest) => call<SendResult>(`${services}.ChatService.Send`, req),
    stop: (chatId: string) => call<void>(`${services}.ChatService.Stop`, chatId),
    approve: (chatId: string, messageId: string, callId: string, allow: boolean) => call<void>(`${services}.ChatService.Approve`, chatId, messageId, callId, allow),
}

export const historyApi = {
    recent: (limit: number) => call<Chat[] | null>(`${services}.HistoryService.Recent`, limit).then((r) => r ?? []),
    search: (q: string) => call<SearchHit[] | null>(`${services}.HistoryService.Search`, q).then((r) => r ?? []),
    get: (chatId: string) => call<ChatDetail>(`${services}.HistoryService.Get`, chatId),
    remove: (chatId: string) => call<void>(`${services}.HistoryService.Delete`, chatId),
    clear: () => call<void>(`${services}.HistoryService.Clear`),
};

export const prefsApi = {
    pickToolFolder: (current: string) => call<string>(`${services}.SettingsService.PickToolFolder`, current),
    get: () => call<Settings>(`${services}.SettingsService.Get`).then((s) => ({ ...s, fallbacks: s.fallbacks ?? [] })),
    save: (s: Settings) => call<void>(`${services}.SettingsService.Save`, s),
    hasKey: () => call<boolean>(`${services}.SettingsService.HasAPIKey`),
    providerStatus: () => call<ProviderStatus>(`${services}.SettingsService.ProviderStatus`),
    presets: () => call<Connection[] | null>(`${services}.SettingsService.ConnectionPresets`).then((r) => r ?? []),
    addConnection: (c: Connection) => call<Settings>(`${services}.SettingsService.AddConnection`, c),
    removeConnection: (id: string) => call<Settings>(`${services}.SettingsService.RemoveConnection`, id),
    hasConnectionKey: (id: string) => call<boolean>(`${services}.SettingsService.HasConnectionKey`, id),
    setConnectionKey: (id: string, key: string) => call<void>(`${services}.SettingsService.SetConnectionKey`, id, key),
    connectionModels: (id: string) => call<ModelInfo[] | null>(`${services}.SettingsService.ConnectionModels`, id).then((r) => r ?? []),
    setKey: (key: string) => call<void>(`${services}.SettingsService.SetAPIKey`, key),
    models: () => call<ModelInfo[] | null>(`${services}.SettingsService.Models`).then((r) => r ?? []),
};

export const voiceApi = {
    start: (recordingId: string) => call<void>(`${services}.VoiceService.Start`, recordingId),
    stop: () => call<string>(`${services}.VoiceService.Stop`),
    cancel: () => call<void>(`${services}.VoiceService.Cancel`),
    modelName: () => call<string>(`${services}.VoiceService.ModelName`),
    checkSetup: (settings: VoiceSettings) => call<VoiceReadiness>(`${services}.VoiceService.CheckSetup`, settings),
    pickModelFolder: (engine: string, current: string) => call<string>(`${services}.VoiceService.PickModelFolder`, engine, current),
};

export const contextApi = {
    clipboard: () => call<Attachment | null>(`${services}.ContextService.Clipboard`),
    autoClipboard: () => call<ClipboardCapture>(`${services}.ContextService.AutoClipboard`),
    screenshot: () => call<Attachment | null>(`${services}.ContextService.Screenshot`),
    cancelScreenshot: () => call<void>(`${services}.ContextService.CancelScreenshot`),
    selectionStatus: () => call<SelectionSupport>(`${services}.ContextService.SelectionStatus`),
    selection: () => call<SelectionCapture>(`${services}.ContextService.Selection`),
};

export const popupApi = {
    hide: () => call<void>("main.PopupService.Hide"),
    resize: (height: number) => call<void>("main.PopupService.Resize", height),
};

//Events from Go. Each returns an unsubscribe function
type Handler<T> = (data: T) => void
const on = <T>(name: string, fn: Handler<T>) => Events.On(name, (ev) => fn(ev.data as T));

export const events = {
    open: (fn: Handler<{ mode: View; selection?: SelectionCapture }>) => on("ntikuma:open", fn),
    hidden: (fn: Handler<null>) => on("ntikuma:hidden", fn),
    focus: (fn: Handler<null>) => on("ntikuma:focus", fn),
    delta: (fn: Handler<DeltaEvent>) => on("chat:delta", fn),
    done: (fn: Handler<DoneEvent>) => on("chat:done", fn),
    error: (fn: Handler<ErrorEvent>) => on("chat:error", fn),
    tool: (fn: Handler<ToolEvent>) => on("chat:tool", fn),
    approval: (fn: Handler<ApprovalEvent>) => on("chat:approval", fn),
    voiceLevel: (fn: Handler<VoiceLevelEvent>) => on("voice:level", fn),
};

/** Turns a Go error (or anything thrown) into a readable sentence. */
export function errorText(e: unknown): string {
    if (e instanceof Error) return e.message;
    if (typeof e === "string") return e;
    if (e && typeof e === "object" && "message" in e) return String((e as { message: unknown }).message);
    return "Something went wrong.";
}
