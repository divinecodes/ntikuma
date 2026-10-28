package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLegacyDatabasesUpgradeAndKeepHistory(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, err := sql.Open(DriverName, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO chats VALUES('chat','Old chat',1,1); INSERT INTO messages(id,chat_id,role,content,attachments,created_at) VALUES('message','chat','user','Older searchable message','[{"kind":"clipboard","content":"saved"}]',1)`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				t.Fatal(err)
			}
			db.Close()
			for attempt := 0; attempt < 2; attempt++ {
				st, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				var got int
				if err := st.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != 2 {
					t.Fatalf("version %d: %v", got, err)
				}
				_, messages, err := st.GetChat(context.Background(), "chat")
				if err != nil || len(messages) != 1 || messages[0].Meta != "" || messages[0].Attachments == "" {
					t.Fatalf("old history lost: %+v %v", messages, err)
				}
				hits, err := st.Search(context.Background(), "searchable", 10)
				if err != nil || len(hits) != 1 {
					t.Fatalf("FTS lost: %+v %v", hits, err)
				}
				st.Close()
			}
		})
	}
}

func TestFreshDatabaseMetadataRoundTrip(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	chat, err := st.CreateChat(ctx, "Sources")
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"citations":[{"url":"https://go.dev/","title":"Go"}],"searches":1,"futureToolActivity":[]}`
	if _, err := st.AddMessage(ctx, Message{ChatID: chat.ID, Role: "assistant", Content: "Answer", Meta: meta}); err != nil {
		t.Fatal(err)
	}
	_, messages, err := st.GetChat(ctx, chat.ID)
	if err != nil || len(messages) != 1 || messages[0].Meta != meta {
		t.Fatalf("metadata lost: %+v %v", messages, err)
	}
}

func TestNewerDatabaseVersionIsNotDowngraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open(DriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("accepted future schema")
	}
	db, err = sql.Open(DriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 3 {
		t.Fatalf("future version changed: %d", version)
	}
}
