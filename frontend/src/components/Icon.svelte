<script lang="ts" module>
    // Stroke icons, 24-unit grid, drawn to match the design canvas.
    const paths = {
        logo: '<circle cx="12" cy="12" r="9"/><path d="M12 3v18M4.2 7.5l15.6 9M4.2 16.5l15.6-9"/><path d="M12 7.5l3.9 2.25v4.5L12 16.5l-3.9-2.25v-4.5z"/>',
        mic: '<rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3"/>',
        x: '<path d="M6 6l12 12M18 6L6 18"/>',
        chevron: '<path d="M6 9l6 6 6-6"/>',
        clipboard: '<rect x="6" y="4" width="12" height="17" rx="2"/><path d="M9 4h6v3H9z"/>',
        screenshot:
            '<path d="M4 8V5a1 1 0 0 1 1-1h3M16 4h3a1 1 0 0 1 1 1v3M20 16v3a1 1 0 0 1-1 1h-3M8 20H5a1 1 0 0 1-1-1v-3"/>',
        plus: '<path d="M12 5v14M5 12h14"/>',
        clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
        edit: '<path d="M12 20h8M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
        copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
        search: '<circle cx="11" cy="11" r="6"/><path d="M20 20l-4.5-4.5"/>',
        globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a17 17 0 0 1 0 18 17 17 0 0 1 0-18"/>',
        trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
        refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7"/>',
        swap: '<path d="M7 7h11l-3-3M17 17H6l3 3"/>',
        up: '<path d="M6 15l6-6 6 6"/>',
        down: '<path d="M6 9l6 6 6-6"/>',
        back: '<path d="M15 6l-6 6 6 6"/>',
        send: '<path d="M12 19V5M6 11l6-6 6 6"/>',
        stop: '<rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor" stroke="none"/>',
        settings:
            '<path d="M4 7h10M18 7h2M4 17h4M12 17h8"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="17" r="2"/>',
    } as const;

    export type IconName = keyof typeof paths;
</script>

<script lang="ts">
    let { name, size = 16, stroke = 1.8 }: { name: IconName; size?: number; stroke?: number } = $props();
</script>

<svg
        viewBox="0 0 24 24"
        width={size}
        height={size}
        aria-hidden="true"
        fill="none"
        stroke="currentColor"
        stroke-width={stroke}
        stroke-linecap="round"
        stroke-linejoin="round"
        style="flex: none"
>
    {@html paths[name]}
</svg>
