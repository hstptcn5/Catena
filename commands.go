package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
)

var (
	ErrCommandNotFound     = errors.New("command not found")
	ErrCommandValidation   = errors.New("command validation failed")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrReceiptNotFound     = errors.New("receipt not found")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

//go:embed migrations/001_named_command_receipts.sql
var commandReceiptMigration string

type CommandFile struct {
	Version  int                          `yaml:"version"`
	Commands map[string]CommandDefinition `yaml:"commands"`
}

type CommandDefinition struct {
	Version     int                   `yaml:"version"`
	Description string                `yaml:"description"`
	Permission  string                `yaml:"permission"`
	Idempotency CommandIdempotency    `yaml:"idempotency"`
	Input       map[string]InputField `yaml:"input"`
	Statements  []CommandStatement    `yaml:"statements"`
	Result      *CommandResult        `yaml:"result"`
	Emit        []CommandEmit         `yaml:"emit"`
}

type CommandIdempotency struct {
	Required bool          `yaml:"required"`
	TTL      time.Duration `yaml:"-"`
	TTLText  string        `yaml:"ttl"`
}

type InputField struct {
	Type     string   `yaml:"type"`
	Required bool     `yaml:"required"`
	Minimum  *float64 `yaml:"minimum"`
	Maximum  *float64 `yaml:"maximum"`
}

type CommandStatement struct {
	SQL string `yaml:"sql"`
}

type CommandResult struct {
	SQL string `yaml:"sql"`
}

type CommandEmit struct {
	EventID     string         `yaml:"event_id"`
	Topic       string         `yaml:"topic"`
	Destination string         `yaml:"destination"`
	Payload     map[string]any `yaml:"payload"`
}

type CommandAuthFile struct {
	Version int                         `yaml:"version"`
	Tokens  map[string]CommandTokenFile `yaml:"tokens"`
}

type CommandTokenFile struct {
	TokenEnv    string   `yaml:"token_env"`
	ActorID     string   `yaml:"actor_id"`
	Permissions []string `yaml:"permissions"`
}

type CommandActor struct {
	ID          string
	Permissions map[string]struct{}
}

type commandCredential struct {
	token string
	actor CommandActor
}

type CommandAuthorizer struct {
	credentials []commandCredential
}

type CommandRegistry struct {
	definitions map[string]CommandDefinition
}

type ExecuteCommandRequest struct {
	CommandName    string
	IdempotencyKey string
	CorrelationID  string
	Actor          CommandActor
	Input          map[string]any
}

type CommandReceipt struct {
	CommandID        string         `json:"command_id"`
	CommandName      string         `json:"command_name"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
	DatabaseStatus   string         `json:"database_status"`
	DeliveryStatus   string         `json:"delivery_status"`
	Complete         bool           `json:"complete"`
	Result           map[string]any `json:"result,omitempty"`
	Events           []ReceiptEvent `json:"events"`
	CreatedAt        string         `json:"created_at"`
	CommittedAt      string         `json:"committed_at,omitempty"`
	CompletedAt      string         `json:"completed_at,omitempty"`
	ReceiptURL       string         `json:"receipt_url"`
	IdempotentReplay bool           `json:"idempotent_replay,omitempty"`
}

type ReceiptEvent struct {
	EventID       string  `json:"event_id"`
	DeliveryID    string  `json:"delivery_id"`
	Topic         string  `json:"topic"`
	Destination   string  `json:"destination"`
	Status        string  `json:"status"`
	Generation    int     `json:"generation"`
	Attempts      int     `json:"attempts"`
	CommandID     string  `json:"command_id"`
	CorrelationID string  `json:"correlation_id"`
	CausationID   string  `json:"causation_id"`
	DeliveredAt   *string `json:"delivered_at,omitempty"`
	LastErrorCode *string `json:"last_error_code,omitempty"`
}

type CommandExecutor struct {
	db              *DB
	registry        *CommandRegistry
	maxResultBytes  int
	maxPayloadBytes int
	now             func() time.Time
}

func LoadCommandRegistry(path string) (*CommandRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read commands file: %w", err)
	}
	return ParseCommandRegistry(data)
}

func ParseCommandRegistry(data []byte) (*CommandRegistry, error) {
	var file CommandFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode commands file: %w", err)
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported commands file version %d", file.Version)
	}
	if len(file.Commands) == 0 {
		return nil, errors.New("commands file must define at least one command")
	}
	for name, definition := range file.Commands {
		if !identifierPattern.MatchString(name) {
			return nil, fmt.Errorf("invalid command name %q", name)
		}
		if definition.Version == 0 {
			definition.Version = 1
		}
		if definition.Permission == "" || !identifierPattern.MatchString(definition.Permission) {
			return nil, fmt.Errorf("command %q must define a valid permission", name)
		}
		if !definition.Idempotency.Required {
			return nil, fmt.Errorf("command %q must require idempotency in Catena Node mode", name)
		}
		if definition.Idempotency.TTLText != "" {
			ttl, err := time.ParseDuration(definition.Idempotency.TTLText)
			if err != nil || ttl <= 0 {
				return nil, fmt.Errorf("command %q has invalid idempotency.ttl", name)
			}
			definition.Idempotency.TTL = ttl
		}
		if len(definition.Statements) == 0 {
			return nil, fmt.Errorf("command %q must define at least one statement", name)
		}
		for fieldName, field := range definition.Input {
			if !identifierPattern.MatchString(fieldName) {
				return nil, fmt.Errorf("command %q has invalid input field %q", name, fieldName)
			}
			switch field.Type {
			case "string", "integer", "number", "boolean":
			default:
				return nil, fmt.Errorf("command %q field %q has unsupported type %q", name, fieldName, field.Type)
			}
			if field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
				return nil, fmt.Errorf("command %q field %q has minimum greater than maximum", name, fieldName)
			}
		}
		for i, statement := range definition.Statements {
			if strings.TrimSpace(statement.SQL) == "" {
				return nil, fmt.Errorf("command %q statement %d is empty", name, i)
			}
			if kind, err := ClassifySQL(statement.SQL); err != nil || kind != SQLWrite {
				return nil, fmt.Errorf("command %q statement %d must be one bound write statement", name, i)
			}
		}
		if definition.Result != nil && strings.TrimSpace(definition.Result.SQL) != "" {
			if kind, err := ClassifySQL(definition.Result.SQL); err != nil || kind != SQLRead {
				return nil, fmt.Errorf("command %q result must be one read statement", name)
			}
		}
		for i, event := range definition.Emit {
			if !identifierPattern.MatchString(event.Topic) {
				return nil, fmt.Errorf("command %q event %d has invalid topic", name, i)
			}
			if !identifierPattern.MatchString(event.Destination) {
				return nil, fmt.Errorf("command %q event %d has invalid destination", name, i)
			}
		}
		file.Commands[name] = definition
	}
	return &CommandRegistry{definitions: file.Commands}, nil
}

func LoadCommandAuthorizer(path string, lookup func(string) (string, bool)) (*CommandAuthorizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read command auth file: %w", err)
	}
	return ParseCommandAuthorizer(data, lookup)
}

func ParseCommandAuthorizer(data []byte, lookup func(string) (string, bool)) (*CommandAuthorizer, error) {
	var file CommandAuthFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode command auth file: %w", err)
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported command auth version %d", file.Version)
	}
	if len(file.Tokens) == 0 {
		return nil, errors.New("command auth must define at least one token")
	}
	authorizer := &CommandAuthorizer{}
	for alias, item := range file.Tokens {
		if !identifierPattern.MatchString(alias) {
			return nil, fmt.Errorf("invalid command token alias %q", alias)
		}
		if item.TokenEnv == "" {
			return nil, fmt.Errorf("command token %q token_env must not be empty", alias)
		}
		token, ok := lookup(item.TokenEnv)
		if !ok || token == "" {
			return nil, fmt.Errorf("command token environment variable %s is not set or empty", item.TokenEnv)
		}
		if item.ActorID == "" {
			return nil, fmt.Errorf("command token %q actor_id must not be empty", alias)
		}
		permissions := make(map[string]struct{}, len(item.Permissions))
		for _, permission := range item.Permissions {
			if !identifierPattern.MatchString(permission) {
				return nil, fmt.Errorf("command token %q has invalid permission %q", alias, permission)
			}
			permissions[permission] = struct{}{}
		}
		authorizer.credentials = append(authorizer.credentials, commandCredential{
			token: token,
			actor: CommandActor{ID: item.ActorID, Permissions: permissions},
		})
	}
	return authorizer, nil
}

func (a *CommandAuthorizer) Authenticate(token string) (CommandActor, bool) {
	var actor CommandActor
	matched := 0
	tokenHash := sha256.Sum256([]byte(token))
	for _, credential := range a.credentials {
		credentialHash := sha256.Sum256([]byte(credential.token))
		equal := subtle.ConstantTimeCompare(tokenHash[:], credentialHash[:])
		if equal == 1 && matched == 0 {
			actor = credential.actor
		}
		matched |= equal
	}
	return actor, matched == 1
}

func (a CommandActor) Can(permission string) bool {
	_, ok := a.Permissions[permission]
	return ok
}

func (r *CommandRegistry) Definition(name string) (CommandDefinition, bool) {
	definition, ok := r.definitions[name]
	return definition, ok
}

func (r *CommandRegistry) Names() []string {
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func NewCommandExecutor(db *DB, registry *CommandRegistry, maxResultBytes, maxPayloadBytes int) (*CommandExecutor, error) {
	if db == nil || registry == nil {
		return nil, errors.New("database and command registry are required")
	}
	if maxResultBytes <= 0 {
		maxResultBytes = 256 << 10
	}
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = 1 << 20
	}
	executor := &CommandExecutor{
		db: db, registry: registry, maxResultBytes: maxResultBytes,
		maxPayloadBytes: maxPayloadBytes, now: time.Now,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := executor.initialize(ctx); err != nil {
		return nil, err
	}
	return executor, nil
}

func (e *CommandExecutor) initialize(ctx context.Context) error {
	required := map[string]bool{
		"id": false, "topic": false, "destination": false, "payload": false,
		"status": false, "generation": false, "attempts": false,
		"command_id": false, "correlation_id": false, "causation_id": false,
	}
	rows, err := e.db.sqliteDB.QueryContext(ctx, `PRAGMA table_info(anetac_outbox)`)
	if err != nil {
		return fmt.Errorf("inspect Anetac outbox: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect Anetac outbox column: %w", err)
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("Anetac outbox is missing required column %s; initialize a compatible Anetac schema first", name)
		}
	}
	if _, err := e.db.sqliteDB.ExecContext(ctx, commandReceiptMigration); err != nil {
		return fmt.Errorf("initialize command receipt schema: %w", err)
	}
	return nil
}

func (e *CommandExecutor) Execute(ctx context.Context, request ExecuteCommandRequest) (CommandReceipt, error) {
	definition, ok := e.registry.Definition(request.CommandName)
	if !ok {
		return CommandReceipt{}, ErrCommandNotFound
	}
	if !request.Actor.Can(definition.Permission) {
		return CommandReceipt{}, fmt.Errorf("%w: permission %s is required", ErrCommandValidation, definition.Permission)
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return CommandReceipt{}, fmt.Errorf("%w: Idempotency-Key is required", ErrCommandValidation)
	}
	if len(request.IdempotencyKey) > 255 {
		return CommandReceipt{}, fmt.Errorf("%w: Idempotency-Key is too long", ErrCommandValidation)
	}
	normalized, err := validateCommandInput(definition, request.Input)
	if err != nil {
		return CommandReceipt{}, err
	}
	fingerprint, err := requestFingerprint(request.CommandName, definition.Version, request.Actor.ID, normalized)
	if err != nil {
		return CommandReceipt{}, err
	}

	e.db.writeMu.Lock()
	defer e.db.writeMu.Unlock()

	tx, err := e.db.sqliteDB.BeginTx(ctx, nil)
	if err != nil {
		return CommandReceipt{}, fmt.Errorf("begin command transaction: %w", err)
	}
	defer tx.Rollback()

	var existingID, existingFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT command_id,request_fingerprint
		FROM catena_command_receipts
		WHERE command_name=? AND idempotency_key=?`,
		request.CommandName, request.IdempotencyKey).Scan(&existingID, &existingFingerprint)
	if err == nil {
		if !constantTimeStringEqual(existingFingerprint, fingerprint) {
			return CommandReceipt{}, ErrIdempotencyConflict
		}
		if err := tx.Rollback(); err != nil {
			return CommandReceipt{}, fmt.Errorf("close idempotent command transaction: %w", err)
		}
		receipt, err := e.GetReceipt(ctx, existingID)
		if err == nil {
			receipt.IdempotentReplay = true
		}
		return receipt, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CommandReceipt{}, fmt.Errorf("check command idempotency: %w", err)
	}

	now := e.now().UTC()
	commandID := "cmd_" + uuid.NewString()
	correlationID := strings.TrimSpace(request.CorrelationID)
	if correlationID == "" {
		correlationID = commandID
	}
	if len(correlationID) > 255 {
		return CommandReceipt{}, fmt.Errorf("%w: correlation ID is too long", ErrCommandValidation)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO catena_command_receipts(
			command_id,command_name,idempotency_key,request_fingerprint,actor_id,permission,
			database_status,created_at_ms
		) VALUES(?,?,?,?,?,?,?,?)`,
		commandID, request.CommandName, request.IdempotencyKey, fingerprint, request.Actor.ID,
		definition.Permission, "committed", now.UnixMilli()); err != nil {
		return CommandReceipt{}, fmt.Errorf("create command receipt: %w", err)
	}

	args := namedArguments(normalized, map[string]any{
		"command_id": commandID, "correlation_id": correlationID, "causation_id": commandID,
	})
	for i, statement := range definition.Statements {
		if _, err := tx.ExecContext(ctx, statement.SQL, args...); err != nil {
			return CommandReceipt{}, fmt.Errorf("execute command statement %d: %w", i, err)
		}
	}

	result := map[string]any{}
	if definition.Result != nil && strings.TrimSpace(definition.Result.SQL) != "" {
		result, err = querySingleResult(ctx, tx, definition.Result.SQL, args)
		if err != nil {
			return CommandReceipt{}, fmt.Errorf("query command result: %w", err)
		}
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return CommandReceipt{}, fmt.Errorf("encode command result: %w", err)
	}
	if len(resultJSON) > e.maxResultBytes {
		return CommandReceipt{}, fmt.Errorf("%w: command result exceeds configured limit", ErrCommandValidation)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE catena_command_receipts SET result_json=?,committed_at_ms=? WHERE command_id=?`,
		string(resultJSON), now.UnixMilli(), commandID); err != nil {
		return CommandReceipt{}, fmt.Errorf("store command result: %w", err)
	}

	for i, emitted := range definition.Emit {
		eventID := renderEventID(emitted.EventID, commandID, i)
		payload := renderPayload(emitted.Payload, normalized, map[string]any{
			"command_id": commandID, "correlation_id": correlationID, "causation_id": commandID,
		})
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return CommandReceipt{}, fmt.Errorf("encode event %d payload: %w", i, err)
		}
		if len(payloadJSON) > e.maxPayloadBytes {
			return CommandReceipt{}, fmt.Errorf("%w: event payload exceeds limit", ErrCommandValidation)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO anetac_outbox(
				id,topic,destination,payload,status,generation,attempts,created_at_ms,available_at_ms,
				command_id,correlation_id,causation_id,updated_at_ms
			) VALUES(?,?,?,?, 'pending',0,0,?,?,?,?,?,?)`,
			eventID, emitted.Topic, emitted.Destination, string(payloadJSON), now.UnixMilli(), now.UnixMilli(),
			commandID, correlationID, commandID, now.UnixMilli()); err != nil {
			return CommandReceipt{}, fmt.Errorf("create outbox event %d: %w", i, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO catena_command_events(command_id,event_id,topic,destination,created_at_ms)
			VALUES(?,?,?,?,?)`, commandID, eventID, emitted.Topic, emitted.Destination, now.UnixMilli()); err != nil {
			return CommandReceipt{}, fmt.Errorf("link outbox event %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return CommandReceipt{}, fmt.Errorf("commit command: %w", err)
	}
	return e.GetReceipt(ctx, commandID)
}

func (e *CommandExecutor) GetReceipt(ctx context.Context, commandID string) (CommandReceipt, error) {
	var receipt CommandReceipt
	var resultJSON sql.NullString
	var createdAt, committedAt int64
	var completedAt sql.NullInt64
	err := e.db.sqliteDB.QueryRowContext(ctx, `
		SELECT command_id,command_name,idempotency_key,database_status,result_json,
		       created_at_ms,committed_at_ms,completed_at_ms
		FROM catena_command_receipts WHERE command_id=?`, commandID).Scan(
		&receipt.CommandID, &receipt.CommandName, &receipt.IdempotencyKey, &receipt.DatabaseStatus,
		&resultJSON, &createdAt, &committedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandReceipt{}, ErrReceiptNotFound
	}
	if err != nil {
		return CommandReceipt{}, fmt.Errorf("read command receipt: %w", err)
	}
	if resultJSON.Valid && resultJSON.String != "" {
		if err := json.Unmarshal([]byte(resultJSON.String), &receipt.Result); err != nil {
			return CommandReceipt{}, fmt.Errorf("decode stored command result: %w", err)
		}
	}
	receipt.CreatedAt = formatMillis(createdAt)
	receipt.CommittedAt = formatMillis(committedAt)
	receipt.ReceiptURL = "/v1/receipts/" + receipt.CommandID

	rows, err := e.db.sqliteDB.QueryContext(ctx, `
		SELECT o.id,o.topic,o.destination,o.status,o.generation,o.attempts,o.command_id,
		       o.correlation_id,o.causation_id,o.delivered_at_ms,o.last_error_code,o.updated_at_ms
		FROM catena_command_events ce
		JOIN anetac_outbox o ON o.id=ce.event_id
		WHERE ce.command_id=?
		ORDER BY ce.created_at_ms,ce.event_id`, commandID)
	if err != nil {
		return CommandReceipt{}, fmt.Errorf("read receipt events: %w", err)
	}
	defer rows.Close()
	var terminalAt int64
	for rows.Next() {
		var event ReceiptEvent
		var deliveredAt, updatedAt sql.NullInt64
		if err := rows.Scan(
			&event.EventID, &event.Topic, &event.Destination, &event.Status, &event.Generation,
			&event.Attempts, &event.CommandID, &event.CorrelationID, &event.CausationID,
			&deliveredAt, &event.LastErrorCode, &updatedAt,
		); err != nil {
			return CommandReceipt{}, fmt.Errorf("scan receipt event: %w", err)
		}
		event.DeliveryID = fmt.Sprintf("%s:%d", event.EventID, event.Generation)
		if deliveredAt.Valid {
			value := formatMillis(deliveredAt.Int64)
			event.DeliveredAt = &value
		}
		if updatedAt.Valid && updatedAt.Int64 > terminalAt {
			terminalAt = updatedAt.Int64
		}
		receipt.Events = append(receipt.Events, event)
	}
	if err := rows.Err(); err != nil {
		return CommandReceipt{}, fmt.Errorf("read receipt events: %w", err)
	}
	receipt.DeliveryStatus, receipt.Complete = aggregateDeliveryState(receipt.Events)
	if completedAt.Valid {
		receipt.CompletedAt = formatMillis(completedAt.Int64)
	} else if receipt.Complete {
		if terminalAt == 0 {
			terminalAt = committedAt
		}
		receipt.CompletedAt = formatMillis(terminalAt)
		_, _ = e.db.sqliteDB.ExecContext(ctx,
			`UPDATE catena_command_receipts SET completed_at_ms=? WHERE command_id=? AND completed_at_ms IS NULL`,
			terminalAt, commandID)
	}
	if receipt.Events == nil {
		receipt.Events = []ReceiptEvent{}
	}
	return receipt, nil
}

func validateCommandInput(definition CommandDefinition, input map[string]any) (map[string]any, error) {
	if input == nil {
		input = map[string]any{}
	}
	for name := range input {
		if _, ok := definition.Input[name]; !ok {
			return nil, fmt.Errorf("%w: unknown field %q", ErrCommandValidation, name)
		}
	}
	normalized := make(map[string]any, len(definition.Input))
	for name, field := range definition.Input {
		value, present := input[name]
		if !present || value == nil {
			if field.Required {
				return nil, fmt.Errorf("%w: field %q is required", ErrCommandValidation, name)
			}
			normalized[name] = nil
			continue
		}
		switch field.Type {
		case "string":
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("%w: field %q must be a string", ErrCommandValidation, name)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("%w: field %q must be a boolean", ErrCommandValidation, name)
			}
		case "integer":
			number, ok := value.(json.Number)
			if !ok {
				return nil, fmt.Errorf("%w: field %q must be an integer", ErrCommandValidation, name)
			}
			integer, err := number.Int64()
			if err != nil {
				return nil, fmt.Errorf("%w: field %q must be an integer", ErrCommandValidation, name)
			}
			value = integer
			if err := validateBounds(name, float64(integer), field); err != nil {
				return nil, err
			}
		case "number":
			number, ok := value.(json.Number)
			if !ok {
				return nil, fmt.Errorf("%w: field %q must be a number", ErrCommandValidation, name)
			}
			numeric, err := number.Float64()
			if err != nil {
				return nil, fmt.Errorf("%w: field %q must be a valid number", ErrCommandValidation, name)
			}
			value = numeric
			if err := validateBounds(name, numeric, field); err != nil {
				return nil, err
			}
		}
		normalized[name] = value
	}
	return normalized, nil
}

