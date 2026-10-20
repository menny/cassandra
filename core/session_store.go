package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/menny/cassandra/llm"
	"github.com/menny/cassandra/util"
)

const (
	messagesFileName  = "messages.jsonl"
	sessionDirPerm    = 0o700
	sessionFilePerm   = 0o600
	maxLineBufferSize = 10 * 1024 * 1024 // 10 MB per JSON line
)

// SessionStore manages persisting and loading conversation sessions.
type SessionStore interface {
	// AppendMessages appends messages to the session's messages.jsonl.
	AppendMessages(ctx context.Context, sessionID string, msgs ...llm.Message) error
	// LoadSession reads and parses all messages from the session's messages.jsonl.
	LoadSession(ctx context.Context, sessionID string) ([]llm.Message, error)
	// SessionExists returns true if messages.jsonl exists and is non-empty for sessionID.
	SessionExists(sessionID string) bool
}

var (
	_ SessionStore = (*ConcreteSessionStore)(nil)
	_ SessionStore = (*NoopSessionStore)(nil)
)

// NewSessionStore returns a ConcreteSessionStore rooted at the given config folder,
// or a NoopSessionStore if configFolder is empty.
func NewSessionStore(configFolder string) SessionStore {
	trimmed := strings.TrimSpace(configFolder)
	if trimmed == "" {
		return NewNoopSessionStore()
	}
	expanded := util.ExpandHome(trimmed)
	return NewConcreteSessionStore(expanded)
}

// ConcreteSessionStore persists session messages to <baseDir>/sessions/<sessionID>/messages.jsonl.
type ConcreteSessionStore struct {
	mu      sync.Mutex
	baseDir string
}

// NewConcreteSessionStore creates a ConcreteSessionStore persisting to <baseDir>/sessions/<sessionID>/messages.jsonl.
func NewConcreteSessionStore(baseDir string) *ConcreteSessionStore {
	return &ConcreteSessionStore{
		baseDir: filepath.Clean(baseDir),
	}
}

func (s *ConcreteSessionStore) validateSessionID(sessionID string) error {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return errors.New("session ID cannot be empty")
	}
	sessionsDir := filepath.Clean(filepath.Join(s.baseDir, "sessions"))
	targetDir := filepath.Clean(filepath.Join(sessionsDir, trimmed))
	rel, err := filepath.Rel(sessionsDir, targetDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(rel) != "." || strings.ContainsAny(trimmed, "/\\") {
		return fmt.Errorf("invalid session ID %q", sessionID)
	}
	return nil
}

func (s *ConcreteSessionStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, "sessions", strings.TrimSpace(sessionID))
}

func (s *ConcreteSessionStore) messagesFilePath(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), messagesFileName)
}

func (s *ConcreteSessionStore) AppendMessages(ctx context.Context, sessionID string, msgs ...llm.Message) error {
	sessionID = strings.TrimSpace(sessionID)
	if len(msgs) == 0 {
		return nil
	}
	if err := s.validateSessionID(sessionID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	if err := os.MkdirAll(dir, sessionDirPerm); err != nil {
		return fmt.Errorf("creating session directory %s: %w", dir, err)
	}
	// Explicitly ensure directory permissions match sessionDirPerm even if umask modified it
	if err := os.Chmod(dir, sessionDirPerm); err != nil {
		return fmt.Errorf("chmod session directory %s: %w", dir, err)
	}

	filePath := s.messagesFilePath(sessionID)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, sessionFilePerm)
	if err != nil {
		return fmt.Errorf("opening session file %s: %w", filePath, err)
	}
	defer f.Close()

	// Explicitly ensure file permissions match sessionFilePerm even if umask modified it
	if err := f.Chmod(sessionFilePerm); err != nil {
		return fmt.Errorf("chmod session file %s: %w", filePath, err)
	}

	var buf bytes.Buffer
	for i, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshaling message %d for session %s: %w", i, sessionID, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	if _, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("writing messages to %s: %w", filePath, err)
	}
	return nil
}

func (s *ConcreteSessionStore) LoadSession(ctx context.Context, sessionID string) ([]llm.Message, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := s.validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.messagesFilePath(sessionID)
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q not found: %w", sessionID, err)
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var messages []llm.Message
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxLineBufferSize)

	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("corrupt message on line %d of session %q: %w", lineNum, sessionID, err)
		}
		messages = append(messages, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading session %q: %w", sessionID, err)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("session %q is empty", sessionID)
	}
	return messages, nil
}

func (s *ConcreteSessionStore) SessionExists(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if err := s.validateSessionID(sessionID); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	fi, err := os.Stat(s.messagesFilePath(sessionID))
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// NoopSessionStore is a SessionStore that does not persist anything.
type NoopSessionStore struct{}

// NewNoopSessionStore returns a NoopSessionStore that does not persist anything.
func NewNoopSessionStore() *NoopSessionStore {
	return &NoopSessionStore{}
}

func (s *NoopSessionStore) AppendMessages(_ context.Context, _ string, _ ...llm.Message) error {
	return nil
}

func (s *NoopSessionStore) LoadSession(_ context.Context, sessionID string) ([]llm.Message, error) {
	return nil, fmt.Errorf("session persistence is disabled; cannot load session %q", sessionID)
}

func (s *NoopSessionStore) SessionExists(_ string) bool {
	return false
}
