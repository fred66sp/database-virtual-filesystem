package memory

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Store is the memory of one project over one db-ai mount.
type Store struct {
	db      dbai
	Alias   string
	AgentID string
	// Env resolves OPENCODE_SESSION_ID when an operation names no session.
	Env func(string) string
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// New builds a store. The zero alias and agent id fall back to the values the
// Python adapter used.
func New(alias, agentID string, env func(string) string) *Store {
	if alias == "" {
		alias = "mem"
	}
	if agentID == "" {
		agentID = "opencode"
	}
	return &Store{
		Alias:   alias,
		AgentID: agentID,
		Env:     env,
		Now:     time.Now,
	}
}

// Row is one row of a find answer.
type Row struct {
	Path   string                   `json:"path"`
	ETag   string                   `json:"etag"`
	Fields map[string]RowValueField `json:"fields"`
}

// RowValueField is one typed field of a row. db-ai sends data as a JSON value;
// the schema of ACM only stores text and null.
type RowValueField struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// Values flattens a row to the text the core reads, as the Python adapter did.
func (r Row) Values() map[string]string {
	values := make(map[string]string, len(r.Fields))
	for name, field := range r.Fields {
		var text string
		if err := json.Unmarshal(field.Data, &text); err == nil {
			values[name] = text
			continue
		}
		values[name] = string(field.Data)
	}
	return values
}

// Session is the session row the core needs.
type Session struct {
	ID      string
	Goal    string
	Status  string
	Summary string
}

// Observation is the knowledge of one capture.
type Observation struct {
	Title        string
	Body         string
	Rationale    string
	Alternatives string
	Consequences string
	Topic        string
	Confidence   string
}

// ResolveSession applies the identity rule: the name given by the operation or
// OPENCODE_SESSION_ID, restricted to [A-Za-z0-9._-]{1,128}.
func (s *Store) ResolveSession(given string) (string, error) {
	session := given
	if session == "" && s.Env != nil {
		session = s.Env("OPENCODE_SESSION_ID")
	}
	if session == "" {
		return "", errorf(codeUsage, "falta --session (o la variable OPENCODE_SESSION_ID)")
	}
	if !sessionPattern.MatchString(session) {
		return "", errorf(codeUsage, "el identificador de sesión solo admite "+
			"[A-Za-z0-9._-]; p. ej. --session tarea-parser")
	}
	return session, nil
}

func (s *Store) now() string {
	return s.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func (s *Store) aliasPath(table string) string {
	return "/" + s.Alias + "/" + table
}

// rows is one bounded find: the filter runs before the limit, as db-ai fixes.
func (s *Store) rows(ctx context.Context, table string, wheres []string,
	order string, limit int) ([]Row, error) {
	args := []string{"find", s.aliasPath(table), "--limit", strconv.Itoa(limit)}
	for _, where := range wheres {
		args = append(args, "--where", where)
	}
	if order != "" {
		args = append(args, "--order", order)
	}
	data, err := s.db.call(ctx, args...)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Rows []Row `json:"rows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errorf(codeInternal, "respuesta de db-ai ilegible: %v", err)
	}
	return payload.Rows, nil
}

// loadSession reads a session or fails with E_NOT_FOUND, the message the
// Python adapter gave when the agent had not opened it.
func (s *Store) loadSession(ctx context.Context, session string) (Session, string, error) {
	found, err := s.rows(ctx, "sessions", []string{"id=" + session}, "", 1)
	if err != nil {
		return Session{}, "", err
	}
	if len(found) == 0 {
		return Session{}, "", errorf(codeNotFound,
			"la sesión '%s' no existe; ábrela con `acm start --session %s`", session, session)
	}
	values := found[0].Values()
	return Session{
		ID:      session,
		Goal:    values["goal"],
		Status:  values["status"],
		Summary: values["summary"],
	}, found[0].ETag, nil
}

// OpenSession requires an open session: a closed one is a state refusal.
func (s *Store) OpenSession(ctx context.Context, session string) (Session, error) {
	loaded, _, err := s.loadSession(ctx, session)
	if err != nil {
		return Session{}, err
	}
	if loaded.Status != "open" {
		return Session{}, errorf(codeConstraint,
			"la sesión '%s' está %s, no abierta; usa otro identificador con `acm start`",
			session, loaded.Status)
	}
	return loaded, nil
}

func (s *Store) touch(ctx context.Context, path string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return errorf(codeInternal, "no se pudo serializar la escritura: %v", err)
	}
	_, err = s.db.call(ctx, "touch", path, "--data", string(raw))
	return err
}

func (s *Store) write(ctx context.Context, path, etag string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return errorf(codeInternal, "no se pudo serializar la escritura: %v", err)
	}
	_, err = s.db.call(ctx, "write", path, "--if-match", etag, "--data", string(raw))
	return err
}

func (s *Store) batch(ctx context.Context, operations []any) error {
	raw, err := json.Marshal(operations)
	if err != nil {
		return errorf(codeInternal, "no se pudo serializar el lote: %v", err)
	}
	_, err = s.db.call(ctx, "batch", "--data", string(raw))
	return err
}

func requireText(value, name string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", errorf(codeUsage, "falta %s", name)
	}
	return text, nil
}

// RecordObservation captures knowledge, deduplicating by content hash and
// consolidating a repeat. It returns created or consolidated.
func (s *Store) RecordObservation(ctx context.Context, session, kind string,
	observation Observation) (string, error) {
	if _, err := s.OpenSession(ctx, session); err != nil {
		return "", err
	}
	title, err := requireText(observation.Title, "--title")
	if err != nil {
		return "", err
	}
	bodyName := "--body"
	if kind == "decision" {
		bodyName = "--decision"
	}
	body, err := requireText(observation.Body, bodyName)
	if err != nil {
		return "", err
	}
	content := map[string]any{
		"kind":         kind,
		"title":        title,
		"body":         body,
		"rationale":    strings.TrimSpace(observation.Rationale),
		"alternatives": strings.TrimSpace(observation.Alternatives),
		"consequences": strings.TrimSpace(observation.Consequences),
		"topic_key":    strings.TrimSpace(observation.Topic),
	}
	digest, err := ContentHash("observations", content)
	if err != nil {
		return "", err
	}
	timestamp := s.now()
	found, err := s.rows(ctx, "observations", []string{"content_hash=" + digest}, "", 1)
	if err != nil {
		return "", err
	}
	if len(found) > 0 {
		return s.consolidate(ctx, valuesID(found[0]), found[0], timestamp)
	}

	confidence := observation.Confidence
	if confidence == "" {
		confidence = "confirmed"
	}
	record := map[string]any{
		"kind": kind, "title": title, "body": body,
		"rationale":    content["rationale"],
		"alternatives": content["alternatives"],
		"consequences": content["consequences"],
		"topic_key":    content["topic_key"],
		"id":           newID(s.Now()),
		"session_id":   session, "agent_id": s.AgentID, "source": "agent",
		"confidence": confidence, "status": "active", "duplicate_count": 1,
		"last_seen_at": timestamp, "content_hash": digest,
		"created_at": timestamp, "updated_at": timestamp,
	}
	if touchErr := s.touch(ctx, s.aliasPath("observations"), record); touchErr != nil {
		var contract *Error
		if !errors.As(touchErr, &contract) || contract.Code != codeConstraint {
			return "", touchErr
		}
		// Another writer won the hash race: consolidate, do not retry the insert.
		found, err := s.rows(ctx, "observations", []string{"content_hash=" + digest}, "", 1)
		if err != nil {
			return "", err
		}
		if len(found) == 0 {
			return "", touchErr
		}
		return s.consolidate(ctx, valuesID(found[0]), found[0], timestamp)
	}
	return "created", nil
}

func valuesID(row Row) string {
	return row.Values()["id"]
}

// consolidate adds one repetition; on E_CONFLICT it rereads the row and
// retries, at most three times, as the Python adapter did.
func (s *Store) consolidate(ctx context.Context, id string, row Row, timestamp string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		values := row.Values()
		count, err := strconv.Atoi(values["duplicate_count"])
		if err != nil {
			return "", errorf(codeInternal, "duplicate_count ilegible: %v", err)
		}
		err = s.write(ctx, s.aliasPath("observations")+"/"+id, row.ETag, map[string]any{
			"duplicate_count": count + 1,
			"last_seen_at":    timestamp,
			"updated_at":      timestamp,
		})
		if err == nil {
			return "consolidated", nil
		}
		var contract *Error
		if !errors.As(err, &contract) || contract.Code != codeConflict || attempt == 2 {
			return "", err
		}
		// Another process consolidated first: the ETag is stale, reread the row.
		found, readErr := s.rows(ctx, "observations",
			[]string{"content_hash=" + values["content_hash"]}, "", 1)
		if readErr != nil {
			return "", readErr
		}
		if len(found) == 0 {
			return "", err
		}
		row = found[0]
	}
	return "", errorf(codeInternal, "consolidación sin intentos")
}

// SetPreference keeps one active preference per key. A repeated content is
// existing; a new value supersedes the active one in the same batch.
func (s *Store) SetPreference(ctx context.Context, session, key, value,
	rationale, origin string) (string, error) {
	if origin != "human" && origin != "agent" {
		return "", errorf(codeUsage, "origin debe ser human (dicho por el usuario) o agent")
	}
	if _, err := s.OpenSession(ctx, session); err != nil {
		return "", err
	}
	key, err := requireText(key, "--pref-key")
	if err != nil {
		return "", err
	}
	value, err = requireText(value, "--value")
	if err != nil {
		return "", err
	}
	rationale = strings.TrimSpace(rationale)
	digest, err := ContentHash("preferences", map[string]any{
		"pref_key": key, "value": value, "rationale": rationale,
	})
	if err != nil {
		return "", err
	}
	found, err := s.rows(ctx, "preferences", []string{"content_hash=" + digest}, "", 1)
	if err != nil {
		return "", err
	}
	if len(found) > 0 {
		return "existing", nil
	}

	timestamp := s.now()
	preferenceID := newID(s.Now())
	operations := []any{map[string]any{
		"op":   "touch",
		"path": s.aliasPath("preferences"),
		"data": map[string]any{
			"id": preferenceID, "session_id": session, "agent_id": s.AgentID,
			// An "origin" already uses the vocabulary of "source": human or agent.
			"source": origin, "pref_key": key, "value": value,
			"rationale": nullable(rationale), "status": "active",
			"superseded_by": nil, "content_hash": digest,
			"created_at": timestamp, "updated_at": timestamp,
		},
	}}
	active, err := s.rows(ctx, "preferences",
		[]string{"pref_key=" + key, "status=active"}, "created_at:desc,id:desc", 1)
	if err != nil {
		return "", err
	}
	if len(active) > 0 {
		operations = append(operations, map[string]any{
			"op":       "write",
			"path":     s.aliasPath("preferences") + "/" + valuesID(active[0]),
			"if_match": active[0].ETag,
			"data": map[string]any{
				"status": "superseded", "superseded_by": preferenceID,
				"updated_at": timestamp,
			},
		})
	}
	if batchErr := s.batch(ctx, operations); batchErr != nil {
		var contract *Error
		if !errors.As(batchErr, &contract) || contract.Code != codeConstraint {
			return "", batchErr
		}
		// Another writer won the same hash: it is the same content, not a retry.
		found, err := s.rows(ctx, "preferences", []string{"content_hash=" + digest}, "", 1)
		if err != nil {
			return "", err
		}
		if len(found) == 0 {
			return "", batchErr
		}
		return "existing", nil
	}
	return "created", nil
}

// nullable turns an empty optional text into a JSON null, as the Python
// adapter did for the columns that admit NULL.
func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

// checkpointBatch plans the new checkpoint and the supersession of the active
// one. An exact content_hash duplicate is a no-op: it returns no operations and
// the id of the existing row, because UNIQUE(content_hash) forbids it.
func (s *Store) checkpointBatch(ctx context.Context, session, goal, state,
	nextSteps, files, questions string) ([]any, string, error) {
	timestamp := s.now()
	digest, err := ContentHash("checkpoints", map[string]any{
		"goal": goal, "state": state, "next_steps": nextSteps,
		"files": files, "open_questions": questions,
	})
	if err != nil {
		return nil, "", err
	}
	existing, err := s.rows(ctx, "checkpoints", []string{"content_hash=" + digest}, "", 1)
	if err != nil {
		return nil, "", err
	}
	if len(existing) > 0 {
		return nil, valuesID(existing[0]), nil
	}
	checkpointID := newID(s.Now())
	operations := []any{map[string]any{
		"op":   "touch",
		"path": s.aliasPath("checkpoints"),
		"data": map[string]any{
			"id": checkpointID, "session_id": session, "agent_id": s.AgentID,
			"source": "agent", "goal": goal, "state": state,
			"next_steps": nextSteps, "files": nullable(files),
			"open_questions": nullable(questions), "status": "active",
			"content_hash": digest, "created_at": timestamp,
			"updated_at": timestamp,
		},
	}}
	active, err := s.rows(ctx, "checkpoints",
		[]string{"session_id=" + session, "status=active"}, "created_at:desc,id:desc", 1)
	if err != nil {
		return nil, "", err
	}
	if len(active) > 0 {
		operations = append(operations, map[string]any{
			"op":       "write",
			"path":     s.aliasPath("checkpoints") + "/" + valuesID(active[0]),
			"if_match": active[0].ETag,
			"data": map[string]any{
				"status": "superseded", "superseded_by": checkpointID,
				"updated_at": timestamp,
			},
		})
	}
	return operations, checkpointID, nil
}

// WriteCheckpoint stores a checkpoint and supersedes the active one in one
// db-ai transaction. It returns created or existing next to the checkpoint id.
func (s *Store) WriteCheckpoint(ctx context.Context, session, state, nextSteps,
	files, questions string) (string, string, error) {
	loaded, err := s.OpenSession(ctx, session)
	if err != nil {
		return "", "", err
	}
	state, err = requireText(state, "--state")
	if err != nil {
		return "", "", err
	}
	nextSteps, err = requireText(nextSteps, "--next-steps")
	if err != nil {
		return "", "", err
	}
	operations, checkpointID, err := s.checkpointBatch(ctx, session, loaded.Goal,
		state, nextSteps, strings.TrimSpace(files), strings.TrimSpace(questions))
	if err != nil {
		return "", "", err
	}
	if operations == nil {
		return "existing", checkpointID, nil
	}
	if err := s.batch(ctx, operations); err != nil {
		return "", "", err
	}
	return "created", checkpointID, nil
}

// CloseSession writes the final checkpoint and closes the session in the same
// transaction. A failed batch leaves the session open; it is never retried.
func (s *Store) CloseSession(ctx context.Context, session, summary, state,
	nextSteps, files, questions string) (string, string, error) {
	loaded, etag, err := s.loadSession(ctx, session)
	if err != nil {
		return "", "", err
	}
	if loaded.Status != "open" {
		return "", "", errorf(codeConstraint,
			"la sesión '%s' está %s, no abierta; usa otro identificador con `acm start`",
			session, loaded.Status)
	}
	summary, err = requireText(summary, "--summary")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(state) == "" {
		state = summary
	}
	if strings.TrimSpace(nextSteps) == "" {
		nextSteps = "ninguno"
	}
	operations, checkpointID, err := s.checkpointBatch(ctx, session, loaded.Goal,
		strings.TrimSpace(state), strings.TrimSpace(nextSteps),
		strings.TrimSpace(files), strings.TrimSpace(questions))
	if err != nil {
		return "", "", err
	}
	if operations == nil {
		operations = []any{}
	}
	timestamp := s.now()
	operations = append(operations, map[string]any{
		"op": "write", "path": s.aliasPath("sessions") + "/" + session,
		"if_match": etag,
		"data": map[string]any{
			"status": "closed", "ended_at": timestamp,
			"summary": summary, "updated_at": timestamp,
		},
	})
	if err := s.batch(ctx, operations); err != nil {
		return "", "", err
	}
	return timestamp, checkpointID, nil
}

// Start opens the session or restores it, and returns the continuity Markdown.
func (s *Store) Start(ctx context.Context, session, goal string) (string, error) {
	found, err := s.rows(ctx, "sessions", []string{"id=" + session}, "", 1)
	if err != nil {
		return "", err
	}
	if len(found) > 0 {
		values := found[0].Values()
		if values["status"] != "open" {
			return "", errorf(codeConstraint,
				"la sesión '%s' está %s; usa otro identificador", session, values["status"])
		}
		parts := []string{"Sesión restaurada."}
		stored := strings.TrimSpace(values["goal"])
		if stored != "" && stored != "sesión "+session {
			parts = append(parts, "Objetivo: "+stored)
		}
		active, err := s.rows(ctx, "checkpoints",
			[]string{"session_id=" + session, "status=active"}, "created_at:desc,id:desc", 1)
		if err != nil {
			return "", err
		}
		if len(active) > 0 {
			parts = append(parts, checkpointMarkdown("Estado de esta sesión", active[0].Values()))
		}
		continuity, err := s.Continuity(ctx)
		if err != nil {
			return "", err
		}
		if continuity != "" {
			parts = append(parts, continuity)
		}
		return strings.Join(parts, "\n\n"), nil
	}

	goal = strings.TrimSpace(goal)
	if goal == "" {
		goal = "sesión " + session
	}
	timestamp := s.now()
	sessionRecord := map[string]any{
		"id": session, "agent_id": s.AgentID, "source": "agent", "goal": goal,
		"status": "open", "started_at": timestamp, "ended_at": nil,
		"summary": nil, "created_at": timestamp, "updated_at": timestamp,
	}
	if err := s.touch(ctx, s.aliasPath("sessions"), sessionRecord); err != nil {
		return "", err
	}
	parts := []string{"Sesión iniciada."}
	if goal != "sesión "+session {
		parts = append(parts, "Objetivo: "+goal)
	}
	continuity, err := s.Continuity(ctx)
	if err != nil {
		return "", err
	}
	if continuity != "" {
		parts = append(parts, continuity)
	}
	return strings.Join(parts, "\n\n"), nil
}
