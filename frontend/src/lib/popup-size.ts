// WebKit can pause rendering observers while the native popup is hidden. DOM
// changes must still fit new content, especially a pending approval card.
export function observePopupSize(card: HTMLElement, resize: (height: number) => void) {
    let last = 0;
    const measure = () => {
        const height = Math.ceil(card.getBoundingClientRect().height);
        if (height > 0 && height !== last) {
            last = height;
            resize(height);
        }
    };
    const observer = new ResizeObserver(measure);
    const mutations = new MutationObserver(measure);
    observer.observe(card);
    mutations.observe(card, { subtree: true, childList: true, characterData: true, attributes: true });
    measure();
    return () => { observer.disconnect(); mutations.disconnect(); };
}
