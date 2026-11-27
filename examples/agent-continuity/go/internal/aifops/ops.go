// Package aifops serves the AIF 0.2 operations of ACM: one operation per memory
// event, each delegating in the same core the CLI uses. Discovery and the
// response envelope belong to the AIF library.
package aifops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	interaction "github.com/fred66sp/agent-interaction-forms"
	"github.com/fred66sp/database-virtual-filesystem/acm/internal/memory"
)

const (
	fieldSession       = "session"
	fieldTopic         = "topic"
	fieldTitle         = "title"
	fieldBody          = "body"
	fieldDecision      = "decision"
	fieldRationale     = "rationale"
	fieldAlternatives  = "alternatives"
	fieldConsequences  = "consequences"
	fieldConfidence    = "confidence"
	fieldPrefKey       = "pref_key"
	fieldValue         = "value"
	fieldOrigin        = "origin"
	fieldQuery         = "query"
	fieldLimit         = "limit"
	fieldState         = "state"
	fieldNextSteps     = "next_steps"
	fieldFiles         = "files"
	fieldOpenQuestions = "open_questions"
	fieldSummary       = "summary"
	textType           = "string"
)

// Confidences is the closed set of the schema.
var Confidences = []string{"confirmed", "inference", "hypothesis", "proposal", "unknown"}

// New builds the ten operations of the memory over one project.
func New(ctx context.Context, store *memory.Store) *interaction.Executor {
	return &interaction.Executor{Operations: []*interaction.Operation{
		decisionOperation(ctx, store),
		recordOperation(ctx, store, "learning",
			"Registra un aprendizaje reutilizable: cómo funciona algo o qué se aprendió al resolverlo."),
		recordOperation(ctx, store, "discovery",
			"Registra un hallazgo del repositorio o de una fuente externa que merezca conservarse."),
		recordOperation(ctx, store, "bugfix",
			"Registra un defecto resuelto con su causa y su corrección."),
		recordOperation(ctx, store, "pattern",
			"Registra un patrón reutilizable del proyecto."),
		recordOperation(ctx, store, "architecture",
			"Registra una decisión o un hecho de arquitectura del proyecto."),
		preferenceOperation(ctx, store),
		recallOperation(ctx, store),
		checkpointOperation(ctx, store),
		closeOperation(ctx, store),
	}}
}

