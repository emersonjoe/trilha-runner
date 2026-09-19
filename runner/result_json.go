package runner

import (
	"encoding/json"
	"strconv"
)

func (result Result) MarshalJSON() ([]byte, error) {
	type alias Result
	encoded, err := json.Marshal(alias(result))
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	items, _ := object["evidence"].([]any)
	for index, evidence := range result.Evidence {
		if evidence.Kind != "eval" || index >= len(items) {
			continue
		}
		record, _ := items[index].(map[string]any)
		record["metric"] = evidence.Meta["metric"]
		if value, err := strconv.ParseFloat(evidence.Meta["value"], 64); err == nil {
			record["value"] = value
		}
		if threshold, err := strconv.ParseFloat(evidence.Meta["threshold"], 64); err == nil {
			record["threshold"] = threshold
		}
		record["comparator"] = evidence.Meta["comparator"]
		if unit := evidence.Meta["unit"]; unit != "" {
			record["unit"] = unit
		}
		if id := evidence.Meta["dataset_id"]; id != "" {
			record["dataset"] = map[string]string{"id": id, "sha256": evidence.Meta["dataset_sha256"]}
		}
	}
	return json.Marshal(object)
}
