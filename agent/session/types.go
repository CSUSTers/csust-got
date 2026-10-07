package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Version identifies the archive and Redis state format.
const Version = 1

var (
	// ErrMiss reports that selection found no readable active session.
	ErrMiss = errors.New("session not found")
	// ErrCorrupt reports an invalid archive, chain, or metadata value.
	ErrCorrupt = errors.New("invalid session archive or chain")
	// ErrFence reports that the lease or DAG generation is no longer valid.
	ErrFence = errors.New("session lease or generation lost")
	// ErrClosed reports an operation attempted after Service.Close.
	ErrClosed = errors.New("session service closed")
	// ErrConflict reports exhausted optimistic transaction retries.
	ErrConflict = errors.New("session transaction conflict")
	// ErrUnknown requires retaining files until publication can be confirmed.
	ErrUnknown = errors.New("session publication outcome unknown; retained for recovery")
)

// Scope isolates storage by deployment, bot, platform, and chat.
type Scope struct {
	Namespace string `json:"namespace"`
	Bot       string `json:"bot"`
	Platform  string `json:"platform"`
	ChatID    int64  `json:"chat_id"`
}

// StorageNamespace derives the shared file namespace from the Redis prefix.
func StorageNamespace(redisPrefix string) string {
	h := sha256.Sum256([]byte(redisPrefix))
	return hex.EncodeToString(h[:])
}

// Key returns a collision-resistant encoding of the complete scope.
func (s Scope) Key() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Validate checks the required scope fields and namespace encoding.
func (s Scope) Validate() error {
	if len(s.Namespace) != 64 || !hexID(s.Namespace) || s.Bot == "" || s.Platform == "" {
		return fmt.Errorf("%w: scope", ErrCorrupt)
	}
	return nil
}

// SelectionMode determines how a parent node is selected.
type SelectionMode string

// Selection modes disable loading, select an exact reply, or select the agent's latest commit.
const (
	SelectNone   SelectionMode = "none"
	SelectReply  SelectionMode = "reply"
	SelectLatest SelectionMode = "latest"
)

// Selection identifies the requested scope and parent lookup.
type Selection struct {
	Scope          Scope
	Agent          string
	Mode           SelectionMode
	ReplyMessageID int
}

// NodeRef identifies one immutable node within its DAG.
type NodeRef struct {
	DAGID  string `json:"dag_id"`
	NodeID string `json:"node_id"`
}

// Validate checks both identifiers against the generated ID format.
func (r NodeRef) Validate() error {
	if !ValidID(r.DAGID) || !ValidID(r.NodeID) {
		return ErrCorrupt
	}
	return nil
}

// NewID returns a cryptographically random, path-safe identifier.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func hexID(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidID reports whether s has the generated identifier format.
func ValidID(s string) bool { return len(s) == 32 && hexID(s) }

// Source classifies messages without interpreting their content.
type Source string

// Message sources distinguish replayable history from replaceable frame and guidance.
const (
	SourceHistory  Source = "history"
	SourceFrame    Source = "frame"
	SourceGuidance Source = "guidance"
)

// Record archives a complete message with its explicit source.
type Record struct {
	Source  Source          `json:"source"`
	Message *schema.Message `json:"message"`
}

// History labels messages as replayable history; Snapshot detaches their values.
func History(messages ...*schema.Message) []Record {
	r := make([]Record, len(messages))
	for i, m := range messages {
		r[i] = Record{Source: SourceHistory, Message: m}
	}
	return r
}

// TurnCapture contains the invocation frame, root baseline, and new message delta.
type TurnCapture struct {
	Frame     []Record `json:"frame,omitempty"`
	Bootstrap []Record `json:"bootstrap,omitempty"`
	Delta     []Record `json:"delta"`
	Complete  bool     `json:"complete"`
}

// DeliveryReceipt lists the final bot messages successfully delivered for this turn.
type DeliveryReceipt struct {
	MessageIDs []int `json:"message_ids"`
}

// Node is immutable committed metadata for one archive and its optional parent.
type Node struct {
	Scope           Scope    `json:"scope"`
	Ref             NodeRef  `json:"ref"`
	Parent          *NodeRef `json:"parent,omitempty"`
	Agent           string   `json:"agent"`
	RunID           string   `json:"run_id"`
	ReplyMessageIDs []int    `json:"reply_message_ids,omitempty"`
	FileName        string   `json:"file_name"`
	Digest          string   `json:"digest"`
	Size            int64    `json:"size"`
	Version         int      `json:"version"`
	CommitSequence  int64    `json:"commit_sequence"`
}

// Lease pins a DAG with a generation-fenced token and Redis millisecond deadline.
type Lease struct {
	DAGID      string `json:"dag_id"`
	Generation string `json:"generation"`
	Token      string `json:"token"`
	Deadline   int64  `json:"deadline"`
}

// Intent durably records an unpublished or recoverable file attempt.
type Intent struct {
	Node   Node   `json:"node"`
	Lease  Lease  `json:"lease"`
	Status string `json:"status"`
}

// Reservation requests a new root or a child under a pinned loaded parent.
type Reservation struct {
	Scope  Scope
	Agent  string
	RunID  string
	Parent *NodeRef
	Lease  *Lease
}

// Pinned contains root-to-selected ancestor metadata protected by a lease.
type Pinned struct {
	Nodes []Node
	Lease Lease
}

// Deletion is the frozen manifest of an irreversibly deleting DAG.
type Deletion struct {
	DAGID      string
	Generation string
	Nodes      []Node
	Intents    []Intent
}

// Repository atomically maintains lifecycle and reference indexes using Redis time.
type Repository interface {
	Namespace() string
	ResolveAndPin(context.Context, Selection, string, time.Duration) (Pinned, error)
	ConfirmLoaded(context.Context, Scope, Lease, time.Duration) error
	Renew(context.Context, Scope, Lease, time.Duration) error
	Release(context.Context, Scope, Lease) error
	Reserve(context.Context, Reservation, time.Duration) (Intent, error)
	Publish(context.Context, Scope, Intent, string, int64, DeliveryReceipt) (Node, error)
	GetPublication(context.Context, Scope, string) (*Node, error)
	AbortIntent(context.Context, Scope, Intent) (bool, error)
	FinishIntent(context.Context, Scope, Intent) error
	Scopes(context.Context) ([]Scope, error)
	Pending(context.Context, Scope) ([]Intent, error)
	Deleting(context.Context, Scope) ([]Deletion, error)
	ClaimDeleting(context.Context, Scope, time.Duration) ([]Deletion, error)
	FinishDelete(context.Context, Scope, Deletion) error
}

// CommitRequest combines a complete capture, delivery proof, and optional loaded parent.
type CommitRequest struct {
	Scope   Scope
	Agent   string
	RunID   string
	Parent  *LoadedParent
	Capture TurnCapture
	Receipt DeliveryReceipt
}

// LoadResult provides complete replay and its opaque, renewable parent proof.
type LoadResult struct {
	Messages []*schema.Message
	Parent   *LoadedParent
}
