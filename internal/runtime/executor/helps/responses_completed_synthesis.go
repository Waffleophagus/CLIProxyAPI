package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesPartIDKey struct {
	itemID       string
	contentIndex int64
}

type responsesPartIdxKey struct {
	outputIndex  int64
	contentIndex int64
}

// ResponsesStreamOutputTracker tracks streamed Responses output items and synthesizes
// missing message streaming events before a terminal response.completed or
// response.incomplete event when upstream omitted item-level SSE frames.
type ResponsesStreamOutputTracker struct {
	addedIDs                 map[string]bool
	seenIDs                  map[string]bool
	addedIndices             map[int64]bool
	seenIndices              map[int64]bool
	addedFallbackIndices     map[int64]bool
	seenFallbackIndices      map[int64]bool
	addedPartIDs             map[responsesPartIDKey]bool
	addedPartIndices         map[responsesPartIdxKey]bool
	addedPartFallbackIndices map[responsesPartIdxKey]bool
	sawUnkeyedOutput         bool
	maxSeq                   int64
	hasSeq                   bool
}

func (t *ResponsesStreamOutputTracker) nextSeq() int64 {
	if !t.hasSeq {
		t.hasSeq = true
		t.maxSeq = 0
		return 0
	}
	t.maxSeq++
	return t.maxSeq
}

func (t *ResponsesStreamOutputTracker) newEventDoc(eventType string) []byte {
	doc := []byte(`{"type":""}`)
	doc, _ = sjson.SetBytes(doc, "type", eventType)
	doc, _ = sjson.SetBytes(doc, "sequence_number", t.nextSeq())
	return doc
}

func markTrackerKey(idMap *map[string]bool, idxMap *map[int64]bool, fallbackIdxMap *map[int64]bool, itemID string, hasIdx bool, idx int64) bool {
	if itemID != "" {
		if *idMap == nil {
			*idMap = make(map[string]bool)
		}
		(*idMap)[strings.Clone(itemID)] = true
		if hasIdx {
			if *fallbackIdxMap == nil {
				*fallbackIdxMap = make(map[int64]bool)
			}
			(*fallbackIdxMap)[idx] = true
		}
		return true
	}
	if hasIdx {
		if *idxMap == nil {
			*idxMap = make(map[int64]bool)
		}
		(*idxMap)[idx] = true
		return true
	}
	return false
}

func matchesTrackerKey(idMap map[string]bool, idxMap map[int64]bool, fallbackIdxMap map[int64]bool, itemID string, idx int64) bool {
	if itemID != "" {
		return idMap[itemID] || idxMap[idx]
	}
	return idxMap[idx] || fallbackIdxMap[idx]
}

// Observe records streamed events so terminal synthesis skips items already streamed.
func (t *ResponsesStreamOutputTracker) Observe(eventType string, eventData []byte) {
	if t == nil || len(eventData) == 0 {
		return
	}
	root := util.ParseGJSONBytesNoCopy(eventData)
	if seq := root.Get("sequence_number"); seq.Exists() && seq.Type == gjson.Number {
		if v := seq.Int(); !t.hasSeq || v > t.maxSeq {
			t.maxSeq = v
			t.hasSeq = true
		}
	}
	outIdx := root.Get("output_index")
	hasIdx := outIdx.Exists() && outIdx.Type == gjson.Number
	idx := outIdx.Int()

	switch eventType {
	case "response.output_item.added":
		if root.Get("item.type").String() != "message" {
			return
		}
		itemID := root.Get("item.id").String()
		markTrackerKey(&t.addedIDs, &t.addedIndices, &t.addedFallbackIndices, itemID, hasIdx, idx)
	case "response.content_part.added":
		if partType := root.Get("part.type").String(); partType != "output_text" && partType != "refusal" {
			return
		}
		cIdx := root.Get("content_index")
		if !cIdx.Exists() || cIdx.Type != gjson.Number {
			return
		}
		ci := cIdx.Int()
		itemID := root.Get("item_id").String()
		if itemID != "" {
			if t.addedPartIDs == nil {
				t.addedPartIDs = make(map[responsesPartIDKey]bool)
			}
			t.addedPartIDs[responsesPartIDKey{itemID: strings.Clone(itemID), contentIndex: ci}] = true
			if hasIdx {
				if t.addedPartFallbackIndices == nil {
					t.addedPartFallbackIndices = make(map[responsesPartIdxKey]bool)
				}
				t.addedPartFallbackIndices[responsesPartIdxKey{outputIndex: idx, contentIndex: ci}] = true
			}
		} else if hasIdx {
			if t.addedPartIndices == nil {
				t.addedPartIndices = make(map[responsesPartIdxKey]bool)
			}
			t.addedPartIndices[responsesPartIdxKey{outputIndex: idx, contentIndex: ci}] = true
		}
	case "response.output_item.done":
		if root.Get("item.type").String() != "message" {
			return
		}
		itemID := root.Get("item.id").String()
		if !markTrackerKey(&t.seenIDs, &t.seenIndices, &t.seenFallbackIndices, itemID, hasIdx, idx) {
			t.sawUnkeyedOutput = true
		}
	case "response.output_text.delta",
		"response.refusal.delta":
		if root.Get("delta").String() == "" {
			return
		}
		itemID := root.Get("item_id").String()
		if !markTrackerKey(&t.seenIDs, &t.seenIndices, &t.seenFallbackIndices, itemID, hasIdx, idx) {
			t.sawUnkeyedOutput = true
		}
	}
}

