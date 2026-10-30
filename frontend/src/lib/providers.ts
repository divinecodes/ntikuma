import type { Connection, Settings } from "./types";

// The connection questions go to, or undefined for OpenRouter.
export function activeConnection(settings: Settings | null | undefined): Connection | undefined {
    if (!settings?.provider || settings.provider === "openrouter") return undefined;
    return settings.connections?.find((c) => c.id === settings.provider);
}

// The active provider's default model, then its fallbacks, without repeats.
export function activeModels(settings: Settings | null | undefined): string[] {
    if (!settings) return [];
    const connection = activeConnection(settings);
    if (settings.provider && settings.provider !== "openrouter" && !connection) return [];
    const source = connection ?? settings;
    return [...new Set([source.defaultModel, ...(source.fallbacks ?? [])].filter(Boolean))];
}

export function activeDefaultModel(settings: Settings | null | undefined): string {
    return activeModels(settings)[0] ?? "";
}