func validateBounds(name string, value float64, field InputField) error {
	if field.Minimum != nil && value < *field.Minimum {
		return fmt.Errorf("%w: field %q is below its minimum", ErrCommandValidation, name)
	}
	if field.Maximum != nil && value > *field.Maximum {
		return fmt.Errorf("%w: field %q is above its maximum", ErrCommandValidation, name)
	}
	return nil
}

func requestFingerprint(commandName string, version int, actorID string, input map[string]any) (string, error) {
	canonical, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("canonicalize command input: %w", err)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n%d\n%s\n", commandName, version, actorID)
	hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func namedArguments(input map[string]any, metadata map[string]any) []any {
	names := make([]string, 0, len(input)+len(metadata))
	for name := range input {
		names = append(names, name)
	}
	for name := range metadata {
		names = append(names, name)
	}
	sort.Strings(names)
	args := make([]any, 0, len(names))
	for _, name := range names {
		value, ok := input[name]
		if !ok {
			value = metadata[name]
		}
		args = append(args, sql.Named(name, value))
	}
	return args
}

func querySingleResult(ctx context.Context, tx *sql.Tx, query string, args []any) (map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return map[string]any{}, rows.Err()
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(columns))
	for i, value := range values {
		if raw, ok := value.([]byte); ok {
			value = string(raw)
		}
		result[columns[i]] = value
	}
	if rows.Next() {
		return nil, errors.New("command result returned more than one row")
	}
	return result, rows.Err()
}

