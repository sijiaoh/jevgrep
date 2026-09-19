package jev

import (
	"fmt"
	"strconv"
)

// The request puts the lines in the state and asks one noul question per line.
// Sending the state once is the point: the lines are the expensive half of the
// payload, and a question only has to name the line it is about.
type request struct {
	Model     string              `json:"model"`
	State     map[string]string   `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type response struct {
	Answers map[string]answer `json:"answers"`
}

type answer struct {
	// Pointer so that a missing probability is told apart from 0, which is a
	// perfectly good answer meaning "certainly not".
	Noul *float64 `json:"noul"`
}

const noulType = "noul"

func newRequest(model, meaning string, lines []string) request {
	req := request{
		Model:     model,
		State:     make(map[string]string, len(lines)),
		Questions: make(map[string]question, len(lines)),
	}
	for i, line := range lines {
		id := lineID(i + 1)
		req.State[id] = line
		req.Questions[id] = question{Type: noulType, Instructions: instructions(id, meaning)}
	}
	return req
}

// lineID labels a line within one request. The label is positional only: it
// must carry no information about the line, or the model would score the label
// instead of the text.
func lineID(n int) string {
	return "L" + strconv.Itoa(n)
}

func instructions(id, meaning string) string {
	return fmt.Sprintf("Does line %s mean: %s", id, meaning)
}

// scores unpacks the answers back into line order. Anything missing or out of
// range is an error rather than a default: a silently wrong probability is a
// silently wrong grep result.
func (r response) scores(lines int) ([]float64, error) {
	scores := make([]float64, lines)
	for i := range scores {
		id := lineID(i + 1)
		a, ok := r.Answers[id]
		if !ok || a.Noul == nil {
			return nil, fmt.Errorf("jev: response has no answer for %s", id)
		}
		if *a.Noul < 0 || *a.Noul > 1 {
			return nil, fmt.Errorf("jev: answer for %s is not a probability: %v", id, *a.Noul)
		}
		scores[i] = *a.Noul
	}
	return scores, nil
}
