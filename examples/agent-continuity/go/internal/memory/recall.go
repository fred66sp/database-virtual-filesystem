package memory

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// RecallRow is the public view of a recalled observation, in the same field
// order the Python adapter returned.
type RecallRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	TopicKey  string `json:"topic_key"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
}

// RecallResult is the answer of a search: the exact query, how many results,
// the results and, when the search had to relax, the flag.
type RecallResult struct {
	Query   string      `json:"query"`
	Count   int         `json:"count"`
	Results []RecallRow `json:"results"`
	Relaxed bool        `json:"relaxed,omitempty"`
	Message string      `json:"message,omitempty"`
}

// Function words dropped only when the exact recall finds nothing.
var stopwords = map[string]struct{}{}

func init() {
	// init is deterministic and only fills a static set.
	for _, word := range strings.Fields(`
de la el los las un una unos unas que y o en con por para del al es son se su
sus lo le les no mas pero si como este esta estos estas cuando donde quien cual
sobre entre hasta desde sin ya muy tambien hay ser fue han ha
the a an of to in on for and or is are be with how what when where why this
that these those it its as at by from not do does did can could should would
will we you i my our your`) {
		stopwords[word] = struct{}{}
	}
}

var wordPattern = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// foldText is the Python comparison key: casefold, NFD and no combining marks.
// shortcut: filters Mn runes instead of every canonical combining class > 0;
// the few zero-class marks only appear in exotic scripts.
func foldText(text string) string {
	decomposed := norm.NFD.String(cases.Fold().String(text))
	var builder strings.Builder
	builder.Grow(len(decomposed))
	for _, char := range decomposed {
		if unicode.Is(unicode.Mn, char) {
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// Recall finds active memories by keyword. It first requires every term, then
// drops function words, then tolerates one missing significant term when there
// are three or more, ranking by matches.
func (s *Store) Recall(ctx context.Context, session, query string, limit int) (RecallResult, error) {
	if _, _, err := s.loadSession(ctx, session); err != nil {
		return RecallResult{}, err
	}
	terms := wordPattern.FindAllString(foldText(query), -1)
	if len(terms) == 0 {
		return RecallResult{}, errorf(codeUsage,
			"la consulta no tiene palabras clave; usa términos del "+
				"dominio (p. ej. 'parser fechas')")
	}
	limit = max(1, min(limit, 100))

	// status=active goes in the filter, before the limit; 1000 is db-ai's
	// maximum --limit. shortcut: no pagination, more than 1000 active
	// observations are truncated.
	found, err := s.rows(ctx, "observations", []string{"status=active"}, "", 1000)
	if err != nil {
		return RecallResult{}, err
	}
	type candidate struct {
		observation map[string]string
		haystack    string
	}
	candidates := make([]candidate, 0, len(found))
	for _, row := range found {
		observation := row.Values()
		haystack := foldText(strings.Join([]string{
			observation["title"], observation["body"], observation["topic_key"],
		}, " "))
		candidates = append(candidates, candidate{observation, haystack})
	}

	hits := func(stage []string) []RecallRow {
		results := make([]RecallRow, 0, limit)
		for _, entry := range candidates {
			matches := true
			for _, term := range stage {
				if !strings.Contains(entry.haystack, term) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			results = append(results, recallView(entry.observation))
			if len(results) == limit {
				break
			}
		}
		return results
	}

	significant := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, isStopword := stopwords[term]; utf8.RuneCountInString(term) > 2 && !isStopword {
			significant = append(significant, term)
		}
	}
	stages := [][]string{terms}
	if len(significant) > 0 && !slices.Equal(significant, terms) {
		stages = append(stages, significant)
	}
	for index, stage := range stages {
		if results := hits(stage); len(results) > 0 {
			return RecallResult{
				Query: query, Count: len(results), Results: results,
				Relaxed: index > 0,
			}, nil
		}
	}

	// Relaxing by the most significant words: with three or more terms, one
	// may fail; the rank is the number of matches.
	if len(significant) >= 3 {
		threshold := max(2, len(significant)-1)
		type ranked struct {
			matches     int
			observation map[string]string
		}
		ranking := []ranked{}
		for _, entry := range candidates {
			matches := 0
			for _, term := range significant {
				if strings.Contains(entry.haystack, term) {
					matches++
				}
			}
			if matches >= threshold {
				ranking = append(ranking, ranked{matches, entry.observation})
			}
		}
		if len(ranking) > 0 {
			sort.SliceStable(ranking, func(i, j int) bool {
				return ranking[i].matches > ranking[j].matches
			})
			results := make([]RecallRow, 0, min(len(ranking), limit))
			for _, entry := range ranking[:min(len(ranking), limit)] {
				results = append(results, recallView(entry.observation))
			}
			return RecallResult{
				Query: query, Count: len(results), Results: results, Relaxed: true,
			}, nil
		}
	}
	return RecallResult{
		Query: query, Count: 0, Results: []RecallRow{},
		Message: "sin resultados; prueba otras palabras clave",
	}, nil
}

func recallView(observation map[string]string) RecallRow {
	return RecallRow{
		ID:        observation["id"],
		Kind:      observation["kind"],
		Title:     observation["title"],
		Body:      observation["body"],
		TopicKey:  observation["topic_key"],
		SessionID: observation["session_id"],
		CreatedAt: observation["created_at"],
	}
}