func renderEventID(template, commandID string, index int) string {
	if template == "" {
		return fmt.Sprintf("evt_%s_%d", strings.TrimPrefix(commandID, "cmd_"), index)
	}
	rendered := strings.ReplaceAll(template, ":command_id", commandID)
	if rendered == template && len(rendered) > 0 {
		return rendered
	}
	return rendered
}

func renderPayload(payload map[string]any, input, metadata map[string]any) map[string]any {
	rendered := make(map[string]any, len(payload))
	for key, value := range payload {
		rendered[key] = renderTemplateValue(value, input, metadata)
	}
	return rendered
}

func renderTemplateValue(value any, input, metadata map[string]any) any {
	switch item := value.(type) {
	case string:
		if strings.HasPrefix(item, ":") {
			name := strings.TrimPrefix(item, ":")
			if resolved, ok := input[name]; ok {
				return resolved
			}
			if resolved, ok := metadata[name]; ok {
				return resolved
			}
		}
		return item
	case map[string]any:
		return renderPayload(item, input, metadata)
	case []any:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = renderTemplateValue(child, input, metadata)
		}
		return result
	default:
		return item
	}
}

func aggregateDeliveryState(events []ReceiptEvent) (string, bool) {
	if len(events) == 0 {
		return "none", true
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Status]++
	}
	if counts["dead"] > 0 && counts["pending"] == 0 && counts["processing"] == 0 {
		return "dead", true
	}
	if counts["dead"] > 0 {
		return "dead", false
	}
	if counts["delivered"] == len(events) {
		return "delivered", true
	}
	if counts["delivered"] > 0 {
		return "partially_delivered", false
	}
	if counts["processing"] > 0 {
		return "processing", false
	}
	return "pending", false
}

func constantTimeStringEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func formatMillis(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339Nano)
}
