package memory

import (
	"context"
	"strings"
)

// Block is how many complete memories each continuity section shows.
const Block = 5

// checkpointMarkdown renders one complete checkpoint: empty fields are left
// out and nothing is cut mid-sentence, as the Python adapter rendered it.
func checkpointMarkdown(title string, checkpoint map[string]string) string {
	lines := []string{"## " + title + "\n"}
	if state := strings.TrimSpace(checkpoint["state"]); state != "" {
		lines = append(lines, "Estado: "+state+"\n")
	}
	if steps := strings.TrimSpace(checkpoint["next_steps"]); steps != "" && steps != "ninguno" {
		lines = append(lines, "Siguientes pasos: "+steps+"\n")
	}
	if files := strings.TrimSpace(checkpoint["files"]); files != "" {
		lines = append(lines, "Ficheros: "+files+"\n")
	}
	if questions := strings.TrimSpace(checkpoint["open_questions"]); questions != "" {
		lines = append(lines, "Preguntas abiertas: "+questions+"\n")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// Continuity is the compact Markdown context of the project: the last closed
// session with its checkpoint, the current decisions, the recent knowledge and
// the active preferences. Every section shows complete entries.
func (s *Store) Continuity(ctx context.Context) (string, error) {
	sections := []string{}

	closed, err := s.rows(ctx, "sessions", []string{"status=closed"},
		"started_at:desc,id:desc", 1)
	if err != nil {
		return "", err
	}
	if len(closed) > 0 {
		values := closed[0].Values()
		summary := strings.TrimSpace(values["summary"])
		if summary == "" {
			summary = strings.TrimSpace(values["goal"])
		}
		final, err := s.rows(ctx, "checkpoints", []string{"session_id=" + values["id"]},
			"created_at:desc,id:desc", 1)
		if err != nil {
			return "", err
		}
		if len(final) > 0 {
			checkpoint := final[0].Values()
			lines := []string{"## Sesión anterior\n\n" + summary}
			if state := strings.TrimSpace(checkpoint["state"]); state != "" && state != summary {
				lines = append(lines, "Estado: "+state)
			}
			if steps := strings.TrimSpace(checkpoint["next_steps"]); steps != "" && steps != "ninguno" {
				lines = append(lines, "Siguientes pasos: "+steps)
			}
			if files := strings.TrimSpace(checkpoint["files"]); files != "" {
				lines = append(lines, "Ficheros: "+files)
			}
			if questions := strings.TrimSpace(checkpoint["open_questions"]); questions != "" {
				lines = append(lines, "Preguntas abiertas: "+questions)
			}
			sections = append(sections, strings.Join(lines, "\n\n"))
		}
	}

	block := func(title string, wheres ...string) error {
		found, err := s.rows(ctx, "observations", append([]string{"status=active"}, wheres...),
			"created_at:desc,id:desc", Block+1)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return nil
		}
		lines := make([]string, 0, Block+1)
		for _, row := range found[:min(len(found), Block)] {
			observation := row.Values()
			entry := "- **(" + observation["kind"] + ") " + observation["title"] + "**"
			if topic := strings.TrimSpace(observation["topic_key"]); topic != "" {
				entry += " (" + topic + ")"
			}
			body := strings.TrimSpace(observation["body"])
			if body != "" && body != observation["title"] {
				entry += ": " + body
			}
			lines = append(lines, entry)
		}
		if len(found) > Block {
			lines = append(lines, "- _hay más_")
		}
		sections = append(sections, "## "+title+"\n\n"+strings.Join(lines, "\n"))
		return nil
	}
	if err := block("Decisiones vigentes", "kind=decision"); err != nil {
		return "", err
	}
	if err := block("Conocimiento reciente", "kind!=decision"); err != nil {
		return "", err
	}

	preferences, err := s.rows(ctx, "preferences", []string{"status=active"},
		"created_at:desc,id:desc", Block+1)
	if err != nil {
		return "", err
	}
	if len(preferences) > 0 {
		lines := make([]string, 0, Block+1)
		for _, row := range preferences[:min(len(preferences), Block)] {
			preference := row.Values()
			item := "- **" + strings.TrimSpace(preference["pref_key"]) + "**"
			if strings.TrimSpace(preference["source"]) == "human" {
				item += " (del usuario)"
			}
			if value := strings.TrimSpace(preference["value"]); value != "" {
				item += ": " + value
			}
			lines = append(lines, item)
		}
		if len(preferences) > Block {
			lines = append(lines, "- _hay más_")
		}
		sections = append(sections, "## Preferencias activas\n\n"+strings.Join(lines, "\n"))
	}

	return strings.Join(sections, "\n\n"), nil
}
