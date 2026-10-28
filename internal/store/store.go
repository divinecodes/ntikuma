package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

//go:embed schema.sql
var schema string

const DriverName = "sqlite"

var ErrNotFound = errors.New("not found")

// snippet markers wrap matched terms in search snippets. They are control characters so
// can't collide with the chat text; the UI splits on them
const (
	MarkStart = "\x02"
	MarkEnd   = "\x03"
)

// chat is a conversation
type Chat struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Message struct {
	ID       string `json:"id"`
	ChatID   string `json:"chatId"`
	Role     string `json:"role"`
	Content  string `json:"content"`
	Model    string `json:"model,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	// Attachments is the JSON-encoded context that was sent with a user message
	Attachments string    `json:"attachments,omitempty"`
	Meta        string    `json:"meta,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type SearchHit struct {
	Chat    Chat   `json:"chat"`
	Snippet string `json:"snippet"`
}

type Store struct {
	db *sql.DB
}

// open (and migrates) the database at path
func Open(path string) (*Store, error) {
	//_pragma parameters
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer keeps SQLite is okay as this is a single-user application
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

const schemaVersion = 2

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > schemaVersion {
		return fmt.Errorf("unsupported database version %d (maximum %d)", version, schemaVersion)
	}
	// Released installations have the v1 tables but user_version is still zero.
	// The original schema uses IF NOT EXISTS, so it also initializes a fresh DB.
	if version == 0 {
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		if _, err := tx.Exec("ALTER TABLE messages ADD COLUMN meta TEXT"); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// close the database
func (s *Store) Close() error { return s.db.Close() }

// NewID returns a sortable random ID: milliseconds timestamp plus 6 random bytes
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%012x%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// CreateChat starts a chat with the given title
func (s *Store) CreateChat(ctx context.Context, title string) (Chat, error) {
	now := time.Now().UTC()
	c := Chat{ID: NewID(), Title: title, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chats (id,title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		c.ID, c.Title, now.UnixMilli(), now.UnixMilli())

	return c, err
}

// RenameChat changes a chat's title
func (s *Store) RenameChat(ctx context.Context, id, title string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE  chats SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// AddMessage appends m to its chat and bumps the chat's updated time. ID and CreatedAt are filled in when
func (s *Store) AddMessage(ctx context.Context, m Message) (Message, error) {
	if m.ID == "" {
		m.ID = NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}

	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, chat_id, role, content, model, fallback, attachments, meta, created_at)
		VALUES (?, ?, ?, ? , ?, ?, ?, ?, ?)`,
		m.ID, m.ChatID, m.Role, m.Content, m.Model, m.Fallback, m.Attachments, m.Meta, m.CreatedAt.UnixMilli()); err != nil {
		return m, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE chats SET updated_at = ? WHERE id = ?`, m.CreatedAt.UnixMilli(), m.ChatID)
	if err != nil {
		return m, err
	}
	if err := mustAffect(res); err != nil {
		return m, fmt.Errorf("chat %s: %w", m.ChatID, err)
	}
	return m, tx.Commit()
}

func (s *Store) GetChat(ctx context.Context, id string) (Chat, []Message, error) {
	var c Chat
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM chats WHERE id = ? `, id).Scan(&c.ID, &c.Title, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil, ErrNotFound
	}
	if err != nil {
		return c, nil, err
	}
	c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()

	rows, err := s.db.QueryContext(ctx,
		`SELECT  id,chat_id, role, content, COALESCE(model, ''), fallback, COALESCE(attachments, ''), COALESCE(meta, ''), created_at
		FROM messages WHERE chat_id = ? ORDER BY created_at, rowid`, id)
	if err != nil {
		return c, nil, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var m Message
		var at int64
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.Model, &m.Fallback, &m.Attachments, &m.Meta, &at); err != nil {
			return c, nil, err
		}
		m.CreatedAt = time.UnixMilli(at).UTC()
		msgs = append(msgs, m)
	}
	return c, msgs, rows.Err()
}

// ListChats returns the most recently updated chats first
func (s *Store) ListChats(ctx context.Context, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, created_at, updated_at FROM chats ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Search finds chats whose title, or messages match query. Each work matched as a prefix
func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.title, c.created_at, c.updated_at,
		       snippet(messages_fts, 0, ?, ?, '…', 12) AS snip,
		       bm25(messages_fts) AS score
		FROM messages_fts
		JOIN messages m ON m.rowid = messages_fts.rowid
		JOIN chats c ON c.id = m.chat_id
		WHERE messages_fts MATCH ?
		UNION ALL
		SELECT c.id, c.title, c.created_at, c.updated_at, '' AS snip, -1000 AS score
		FROM chats c
		WHERE c.title LIKE ? ESCAPE '\'
		ORDER BY score
		LIMIT ?`,
		MarkStart, MarkEnd, match, "%"+likeEscape(strings.TrimSpace(query))+"%", limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// keep the best-scoring chat
	byChat := map[string]int{}
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var created, updated int64
		var score float64
		if err := rows.Scan(&h.Chat.ID, &h.Chat.Title, &created, &updated, &h.Snippet, &score); err != nil {
			return nil, err
		}
		h.Chat.CreatedAt, h.Chat.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated)
		if i, seen := byChat[h.Chat.ID]; seen {
			if out[i].Snippet == "" && h.Snippet != "" {
				out[i].Snippet = h.Snippet
			}
			continue
		}
		byChat[h.Chat.ID] = len(out)
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

// DeleteChat removes a chat and it's message
func (s *Store) DeleteChat(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// DeleteAll removes every chat
func (s *Store) DeleteAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chats`)
	return err
}

// Prune deletes chats not updated within maxAge
func (s *Store) Prune(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	res, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE  updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ftsQuery turns free text into  safe FTS5 query: every word becomes a qouted prefix term
// so user can't inject FTS5 syntax
func ftsQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.'
	})
	var parts []string
	for _, w := range words {
		w = strings.Trim(w, "-.")
		if w == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
