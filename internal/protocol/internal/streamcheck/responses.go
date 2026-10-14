package streamcheck

import "io"

// Responses checks an OpenAI Responses stream against OpenAI's "Responses streaming events" reference (read
// 2026-10-14): response.created first; sequence_number on every event, rising; output items added with output_index
// counting up from 0, their content parts and summary parts likewise per item, each part done before its item and
// each item before the response completes; every event that names an output item, part or item_id refers to one
// that is open, and a delta of a typed part matches its type. The stream ends with response.completed,
// response.incomplete, response.failed or error.
//
// sequence_number must rise, not rise by one: OpenAI's own streaming example leaves events out, and a relay may drop
// events it does not forward.
func Responses(src io.Reader) error { return check(src, &responses{seq: -1}) }

type responses struct {
	started, ended bool
	seq            int64
	items          []item
}

type item struct {
	id, kind         string
	done             bool
	parts, summaries []part
}

type part struct {
	kind string
	done bool
}

type responsesEvent struct {
	Type         string `json:"type"`
	Seq          *int64 `json:"sequence_number"`
	OutputIndex  *int64 `json:"output_index"`
	ContentIndex *int64 `json:"content_index"`
	SummaryIndex *int64 `json:"summary_index"`
	ItemID       string `json:"item_id"`
	Item         struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"item"`
	Part struct {
		Type string `json:"type"`
	} `json:"part"`
	Response struct {
		Output []struct{} `json:"output"`
	} `json:"response"`
}

// partKinds and itemKinds give the part or item type an event may refer to.
var (
	partKinds = map[string]string{
		"response.output_text.delta":    "output_text",
		"response.output_text.done":     "output_text",
		"response.refusal.delta":        "refusal",
		"response.refusal.done":         "refusal",
		"response.reasoning_text.delta": "reasoning_text",
		"response.reasoning_text.done":  "reasoning_text",
	}
	itemKinds = map[string]string{
		"response.function_call_arguments.delta": "function_call",
		"response.function_call_arguments.done":  "function_call",
		"response.reasoning_summary_text.delta":  "reasoning",
		"response.reasoning_summary_text.done":   "reasoning",
	}
)

var responsesEvents = map[string]func(*responses, *responsesEvent) error{
	"response.created":                      (*responses).created,
	"response.output_item.added":            (*responses).itemAdded,
	"response.output_item.done":             (*responses).itemDone,
	"response.content_part.added":           func(r *responses, e *responsesEvent) error { return r.partAdded(e, contentParts) },
	"response.content_part.done":            func(r *responses, e *responsesEvent) error { return r.partDone(e, contentParts) },
	"response.reasoning_summary_part.added": func(r *responses, e *responsesEvent) error { return r.partAdded(e, summaryParts) },
	"response.reasoning_summary_part.done":  func(r *responses, e *responsesEvent) error { return r.partDone(e, summaryParts) },
	"response.completed":                    (*responses).completed,
	"response.incomplete":                   (*responses).completed,
	"response.failed":                       (*responses).failed,
	"error":                                 (*responses).failed,
}

func (r *responses) event(data []byte) error {
	if r.ended {
		return invalid("an event after the stream ended")
	}
	var e responsesEvent
	if err := decode(data, &e); err != nil {
		return err
	}
	if e.Seq == nil || *e.Seq <= r.seq {
		return invalid("%s: sequence_number missing or not above %d", e.Type, r.seq)
	}
	r.seq = *e.Seq
	if !r.started && e.Type != "response.created" && e.Type != "error" {
		return invalid("%q before response.created", e.Type)
	}
	if handle, ok := responsesEvents[e.Type]; ok {
		return handle(r, &e)
	}
	return r.refersToOpen(&e)
}

func (r *responses) created(*responsesEvent) error {
	if r.started {
		return invalid("a second response.created")
	}
	r.started = true
	return nil
}

func (r *responses) itemAdded(e *responsesEvent) error {
	if !next(e.OutputIndex, len(r.items)) || e.Item.ID == "" || e.Item.Type == "" {
		return invalid("output_item.added without output_index %d, an item id or a type", len(r.items))
	}
	r.items = append(r.items, item{id: e.Item.ID, kind: e.Item.Type})
	return nil
}

func (r *responses) itemDone(e *responsesEvent) error {
	it, err := r.openItem(e)
	if err != nil {
		return err
	}
	for _, parts := range [][]part{it.parts, it.summaries} {
		for _, p := range parts {
			if !p.done {
				return invalid("output_item.done while a part of item %d is open", *e.OutputIndex)
			}
		}
	}
	it.done = true
	return nil
}

// partList picks the content parts or the summary parts of an item, with the index that addresses them.
type partList func(*item, *responsesEvent) (*[]part, *int64)

func contentParts(it *item, e *responsesEvent) (*[]part, *int64) { return &it.parts, e.ContentIndex }

func summaryParts(it *item, e *responsesEvent) (*[]part, *int64) {
	return &it.summaries, e.SummaryIndex
}

func (r *responses) partAdded(e *responsesEvent, list partList) error {
	it, err := r.openItem(e)
	if err != nil {
		return err
	}
	parts, i := list(it, e)
	if !next(i, len(*parts)) {
		return invalid("%s without the next part index %d", e.Type, len(*parts))
	}
	*parts = append(*parts, part{kind: e.Part.Type})
	return nil
}

func (r *responses) partDone(e *responsesEvent, list partList) error {
	p, err := r.openPart(e, list)
	if err != nil {
		return err
	}
	p.done = true
	return nil
}

func (r *responses) completed(e *responsesEvent) error {
	for i, it := range r.items {
		if !it.done {
			return invalid("%s while item %d is open", e.Type, i)
		}
	}
	if len(e.Response.Output) != len(r.items) {
		return invalid("%s lists %d output items, the stream added %d", e.Type, len(e.Response.Output), len(r.items))
	}
	r.ended = true
	return nil
}

func (r *responses) failed(*responsesEvent) error {
	r.ended = true
	return nil
}

// refersToOpen checks an event the table does not handle, a delta or done event or one of a type not listed: the
// item, item_id and parts it names must be open, and a typed event must name what its type belongs to.
func (r *responses) refersToOpen(e *responsesEvent) error {
	partKind, typedPart := partKinds[e.Type]
	itemKind, typedItem := itemKinds[e.Type]
	if e.OutputIndex == nil && !typedPart && !typedItem {
		return nil
	}
	it, err := r.openItem(e)
	switch {
	case err != nil:
		return err
	case typedItem && itemKind != it.kind:
		return invalid("%s for a %s item", e.Type, it.kind)
	case typedPart && e.ContentIndex == nil:
		return invalid("%s without content_index", e.Type)
	}
	return r.partsOpen(e, it, partKind)
}

// partsOpen checks that the parts e names in it are open and, when kind is set, of that kind.
func (r *responses) partsOpen(e *responsesEvent, it *item, kind string) error {
	for _, list := range []partList{contentParts, summaryParts} {
		if _, i := list(it, e); i == nil {
			continue
		}
		p, err := r.openPart(e, list)
		if err != nil {
			return err
		}
		if kind != "" && p.kind != kind {
			return invalid("%s for a %s part", e.Type, p.kind)
		}
	}
	return nil
}

func (r *responses) openItem(e *responsesEvent) (*item, error) {
	i, ok := at(e.OutputIndex, len(r.items))
	if !ok || r.items[i].done || e.ItemID != "" && e.ItemID != r.items[i].id {
		return nil, invalid("%s for an output item that is not open", e.Type)
	}
	return &r.items[i], nil
}

func (r *responses) openPart(e *responsesEvent, list partList) (*part, error) {
	it, err := r.openItem(e)
	if err != nil {
		return nil, err
	}
	parts, i := list(it, e)
	j, ok := at(i, len(*parts))
	if !ok || (*parts)[j].done {
		return nil, invalid("%s for a part that is not open", e.Type)
	}
	return &(*parts)[j], nil
}

func (r *responses) end() error {
	if !r.ended {
		return invalid("the stream ends before the response completes or fails")
	}
	return nil
}