// SynthesizeMissingOutputEvents emits synthetic streaming events for unstreamed
// message items present in a terminal response.completed or response.incomplete event.
func (t *ResponsesStreamOutputTracker) SynthesizeMissingOutputEvents(eventData []byte) ([][]byte, []byte) {
	if t == nil || len(eventData) == 0 {
		return nil, eventData
	}
	root := util.ParseGJSONBytesNoCopy(eventData)
	output := root.Get("response.output")
	if !output.Exists() || !output.IsArray() {
		return nil, eventData
	}

	var synthesized [][]byte
	for i, item := range output.Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		idx := int64(i)
		itemID := item.Get("id").String()
		if t.sawUnkeyedOutput || matchesTrackerKey(t.seenIDs, t.seenIndices, t.seenFallbackIndices, itemID, idx) {
			continue
		}
		alreadyAdded := matchesTrackerKey(t.addedIDs, t.addedIndices, t.addedFallbackIndices, itemID, idx)
		if !alreadyAdded {
			addedItem := []byte(item.Raw)
			addedItem, _ = sjson.SetBytes(addedItem, "status", "in_progress")
			addedItem, _ = sjson.SetRawBytes(addedItem, "content", []byte("[]"))
			doc := t.newEventDoc("response.output_item.added")
			doc, _ = sjson.SetBytes(doc, "output_index", idx)
			doc, _ = sjson.SetRawBytes(doc, "item", addedItem)
			synthesized = append(synthesized, doc)
		}

		for ci, part := range item.Get("content").Array() {
			partType := part.Get("type").String()
			if partType != "output_text" && partType != "refusal" {
				continue
			}
			partIdxKey := responsesPartIdxKey{outputIndex: idx, contentIndex: int64(ci)}
			partAlreadyAdded := (itemID != "" && t.addedPartIDs[responsesPartIDKey{itemID: itemID, contentIndex: int64(ci)}]) ||
				t.addedPartIndices[partIdxKey] ||
				(itemID == "" && t.addedPartFallbackIndices[partIdxKey])
			if !partAlreadyAdded {
				partAdded := t.newEventDoc("response.content_part.added")
				if itemID != "" {
					partAdded, _ = sjson.SetBytes(partAdded, "item_id", itemID)
				}
				partAdded, _ = sjson.SetBytes(partAdded, "output_index", idx)
				partAdded, _ = sjson.SetBytes(partAdded, "content_index", ci)
				if partType == "refusal" {
					partAdded, _ = sjson.SetRawBytes(partAdded, "part", []byte(`{"type":"refusal","refusal":""}`))
				} else {
					partAdded, _ = sjson.SetRawBytes(partAdded, "part", []byte(`{"type":"output_text","text":"","annotations":[]}`))
				}
				synthesized = append(synthesized, partAdded)
			}

			var value string
			if partType == "refusal" {
				value = part.Get("refusal").String()
				if value == "" {
					value = part.Get("text").String()
				}
			} else {
				value = part.Get("text").String()
			}

			deltaType, doneType, doneField := "response.output_text.delta", "response.output_text.done", "text"
			if partType == "refusal" {
				deltaType, doneType, doneField = "response.refusal.delta", "response.refusal.done", "refusal"
			}
			if value != "" {
				deltaDoc := t.newEventDoc(deltaType)
				if itemID != "" {
					deltaDoc, _ = sjson.SetBytes(deltaDoc, "item_id", itemID)
				}
				deltaDoc, _ = sjson.SetBytes(deltaDoc, "output_index", idx)
				deltaDoc, _ = sjson.SetBytes(deltaDoc, "content_index", ci)
				deltaDoc, _ = sjson.SetBytes(deltaDoc, "delta", value)
				synthesized = append(synthesized, deltaDoc)
			}

			doneDoc := t.newEventDoc(doneType)
			if itemID != "" {
				doneDoc, _ = sjson.SetBytes(doneDoc, "item_id", itemID)
			}
			doneDoc, _ = sjson.SetBytes(doneDoc, "output_index", idx)
			doneDoc, _ = sjson.SetBytes(doneDoc, "content_index", ci)
			doneDoc, _ = sjson.SetBytes(doneDoc, doneField, value)
			synthesized = append(synthesized, doneDoc)

			partDone := t.newEventDoc("response.content_part.done")
			if itemID != "" {
				partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
			}
			partDone, _ = sjson.SetBytes(partDone, "output_index", idx)
			partDone, _ = sjson.SetBytes(partDone, "content_index", ci)
			partDone, _ = sjson.SetRawBytes(partDone, "part", []byte(part.Raw))
			synthesized = append(synthesized, partDone)
		}

		itemDone := t.newEventDoc("response.output_item.done")
		itemDone, _ = sjson.SetBytes(itemDone, "output_index", idx)
		itemDone, _ = sjson.SetRawBytes(itemDone, "item", []byte(item.Raw))
		synthesized = append(synthesized, itemDone)
	}

	if len(synthesized) > 0 {
		if seq := root.Get("sequence_number"); seq.Exists() && seq.Type == gjson.Number && seq.Int() <= t.maxSeq {
			eventData, _ = sjson.SetBytes(eventData, "sequence_number", t.nextSeq())
		}
	}
	return synthesized, eventData
}
