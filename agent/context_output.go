package agentv3

import (
	"encoding/json"
	"encoding/xml"
	"slices"
	"strconv"
)

const (
	contextToolOutputByteBudget = 12000
	contextCoverageUnknown      = "unknown"
)

type contextOutputMetadata struct {
	XMLName                 xml.Name               `xml:"metadata"`
	Count                   int                    `xml:"count,attr"`
	MinID                   int                    `xml:"min_message_id,attr,omitempty"`
	MaxID                   int                    `xml:"max_message_id,attr,omitempty"`
	Selection               string                 `xml:"selection,attr"`
	LimitFrom               string                 `xml:"limit_from,attr"`
	Limit                   int                    `xml:"limit,attr"`
	CandidateScope          string                 `xml:"candidate_scope,attr"`
	CandidateCount          int                    `xml:"candidate_count,attr"`
	CandidateMinID          int                    `xml:"candidate_min_message_id,attr,omitempty"`
	CandidateMaxID          int                    `xml:"candidate_max_message_id,attr,omitempty"`
	OmittedByLimit          string                 `xml:"omitted_by_limit,attr"`
	OmittedByBudget         int                    `xml:"omitted_by_budget,attr"`
	TruncatedBodies         int                    `xml:"truncated_bodies,attr"`
	OmittedBodyBytes        int                    `xml:"omitted_body_bytes,attr"`
	HistoryComplete         string                 `xml:"history_complete,attr"`
	Continuity              string                 `xml:"continuity,attr"`
	StreamSnapshotScanLimit int64                  `xml:"stream_snapshot_scan_limit,attr"`
	StreamSnapshotsScanned  string                 `xml:"stream_snapshots_scanned,attr"`
	RecentWindow            string                 `xml:"recent_window,attr"`
	OmittedMessage          *contextOutputOmission `xml:"omitted_message,omitempty"`
	Continuations           []contextContinuation  `xml:"continuation"`
}

type contextOutputOmission struct {
	ID     int    `xml:"id,attr"`
	Reason string `xml:"reason,attr"`
}

type contextContinuation struct {
	Direction string `xml:"direction,attr"`
	Basis     string `xml:"basis,attr"`
	Tool      string `xml:"tool,attr"`
	Arguments string `xml:",chardata"`
}

func formatContextToolOutput(result contextQueryResult, query contextQuery) string {
	selected := result.Messages
	if len(selected) > query.Limit {
		if query.LimitFrom == contextLimitEarliest {
			selected = selected[:query.Limit]
		} else {
			selected = selected[len(selected)-query.Limit:]
		}
	}
	var kept []*ContextMessage
	for i := range selected {
		index := i
		if query.LimitFrom != contextLimitEarliest {
			index = len(selected) - 1 - i
		}
		message := selected[index]
		trial := appendContextOutputMessage(kept, message, query.LimitFrom)
		if output := renderContextToolOutput(result, query, trial, len(selected), 0, nil); len(output) <= contextToolOutputByteBudget {
			kept = trial
			continue
		}
		if len(kept) != 0 {
			break
		}

		clipped := *message
		clipped.Text = ""
		output := renderContextToolOutput(result, query, []*ContextMessage{&clipped}, len(selected), len(message.Text), nil)
		if len(output) > contextToolOutputByteBudget {
			omission := &contextOutputOmission{ID: message.ID, Reason: "attributes_exceed_budget"}
			return renderContextToolOutput(result, query, nil, len(selected), 0, omission)
		}
		runes := []rune(message.Text)
		low, high := 0, len(runes)
		for low < high {
			mid := low + (high-low+1)/2
			clipped.Text = string(runes[:mid])
			output = renderContextToolOutput(result, query, []*ContextMessage{&clipped}, len(selected), len(message.Text)-len(clipped.Text), nil)
			if len(output) <= contextToolOutputByteBudget {
				low = mid
			} else {
				high = mid - 1
			}
		}
		clipped.Text = string(runes[:low])
		return renderContextToolOutput(result, query, []*ContextMessage{&clipped}, len(selected), len(message.Text)-len(clipped.Text), nil)
	}
	return renderContextToolOutput(result, query, kept, len(selected), 0, nil)
}

func appendContextOutputMessage(messages []*ContextMessage, message *ContextMessage, from string) []*ContextMessage {
	if from == contextLimitEarliest {
		return append(slices.Clone(messages), message)
	}
	return append([]*ContextMessage{message}, messages...)
}

func contextOutputIDRange(messages []*ContextMessage) (int, int) {
	if len(messages) == 0 {
		return 0, 0
	}
	minID, maxID := messages[0].ID, messages[0].ID
	for _, message := range messages[1:] {
		minID = min(minID, message.ID)
		maxID = max(maxID, message.ID)
	}
	return minID, maxID
}

func renderContextToolOutput(result contextQueryResult, query contextQuery, messages []*ContextMessage, selectedCount, omittedBodyBytes int, omission *contextOutputOmission) string {
	minID, maxID := contextOutputIDRange(messages)
	candidateMinID, candidateMaxID := contextOutputIDRange(result.Messages)
	omittedByLimit := strconv.Itoa(len(result.Messages) - selectedCount)
	if result.CandidateScope == "default_selection_only" {
		omittedByLimit = contextCoverageUnknown
	}
	metadata := contextOutputMetadata{
		Count: len(messages), MinID: minID, MaxID: maxID,
		Selection: result.Selection, LimitFrom: query.LimitFrom, Limit: query.Limit,
		CandidateScope: result.CandidateScope, CandidateCount: len(result.Messages),
		CandidateMinID: candidateMinID, CandidateMaxID: candidateMaxID,
		OmittedByLimit: omittedByLimit, OmittedByBudget: selectedCount - len(messages),
		OmittedBodyBytes: omittedBodyBytes, OmittedMessage: omission,
		HistoryComplete: contextCoverageUnknown, Continuity: "not_guaranteed",
		StreamSnapshotScanLimit: contextStreamMaxLength, StreamSnapshotsScanned: contextCoverageUnknown,
		RecentWindow: "none",
	}
	if omittedBodyBytes > 0 {
		metadata.TruncatedBodies = 1
	}
	if query.RecentSeconds != nil && *query.RecentSeconds > 0 {
		metadata.RecentWindow = "moving"
	}
	basis := "returned_ids"
	if omission != nil {
		minID, maxID = omission.ID, omission.ID
		basis = "omitted_message"
	}
	if minID != 0 {
		older, newer := contextLimitLatest, contextLimitEarliest
		for _, next := range []struct {
			direction string
			args      getContextArgs
		}{
			{"older", getContextArgs{Limit: query.Limit, RecentSeconds: query.RecentSeconds, AfterMessageID: query.AfterMessageID, BeforeMessageID: &minID, LimitFrom: &older}},
			{"newer", getContextArgs{Limit: query.Limit, RecentSeconds: query.RecentSeconds, AfterMessageID: &maxID, BeforeMessageID: query.BeforeMessageID, LimitFrom: &newer}},
		} {
			arguments, _ := json.Marshal(next.args)
			metadata.Continuations = append(metadata.Continuations, contextContinuation{Direction: next.direction, Basis: basis, Tool: contextToolName, Arguments: string(arguments)})
		}
	}
	encoded, _ := xml.Marshal(metadata)
	return formatContextMessagesWithMetadata(messages, "  "+string(encoded)+"\n")
}
