package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBranchCausesExposeViolatingValue(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"steps": {"type": "array", "items": {"$ref": "#/$defs/action"}}},
		"required": ["steps"], "additionalProperties": false,
		"$defs": {
			"action": {"anyOf": [
				{"type":"object","properties":{
					"action":{"enum":["filter"]},
					"criteria":{"type":"object","properties":{
						"field":{"enum":["text","website"]},
						"op":{"enum":["eq","contains"]},
						"value":{"type":"string"}
					},"required":["field","op","value"],"additionalProperties":false}
				},"required":["action","criteria"],"additionalProperties":false},
				{"type":"object","properties":{
					"action":{"enum":["click"]},
					"target":{"type":"object","properties":{
						"family":{"enum":["selector","text"]},"value":{"type":"string"},"name":{"type":"string"}
					},"required":["family","value","name"],"additionalProperties":false}
				},"required":["action","target"],"additionalProperties":false}
			]}
		}
	}`)
	arguments := `{"steps":[{"action":"filter","criteria":{"field":"items","op":"eq","value":"{{x}}"}}]}`
	err := validateStructuredOutputArguments(schema, arguments)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	message := err.Error()
	if !strings.Contains(message, "(action=filter)") {
		t.Fatalf("branch causes missing filter label: %s", message)
	}
	if !strings.Contains(message, "criteria.field: items not in [text website]") {
		t.Fatalf("branch cause does not expose the actual violating value and allowed enum: %s", message)
	}
}

func TestBranchCausesReportNothingWhenAnyBranchMatches(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"steps": {"type": "array", "items": {"$ref": "#/$defs/action"}}},
		"required": ["steps"], "additionalProperties": false,
		"$defs": {
			"action": {"anyOf": [
				{"type":"object","properties":{
					"action":{"enum":["extract"]},
					"name":{"type":"string"},
					"condition":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"],"additionalProperties":false}
				},"required":["action","name","condition"],"additionalProperties":false},
				{"type":"object","properties":{
					"action":{"enum":["extract"]},
					"name":{"type":"string"}
				},"required":["action","name"],"additionalProperties":false},
				{"type":"object","properties":{
					"action":{"enum":["loop"]},
					"type":{"enum":["fixedCount"]},
					"count":{"type":"integer"}
				},"required":["action","type","count"],"additionalProperties":false},
				{"type":"object","properties":{
					"action":{"enum":["loop"]},
					"type":{"enum":["enum-for-different-action"]},
					"items":{"type":"string"},
					"as":{"type":"string"}
				},"required":["action","type","items","as"],"additionalProperties":false},
				{"type":"object","properties":{
					"action":{"enum":["loop"]},
					"type":{"enum":["forEach"]},
					"items":{"type":"string"},
					"as":{"type":"string"}
				},"required":["action","type","items","as"],"additionalProperties":false}
			]}
		}
	}`)
	arguments := `{"steps":[{"action":"extract","name":"items"},{"action":"loop","type":"forEach","items":"{{x}}","as":"item"}]}`
	err := validateStructuredOutputArguments(schema, arguments)
	if err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	badArguments := `{"steps":[{"action":"loop","type":"forEach","items":"{{x}}"}]}`
	err = validateStructuredOutputArguments(schema, badArguments)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if !strings.Contains(err.Error(), "(action=loop): $.steps[0].as: required property missing") {
		t.Fatalf("forEach missing-property cause not named precisely: %s", err.Error())
	}
}
