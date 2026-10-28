// Progressive enhancement only: the page reads the same without this file.
// Motion is skipped when the system asks for reduced motion.

const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

function setUpMenu() {
    const header = document.querySelector(".header");
    const toggle = header?.querySelector(".menu-toggle");
    const nav = document.getElementById("site-nav");
    if (!header || !toggle || !nav) return;

    const setOpen = (open) => {
        header.classList.toggle("open", open);
        toggle.setAttribute("aria-expanded", String(open));
    };

    toggle.addEventListener("click", () => setOpen(toggle.getAttribute("aria-expanded") !== "true"));
    nav.addEventListener("click", (event) => {
        if (event.target.closest("a")) setOpen(false);
    });
    document.addEventListener("keydown", (event) => {
        if (event.key === "Escape" && header.classList.contains("open")) {
            setOpen(false);
            toggle.focus();
        }
    });
    document.addEventListener("click", (event) => {
        if (!header.contains(event.target)) setOpen(false);
    });
    window.matchMedia("(min-width: 961px)").addEventListener("change", (event) => {
        if (event.matches) setOpen(false);
    });
}

// Types the hero question once the popup has sprung open (see .hero .popup in styles.css).
function typeHeroQuestion() {
    const typed = document.querySelector("[data-typed]");
    if (!typed || reduceMotion.matches) return;

    const text = typed.textContent;
    typed.textContent = "";
    let shown = 0;
    const next = () => {
        shown += 1;
        typed.textContent = text.slice(0, shown);
        if (shown < text.length) setTimeout(next, 40 + Math.random() * 60);
    };
    setTimeout(next, 1400);
}

// Lazy loading: the poster is attached only when the video nears
// the viewport, and the source only when someone presses play, so no video
// bytes are fetched before then (the file is faststart, so it starts quickly).
function setUpVideo() {
    const video = document.querySelector(".video video.lazy");
    const play = document.querySelector(".video .play");
    if (!video || !play) return;

    const showPoster = () => {
        if (!video.poster) video.poster = video.dataset.poster;
    };
    const attachSource = () => {
        const sources = video.querySelectorAll("source[data-src]");
        if (!sources.length) return;
        for (const source of sources) {
            source.src = source.dataset.src;
            source.removeAttribute("data-src");
        }
        video.load();
    };

    if ("IntersectionObserver" in window) {
        const observer = new IntersectionObserver((entries) => {
            if (entries.some((entry) => entry.isIntersecting)) {
                observer.disconnect();
                showPoster();
            }
        }, { rootMargin: "400px 0px" });
        observer.observe(video);
    } else {
        showPoster();
    }

    // The overlay stands in for the native controls until playback starts.
    play.hidden = false;
    video.controls = false;
    play.addEventListener("click", () => {
        showPoster();
        attachSource();
        video.controls = true;
        video.play().catch(() => {
            // Leave the native controls in charge if playback is refused.
            play.hidden = true;
        });
    });
    video.addEventListener("play", () => {
        play.hidden = true;
        video.controls = true;
    });
}

// Runs the voice waveform for 4.8s whenever it comes into view, then freezes
// it, so the motion always stops within five seconds.
function setUpLevels() {
    const levels = document.querySelector(".levels");
    if (!levels || reduceMotion.matches || !("IntersectionObserver" in window)) return;

    let timer;
    const observer = new IntersectionObserver((entries) => {
        clearTimeout(timer);
        if (entries.some((entry) => entry.isIntersecting)) {
            levels.classList.add("live");
            timer = setTimeout(() => levels.classList.remove("live"), 4800);
        } else {
            levels.classList.remove("live");
        }
    }, { threshold: 0.5 });
    observer.observe(levels);
}

// Plays the approval card the way the app resolves it: cursor to Allow once,
// the card resolves to "Ran df -h", then the answer streams in word by word.
function setUpApprovalDemo() {
    const demo = document.getElementById("approval-demo");
    if (!demo) return;

    const popup = demo.querySelector(".popup");
    const cursor = demo.querySelector(".cursor");
    const allow = demo.querySelector(".allow");
    const card = demo.querySelector(".approve");
    const awaiting = demo.querySelector(".awaiting");
    const ran = demo.querySelector(".ran");
    const answer = demo.querySelector(".answer");
    const words = [...answer.querySelectorAll(".w")];
    const replay = demo.querySelector(".replay");

    let timers = [];
    const later = (ms, fn) => timers.push(setTimeout(fn, ms));

    const reset = () => {
        timers.forEach(clearTimeout);
        timers = [];
        popup.style.minHeight = "";
        cursor.classList.remove("show");
        cursor.style.transition = "none";
        allow.classList.remove("pressed");
        card.hidden = false;
        awaiting.hidden = false;
        ran.hidden = true;
        answer.hidden = true;
        words.forEach((word) => word.classList.remove("on"));
        // Stay visible while replaying so keyboard focus isn't dropped.
        replay.setAttribute("aria-disabled", "true");
    };

    const run = () => {
        if (timers.length) return;
        reset();
        // Hold the popup's height so the section doesn't jump when the card resolves.
        popup.style.minHeight = `${popup.offsetHeight}px`;

        const box = popup.getBoundingClientRect();
        const target = allow.getBoundingClientRect();
        const endX = target.left - box.left + target.width * 0.55;
        const endY = target.top - box.top + target.height * 0.5;
        cursor.style.transform = `translate(${box.width - 40}px, ${box.height - 30}px)`;

        later(400, () => {
            cursor.style.transition = "";
            cursor.classList.add("show");
            cursor.style.transform = `translate(${endX}px, ${endY}px)`;
        });
        later(1500, () => allow.classList.add("pressed"));
        later(1800, () => {
            cursor.classList.remove("show");
            card.hidden = true;
            awaiting.hidden = true;
            ran.hidden = false;
        });
        later(2300, () => {
            answer.hidden = false;
            words.forEach((word, index) => later(index * 70, () => word.classList.add("on")));
            later(words.length * 70 + 400, () => {
                timers = [];
                replay.hidden = false;
                replay.removeAttribute("aria-disabled");
            });
        });
    };

    replay.addEventListener("click", run);

    if (reduceMotion.matches || !("IntersectionObserver" in window)) return;
    const observer = new IntersectionObserver((entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
            observer.disconnect();
            run();
        }
    }, { threshold: 0.6 });
    observer.observe(popup);
}

setUpMenu();
typeHeroQuestion();
setUpVideo();
setUpLevels();
setUpApprovalDemo();
