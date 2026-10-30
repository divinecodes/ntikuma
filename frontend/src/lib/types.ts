//mirros of the Go structs

export type View = "bar" | "chat" | "voice" | "history" | "settings"

export interface Attachment {
    kind: "clipboard" | "selection" | "screenshot";
    label: string;
    content: string;
}

export interface SelectionSupport {
    available: boolean;
    beforeFocus: boolean;
    message: string;
}

/** Mirrors services.ClipboardCapture: a held-back clipboard has only a message. */
export interface ClipboardCapture {
    attachment: Attachment | null;
    message: string;
}

export interface SelectionCapture {
    attachment: Attachment | null;
    message: string;
}

export interface SendRequest {
    chatId: string;
    text: string;
    attachments: Attachment[];
    model: string;
    web: boolean;
}

export interface Citation {
    url: string;
    title: string;
}

export interface SendResult {
    chatId: string;
    title: string;
    messageId: string;
}

export interface DeltaEvent {
    chatId: string;
    messageId: string;
    text: string;
}

export interface ToolActivity {
    callId: string;
    name: string;
    summary: string;
    status: "waiting" | "started" | "finished" | "error" | "stopped" | "denied";
}

export interface ToolEvent {
    chatId: string;
    messageId: string;
    activity: ToolActivity;
}

export interface ApprovalEvent {
    chatId: string;
    messageId: string;
    callId: string;
    command: string;
    workingDirectory: string;
    expiresAt: string;
    state: "pending" | "closed";
    outcome?: "allowed" | "denied" | "expired" | "canceled" | "revoked";
}

export interface DoneEvent {
    chatId: string;
    messageId: string;
    model: string;
    fallback: boolean;
    stopped: boolean;
    cutOff?: boolean;
    activity?: ToolActivity[];
    citations?: Citation[];
    searches?: number;
}

export interface ErrorEvent {
    chatId: string;
    messageId: string;
    error: string;
    cutOff?: boolean;
    model?: string;
    fallback?: boolean;
    activity?: ToolActivity[];
    citations?: Citation[];
    searches?: number;
}

export interface Chat {
    id: string;
    title: string;
    createdAt: string;
    updatedAt: string;
}

export interface MessageView {
    id: string;
    role: "user" | "assistant" | "system";
    content: string;
    model?: string;
    fallback?: boolean;
    attachments?: Attachment[];
    createdAt: string;
    citations?: Citation[];
    searches?: number;
    activity?: ToolActivity[];
    stopped?: boolean;
    error?: string;
    cutOff?: boolean;
}

export interface ChatDetail {
    chat: Chat;
    messages: MessageView[];
}

export interface SearchHit {
    chat: Chat;
    snippet: string;
}

export interface VoiceSettings {
    engine: string;
    whisperUrl: string;
    whisperServerBin: string;
    whisperModelPath: string;
    language: string;
    nativeHelper: string;
    qwen: LocalModelSettings;
    parakeet: LocalModelSettings;
    cohere: LocalModelSettings & { language: string };
}

export interface LocalModelSettings { modelDir: string; threads: number; }
export interface VoiceReadiness { ready: boolean; message: string; }

export interface VoiceLevelEvent {
    recordingId: string;
    level: number;
    available: boolean;
}

export interface Connection {
    id: string;
    kind: "openai-compatible" | "anthropic";
    name: string;
    baseUrl: string;
    defaultModel: string;
    fallbacks: string[];
    contextTokens: number;
    maxTokens: number;
}

export interface Settings {
    // "" or "openrouter", or a connection ID. defaultModel and fallbacks are OpenRouter's.
    provider: string;
    connections: Connection[];
    defaultModel: string;
    fallbacks: string[];
    systemPrompt: string;
    attachClipboard: boolean;
    attachSelection: boolean;
    offerScreenshot: boolean;
    maxContextChars: number;
    voice: VoiceSettings;
    webSearch: { maxResults: number; maxFetches: number; maxToolCalls: number };
    localTools: boolean;
    toolRoots: string[];
    allowHiddenFiles: boolean;
    allowShellCommands: boolean;
    keepChatsDays: number;
    usePortalShortcuts: boolean;
}

export interface ModelInfo {
    id: string;
    name: string;
    contextLength: number;
    inputModalities?: string[];
}

export interface UIMessage {
    id: string;
    role: "user" | "assistant";
    content: string;
    model?: string;
    fallback?: boolean;
    attachments?: Attachment[];
    streaming?: boolean;
    stopped?: boolean;
    error?: string
    /** Stopped early with some text, so it can be continued. */
    cutOff?: boolean;
    citations?: Citation[];
    searches?: number;
    activity?: ToolActivity[];
}