// writeOutcome is the minimal answer of a write: whether it happened and, when
// it applies, the session, the checkpoint and the closing time.
type writeOutcome struct {
	Result     string `json:"result"`
	Session    string `json:"session"`
	Checkpoint string `json:"checkpoint,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

func decisionOperation(ctx context.Context, store *memory.Store) *interaction.Operation {
	return recordOperationWithFields(ctx, store, "decision",
		"Registra una decisión con su motivo; topic y rationale son obligatorios.",
		[]interaction.Field{
			requiredText(fieldTopic, "el tema de la decisión, en la forma familia/descripcion"),
			requiredText(fieldTitle, "el título de la decisión"),
			requiredText(fieldDecision, "el enunciado de la decisión"),
			requiredText(fieldRationale, "el motivo de la decisión"),
			optionalText(fieldAlternatives, "las alternativas consideradas"),
			optionalText(fieldConsequences, "las consecuencias de la decisión"),
			confidenceField(),
			sessionField(),
		})
}

func recordOperation(ctx context.Context, store *memory.Store, kind, description string) *interaction.Operation {
	// Paridad con tools/acm.py: bugfix (EVENT_FORMS acm.py:104) y pattern/
	// architecture (vía remember --rationale) alimentan content["rationale"]
	// y con ello content_hash; sin el campo, un reenvío Go de un recuerdo
	// escrito por Python divergería y crearía fila nueva en vez de consolidar.
	fields := []interaction.Field{
		requiredText(fieldTitle, "el título del recuerdo"),
		requiredText(fieldBody, "el cuerpo del recuerdo"),
		optionalText(fieldTopic, "el tema del recuerdo, en la forma familia/descripcion"),
	}
	fields = append(fields, confidenceField())
	if kind == "bugfix" || kind == "pattern" || kind == "architecture" {
		fields = append(fields, optionalText(fieldRationale, "el motivo del recuerdo"))
	}
	fields = append(fields, sessionField())
	return recordOperationWithFields(ctx, store, kind, description, fields)
}

func recordOperationWithFields(ctx context.Context, store *memory.Store, kind,
	description string, fields []interaction.Field) *interaction.Operation {
	return &interaction.Operation{
		ID:          kind,
		Description: description,
		Fields:      fields,
		Execute: func(input map[string]json.RawMessage) (json.RawMessage, error) {
			session, err := sessionOf(store, input)
			if err != nil {
				return nil, classify(err)
			}
			observation := memory.Observation{
				Title:        textOf(input, fieldTitle),
				Body:         textOf(input, fieldBody),
				Rationale:    textOf(input, fieldRationale),
				Alternatives: textOf(input, fieldAlternatives),
				Consequences: textOf(input, fieldConsequences),
				Topic:        textOf(input, fieldTopic),
				Confidence:   textOf(input, fieldConfidence),
			}
			if kind == "decision" {
				observation.Body = textOf(input, fieldDecision)
			}
			result, err := store.RecordObservation(ctx, session, kind, observation)
			if err != nil {
				return nil, classify(err)
			}
			return outcome(writeOutcome{Result: result, Session: session})
		},
	}
}

func preferenceOperation(ctx context.Context, store *memory.Store) *interaction.Operation {
	return &interaction.Operation{
		ID:          "preference",
		Description: "Registra una preferencia operativa; una nueva clave activa sustituye a la anterior.",
		Fields: []interaction.Field{
			requiredText(fieldPrefKey, "la clave de la preferencia, en la forma familia/descripcion"),
			requiredText(fieldValue, "el valor de la preferencia"),
			{
				Name: fieldOrigin, Required: true, Type: textType,
				Allowed: []string{"human", "agent"},
				Description: "human SOLO si el usuario manifestó la preferencia " +
					"expresamente; agent si la inferiste",
			},
			optionalText(fieldRationale, "el motivo de la preferencia"),
			sessionField(),
		},
		Execute: func(input map[string]json.RawMessage) (json.RawMessage, error) {
			session, err := sessionOf(store, input)
			if err != nil {
				return nil, classify(err)
			}
			result, err := store.SetPreference(ctx, session,
				textOf(input, fieldPrefKey), textOf(input, fieldValue),
				textOf(input, fieldRationale), textOf(input, fieldOrigin))
			if err != nil {
				return nil, classify(err)
			}
			return outcome(writeOutcome{Result: result, Session: session})
		},
	}
}

func recallOperation(ctx context.Context, store *memory.Store) *interaction.Operation {
	return &interaction.Operation{
		ID:          "recall",
		Description: "Busca recuerdos activos por palabras clave del dominio, no por la pregunta completa.",
		Fields: []interaction.Field{
			requiredText(fieldQuery, "las palabras clave del dominio, no la pregunta completa"),
			{
				Name: fieldLimit, Type: "number",
				Description: "entero entre 1 y 100; por defecto 10",
				Validate: func(value any) (string, bool) {
					number, ok := value.(json.Number)
					if !ok {
						return "entero entre 1 y 100", false
					}
					limit, err := number.Int64()
					if err != nil || limit < 1 || limit > 100 {
						return "entero entre 1 y 100", false
					}
					return "", true
				},
			},
			sessionField(),
		},
		Execute: func(input map[string]json.RawMessage) (json.RawMessage, error) {
			session, err := sessionOf(store, input)
			if err != nil {
				return nil, classify(err)
			}
			limit := 10
			if raw, present := input[fieldLimit]; present && !isNull(raw) {
				var number json.Number
				_ = json.Unmarshal(raw, &number)
				parsed, err := number.Int64()
				if err != nil {
					return nil, interaction.InvalidError{Fields: map[string]string{
						fieldLimit: "entero entre 1 y 100"}}
				}
				limit = int(parsed)
			}
			result, err := store.Recall(ctx, session, textOf(input, fieldQuery), limit)
			if err != nil {
				return nil, classify(err)
			}
			return outcome(result)
		},
	}
}

func checkpointOperation(ctx context.Context, store *memory.Store) *interaction.Operation {
	return &interaction.Operation{
		ID:          "checkpoint",
		Description: "Guarda el checkpoint de reanudación y pasa el anterior a superseded en una transacción.",
		Fields: []interaction.Field{
			requiredText(fieldState, "el estado del trabajo"),
			requiredText(fieldNextSteps, "los siguientes pasos"),
			optionalText(fieldFiles, "los ficheros relevantes"),
			optionalText(fieldOpenQuestions, "las preguntas abiertas"),
			sessionField(),
		},
		Execute: func(input map[string]json.RawMessage) (json.RawMessage, error) {
			session, err := sessionOf(store, input)
			if err != nil {
				return nil, classify(err)
			}
			result, checkpoint, err := store.WriteCheckpoint(ctx, session,
				textOf(input, fieldState), textOf(input, fieldNextSteps),
				textOf(input, fieldFiles), textOf(input, fieldOpenQuestions))
			if err != nil {
				return nil, classify(err)
			}
			return outcome(writeOutcome{
				Result: result, Session: session, Checkpoint: checkpoint,
			})
		},
	}
}

func closeOperation(ctx context.Context, store *memory.Store) *interaction.Operation {
	return &interaction.Operation{
		ID:          "close",
		Description: "Cierra la sesión con su checkpoint final; el checkpoint y el cierre van en la misma transacción.",
		Fields: []interaction.Field{
			requiredText(fieldSummary, "el resumen del cierre"),
			optionalText(fieldState, "el estado al cerrar; por defecto, el summary"),
			optionalText(fieldNextSteps, "el siguiente paso; por defecto, ninguno"),
			optionalText(fieldFiles, "los ficheros relevantes"),
			optionalText(fieldOpenQuestions, "las preguntas abiertas"),
			sessionField(),
		},
		Execute: func(input map[string]json.RawMessage) (json.RawMessage, error) {
			session, err := sessionOf(store, input)
			if err != nil {
				return nil, classify(err)
			}
			endedAt, checkpoint, err := store.CloseSession(ctx, session,
				textOf(input, fieldSummary), textOf(input, fieldState),
				textOf(input, fieldNextSteps), textOf(input, fieldFiles),
				textOf(input, fieldOpenQuestions))
			if err != nil {
				return nil, classify(err)
			}
			return outcome(writeOutcome{
				Result: "closed", Session: session,
				Checkpoint: checkpoint, EndedAt: endedAt,
			})
		},
	}
}

// classify turns a core error into the AIF result it means: a state refusal is
// a denial, a missing session is repairable, and everything else keeps its code
// in a failed result.
func classify(err error) error {
	var contract *memory.Error
	if !errors.As(err, &contract) {
		return interaction.DomainError{Message: err.Error()}
	}
	switch contract.Code {
	case "E_CONSTRAINT", "E_DENIED":
		return interaction.DeniedError{Reason: contract.Message}
	default:
		// E_NOT_FOUND va en failed con su código: la corrección no es un valor
		// de session distinto, es abrir la sesión con `acm start`.
		return interaction.DomainError{Code: contract.Code, Message: contract.Message}
	}
}

func sessionOf(store *memory.Store, input map[string]json.RawMessage) (string, error) {
	return store.ResolveSession(textOf(input, fieldSession))
}

func requiredText(name, description string) interaction.Field {
	return interaction.Field{
		Name: name, Required: true, Type: textType, Description: description,
		Validate: func(value any) (string, bool) {
			text, _ := value.(string)
			if strings.TrimSpace(text) == "" {
				return "obligatorio", false
			}
			return "", true
		},
	}
}

func optionalText(name, description string) interaction.Field {
	return interaction.Field{Name: name, Type: textType, Description: description}
}

func confidenceField() interaction.Field {
	return interaction.Field{
		Name: fieldConfidence, Type: textType, Allowed: Confidences,
		Description: "la certeza del recuerdo; por defecto confirmed",
	}
}

func sessionField() interaction.Field {
	return interaction.Field{
		Name: fieldSession, Type: textType,
		Description: "el identificador de sesión; por defecto OPENCODE_SESSION_ID",
	}
}

func textOf(input map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(input[name], &text)
	return text
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func outcome(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, interaction.DomainError{
			Code: "E_INTERNAL", Message: "no se pudo preparar la respuesta",
		}
	}
	return raw, nil
}
