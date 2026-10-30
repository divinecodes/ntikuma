# Privacy

Ntikuma combines local desktop features with chat through OpenRouter, or directly with a provider you add in Preferences. Review attached context before sending a question.

## Data sent for chat

Sending a question sends its text, previous user/assistant messages in that chat, the system prompt, and attached context to the active provider: OpenRouter and the model provider it routes to, or the provider you chose in Preferences (OpenAI, Anthropic, Ollama, Kimi, DeepSeek or another OpenAI-compatible server). The system prompt includes your local date, time and timezone. Sent clipboard text, selected text and screenshots are included again in follow-up questions. Only the four most recent screenshots in a chat (at most 24 MiB in total) are resent. In a long chat, the oldest messages stop being sent once the conversation passes about half the smallest context window among your chosen models (or the window you set for a direct provider), and never more than about 300,000 characters; they stay in your local history.

Web search is enabled by default. Turn off the Web pill, or press Ctrl+G, for a question without web tools. With Web enabled, OpenRouter's search/fetch services may process the question and conversation context. Sources and reported search counts are saved with the answer. Web search runs only through OpenRouter, so it's off while a direct provider is active.

Enabling Local tools allows the model to list and read files in your configured folders. File contents, names and paths returned by tools go to the chat model. File tools refuse hidden files unless you turn on Read hidden files in Preferences, and always refuse key and credential files such as SSH keys, `.env` files and `.aws/credentials`. This name-based check can miss a secret stored under an ordinary file name. After a local tool runs, the rest of that answer can't fetch web pages. Approved shell commands run with your account's permissions; their output also goes to the model. Folder restrictions apply to file tools, and do not restrict approved shell commands.

OpenRouter, its providers, and providers you connect directly have their own processing and retention policies. Ntikuma does not enforce zero-retention settings or delete provider-held data when you delete local history.

## Direct providers

A direct provider gets the same data OpenRouter would: the question, the chat's earlier messages, the system prompt, attachments and tool results. Its key is saved for its address (scheme, host and port), so after you change the address the old key isn't sent to the new one. Requests use `https://`, except to a server on this computer. A redirect to a different address is refused rather than followed with the question and key. The question doesn't go through OpenRouter.

A server on this computer doesn't keep a question on this computer by itself. Ollama, for example, runs its cloud models on Ollama's servers. To keep questions local with Ollama, use local models and turn off its cloud features (`OLLAMA_NO_CLOUD=1`, or `"disable_ollama_cloud": true` in `~/.ollama/server.json`; see [Ollama's FAQ](https://github.com/ollama/ollama/blob/main/docs/faq.mdx)).

DeepSeek and some similar providers return their reasoning with an answer (`reasoning_content`) and require it back on later requests that offer tools. Ntikuma saves up to 32 KiB of it with the answer in the local database, and sends it back only to the provider that wrote it and only while Local tools are on. It isn't shown in the app. Anthropic's thinking blocks, and OpenAI's encrypted reasoning, are sent back within one answer's tool steps and never saved. OpenAI requests ask it not to store the response (`store: false`); OpenAI's own retention policy for abuse monitoring still applies. History notes which direct provider answered.

## Clipboard and screenshots

Automatic clipboard attachment starts off; choose Clipboard to attach copied text to one question, or turn on auto-attach in Preferences or the composer. With auto-attach on, the chip shows what will be attached; remove it before sending sensitive clipboard content. If you have saved Preferences before, your saved auto-attach setting is kept. Auto-attach holds back clipboard text that looks like a password, API token, private key or a `KEY=value` secret, and says so; choose Clipboard to attach it anyway. In a Wayland session it also holds back anything a password manager marked as secret when copying (KeePassXC and KWalletManager do this; not every password manager does). GTK doesn't pass that mark on in an X11 session, so there only the text check applies. This check is a heuristic, so it can miss a secret or hold back ordinary text. Selected-text attachment is optional, and screenshots use the desktop's interactive capture dialog. Unsent chips are not chat requests.

Removing an unsent chip excludes it from that question. Context already sent remains in its chat history and can be included in later questions. Deleting a chat does not delete an original image saved by the desktop's screenshot application.

## Voice

Qwen, Parakeet and Cohere transcription run through the local native helper using downloaded models. Ntikuma does not upload audio for these engines. Sending the resulting transcript sends its text and attached context to the active provider.

Whisper uploads recordings to the Whisper server URL configured in Preferences. The default is a loopback address on your device. If you configure a remote server, that server receives the audio.

Normal transcription and cancellation remove temporary recordings. A crash or forced shutdown can leave files in the voice cache. Recordings and reports created manually with the evaluation workflow remain until you remove them.

## Local storage and deletion

| Data | Default location |
| --- | --- |
| Preferences and model paths | `~/.config/ntikuma/config.json` |
| Chats, sent attachments, sources and tool activity | `~/.local/share/ntikuma/ntikuma.db` |
| Temporary recordings | `~/.cache/ntikuma/voice/` |
| Installer startup log | `~/.cache/ntikuma/startup.log` |
| Saved OpenRouter and provider keys | GNOME Keyring, service `ntikuma` |

The database includes tool-call arguments and bounded results, which can contain private file contents or command output. Preferences and the database are not encrypted by Ntikuma. `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `XDG_CACHE_HOME` can change these locations.

`NTIKUMA_OPENROUTER_KEY` overrides the saved OpenRouter key when set; it isn't used for direct providers. The saved key is kept separately from preferences and chat storage. Putting a credential in a question, attached clipboard, file or command output can still transmit and save it as ordinary chat content.

Delete chats or clear History to remove their active database records. Retention runs on startup; its default of zero days keeps chats indefinitely. Deletion does not guarantee secure erasure of SQLite pages, journal files, filesystem backups, or data already sent to a provider. Models and manually saved recordings are separate files.

The optional `ntikuma-migrate-deb --apply` command creates private local migration backups containing chats, preferences, old executables, desktop entries, and affected shortcut commands. These remain in your Ntikuma data directory under `migration-backups/` until you delete them. The migration does not copy keyring credentials or models, upload backup contents, or remove old model folders. Chat deletion in the app does not delete these independent backups.

## Other connections

The model picker and image-capability checks fetch OpenRouter's model catalog. Preferences asks a direct provider for its model list, sending that provider's key. Installing dependencies or downloading speech models contacts the relevant package registries or upstream download hosts. The application code has no Ntikuma analytics or crash-reporting endpoint.

Images in model answers appear as descriptions. Raw HTML appears as text. HTTP(S) links open in your browser when you click them; that browser then contacts the destination under its own settings.
