# Privacy

Ntikuma combines local desktop features with chat through OpenRouter. Review attached context before sending a question.

## Data sent for chat

Sending a question sends its text, previous user/assistant messages in that chat, the system prompt, and attached context to OpenRouter and the selected model provider. The system prompt includes your local date, time and timezone. Sent clipboard text, selected text and screenshots are included again in follow-up questions.

Web search is enabled by default. Turn off the Web pill, or press Ctrl+G, for a question without web tools. With Web enabled, OpenRouter's search/fetch services may process the question and conversation context. Sources and reported search counts are saved with the answer.

Enabling Local tools allows the model to list and read files in your configured folders. File contents, names and paths returned by tools go to the chat model. Approved shell commands run with your account's permissions; their output also goes to the model. Folder restrictions apply to file tools, and do not restrict approved shell commands.

OpenRouter and its providers have their own processing and retention policies. Ntikuma does not enforce zero-retention settings or delete provider-held data when you delete local history.

## Clipboard and screenshots

Automatic clipboard attachment starts enabled. The chip shows what will be attached; remove it or turn off auto-attach in Preferences before sending sensitive clipboard content. Selected-text attachment is optional, and screenshots use the desktop's interactive capture dialog. Unsent chips are not chat requests.

Removing an unsent chip excludes it from that question. Context already sent remains in its chat history and can be included in later questions. Deleting a chat does not delete an original image saved by the desktop's screenshot application.

## Voice

Qwen, Parakeet and Cohere transcription run through the local native helper using downloaded models. Ntikuma does not upload audio for these engines. Sending the resulting transcript sends its text and attached context through OpenRouter.

Whisper uploads recordings to the Whisper server URL configured in Preferences. The default is a loopback address on your device. If you configure a remote server, that server receives the audio.

Normal transcription and cancellation remove temporary recordings. A crash or forced shutdown can leave files in the voice cache. Recordings and reports created manually with the evaluation workflow remain until you remove them.

## Local storage and deletion

| Data | Default location |
| --- | --- |
| Preferences and model paths | `~/.config/ntikuma/config.json` |
| Chats, sent attachments, sources and tool activity | `~/.local/share/ntikuma/ntikuma.db` |
| Temporary recordings | `~/.cache/ntikuma/voice/` |
| Installer startup log | `~/.cache/ntikuma/startup.log` |
| Saved OpenRouter key | GNOME Keyring, service `ntikuma` |

The database includes tool-call arguments and bounded results, which can contain private file contents or command output. Preferences and the database are not encrypted by Ntikuma. `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `XDG_CACHE_HOME` can change these locations.

`NTIKUMA_OPENROUTER_KEY` overrides the saved key when set. The saved key is kept separately from preferences and chat storage. Putting a credential in a question, attached clipboard, file or command output can still transmit and save it as ordinary chat content.

Delete chats or clear History to remove their active database records. Retention runs on startup; its default of zero days keeps chats indefinitely. Deletion does not guarantee secure erasure of SQLite pages, journal files, filesystem backups, or data already sent to a provider. Models and manually saved recordings are separate files.

The optional `ntikuma-migrate-deb --apply` command creates private local migration backups containing chats, preferences, old executables, desktop entries, and affected shortcut commands. These remain in your Ntikuma data directory under `migration-backups/` until you delete them. The migration does not copy keyring credentials or models, upload backup contents, or remove old model folders. Chat deletion in the app does not delete these independent backups.

## Other connections

The model picker and image-capability checks fetch OpenRouter's model catalog. Installing dependencies or downloading speech models contacts the relevant package registries or upstream download hosts. The application code has no Ntikuma analytics or crash-reporting endpoint.

Images in model answers appear as descriptions. Raw HTML appears as text. HTTP(S) links open in your browser when you click them; that browser then contacts the destination under its own settings.
