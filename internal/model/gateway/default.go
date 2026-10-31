// English: This gateway validates budgets and tool schemas before routing untrusted model responses.
// 中文：本网关在路由不可信模型输出前校验预算和工具 Schema。
// 中文：本网关在路由不可信模型输出前校验预算、工具白名单与参数 Schema。
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

var ErrNoProvider = errors.New("no model provider configured")

type Default struct{ Providers []Provider }

func (g Default) Complete(ctx context.Context, request Request) (Response, error) {
	if len(g.Providers) == 0 {
		return Response{}, ErrNoProvider
	}
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Model) != request.Model || strings.ContainsAny(request.Model, "\r\n\x00") {
		return Response{}, errors.New("model name is invalid")
	}
	if len(request.Messages) == 0 || len(request.Messages) > 1024 {
		return Response{}, errors.New("model request must contain 1 to 1024 messages")
	}
	if request.MaxTokens < 0 || request.MaxTokens > 128000 || request.Temperature < 0 || request.Temperature > 2 || math.IsNaN(request.Temperature) || math.IsInf(request.Temperature, 0) || request.Timeout < 0 || request.Timeout > 10*time.Minute {
		return Response{}, errors.New("invalid model generation parameters")
	}
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
			return Response{}, errors.New("unsupported model message role")
		}
		if len(message.Content) > 2<<20 {
			return Response{}, errors.New("model message exceeds 2 MiB")
		}
	}
	if len(request.Tools) > 128 {
		return Response{}, errors.New("model request contains too many tool definitions")
	}
	allowedTools := make(map[string]ToolDefinition, len(request.Tools))
	for _, tool := range request.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Name) != tool.Name || len(tool.Name) > 128 || strings.ContainsAny(tool.Name, "\r\n\x00") {
			return Response{}, errors.New("tool definition name is invalid")
		}
		if strings.TrimSpace(tool.Description) != tool.Description || len(tool.Description) > 4096 || strings.ContainsAny(tool.Description, "\x00") {
			return Response{}, errors.New("tool definition description is invalid")
		}
		if _, exists := allowedTools[tool.Name]; exists {
			return Response{}, errors.New("duplicate model tool definition")
		}
		if err := validateToolSchema(tool.Parameters); err != nil {
			return Response{}, err
		}
		allowedTools[tool.Name] = tool
	}

	var lastErr error
	for _, provider := range g.Providers {
		if provider == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		response, err := provider.Complete(ctx, request)
		if err != nil {
			lastErr = err
			continue
		}
		if err := validateResponse(&response, allowedTools); err != nil {
			return Response{}, err
		}
		if request.MaxTokens > 0 && response.Usage.OutputTokens > request.MaxTokens {
			return Response{}, errors.New("model provider exceeded requested output token budget")
		}
		if response.Provider == "" {
			response.Provider = provider.Name()
		}
		return response, nil
	}
	if lastErr == nil {
		return Response{}, ErrNoProvider
	}
	return Response{}, fmt.Errorf("all model providers failed: %w", lastErr)
}

func validateToolSchema(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("tool definition JSON Schema has invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil || schema == nil {
		return errors.New("tool definition parameters must be a JSON Schema object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("tool definition contains trailing schema data")
	}
	if schema["type"] != "object" {
		return errors.New("tool definition JSON Schema type must be object")
	}
	for key := range schema {
		if key != "type" && key != "required" && key != "properties" && key != "additionalProperties" {
			return errors.New("tool schema contains unsupported top-level attributes")
		}
	}
	properties := map[string]any{}
	if rawProperties, exists := schema["properties"]; exists {
		var ok bool
		properties, ok = rawProperties.(map[string]any)
		if !ok {
			return errors.New("tool schema properties must be an object")
		}
	}
	if len(properties) > 256 {
		return errors.New("tool schema properties must be an object with at most 256 fields")
	}
	if additional, exists := schema["additionalProperties"]; exists {
		allowed, ok := additional.(bool)
		if !ok || allowed {
			return errors.New("tool schemas must set additionalProperties to false")
		}
	} else if len(properties) > 0 {
		return errors.New("tool schemas with properties must set additionalProperties to false")
	}
	if required, exists := schema["required"]; exists {
		if err := validateRequired(required, properties); err != nil {
			return err
		}
	}
	for _, rawProperty := range properties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return errors.New("tool property schema must be an object")
		}
		propertyType, ok := property["type"].(string)
		if !ok {
			return errors.New("tool property schema type must be a string")
		}
		switch propertyType {
		case "string":
			if err := validateStringEnum(property["enum"], property); err != nil {
				return err
			}
		case "number", "integer", "boolean":
			if err := onlyKeys(property, "type"); err != nil {
				return err
			}
		case "object":
			if err := validateNestedObject(property); err != nil {
				return err
			}
		default:
			return errors.New("tool property schema has an unsupported type")
		}
	}
	return nil
}

func validateRequired(value any, properties map[string]any) error {
	if value == nil {
		return nil
	}
	required, ok := value.([]any)
	if !ok || len(required) > 256 {
		return errors.New("tool schema required must be an array with at most 256 fields")
	}
	seen := make(map[string]struct{}, len(required))
	for _, rawName := range required {
		name, ok := rawName.(string)
		if !ok || name == "" {
			return errors.New("tool schema required fields must be non-empty strings")
		}
		if _, exists := properties[name]; !exists {
			return errors.New("tool schema requires a field not declared in properties")
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("tool schema repeats a required field")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateStringEnum(value any, property map[string]any) error {
	if err := onlyKeys(property, "type", "enum"); err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	enum, ok := value.([]any)
	if !ok || len(enum) == 0 || len(enum) > 256 {
		return errors.New("tool string enum is invalid")
	}
	seen := make(map[string]struct{}, len(enum))
	for _, raw := range enum {
		item, ok := raw.(string)
		if !ok {
			return errors.New("tool enum values must be strings")
		}
		if _, duplicate := seen[item]; duplicate {
			return errors.New("tool enum values must be unique")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func validateArrayProperty(property map[string]any) error {
	if err := onlyKeys(property, "type", "items"); err != nil {
		return err
	}
	items, ok := property["items"].(map[string]any)
	if !ok || len(items) == 0 || len(items) > 2 {
		return errors.New("array property requires a primitive items schema")
	}
	itemType, ok := items["type"].(string)
	if !ok || (itemType != "string" && itemType != "number" && itemType != "integer" && itemType != "boolean") {
		return errors.New("array items type must be primitive")
	}
	if itemType == "string" {
		return validateStringEnum(items["enum"], items)
	}
	return onlyKeys(items, "type")
}

func validateNestedObject(schema map[string]any) error {
	if err := onlyKeys(schema, "type", "properties", "required", "additionalProperties"); err != nil {
		return err
	}
	additional, ok := schema["additionalProperties"].(bool)
	if !ok || additional {
		return errors.New("nested object must set additionalProperties to false")
	}
	properties := map[string]any{}
	if rawProperties, exists := schema["properties"]; exists {
		var ok bool
		properties, ok = rawProperties.(map[string]any)
		if !ok {
			return errors.New("nested object properties must be an object")
		}
	}
	if len(properties) > 256 {
		return errors.New("nested object properties must be an object with at most 256 fields")
	}
	if required, exists := schema["required"]; exists {
		if err := validateRequired(required, properties); err != nil {
			return err
		}
	}
	for _, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			return errors.New("nested property schema must be an object")
		}
		propertyType, ok := property["type"].(string)
		if !ok {
			return errors.New("nested object supports primitive property types only")
		}
		switch propertyType {
		case "string":
			if err := validateStringEnum(property["enum"], property); err != nil {
				return err
			}
		case "number", "integer", "boolean":
			if err := onlyKeys(property, "type"); err != nil {
				return err
			}
		default:
			return errors.New("nested compound property schemas are not supported")
		}
	}
	return nil
}

func onlyKeys(object map[string]any, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			return errors.New("schema contains unsupported attributes")
		}
	}
	return nil
}

func validateResponse(response *Response, tools map[string]ToolDefinition) error {
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.TotalTokens < 0 {
		return errors.New("model provider returned invalid token usage")
	}
	if response.Usage.InputTokens > int(^uint(0)>>1)-response.Usage.OutputTokens {
		return errors.New("model provider token usage overflows integer range")
	}
	if response.Usage.TotalTokens == 0 {
		response.Usage.TotalTokens = response.Usage.InputTokens + response.Usage.OutputTokens
	}
	if response.Usage.TotalTokens < response.Usage.InputTokens || response.Usage.TotalTokens < response.Usage.OutputTokens || response.Usage.TotalTokens < response.Usage.InputTokens+response.Usage.OutputTokens {
		return errors.New("model provider returned inconsistent token usage")
	}
	if len(response.Content) > 2<<20 {
		return errors.New("model provider response exceeds 2 MiB")
	}
	if len(response.ToolCalls) > 128 {
		return errors.New("model provider returned too many tool calls")
	}
	if len(response.ToolCalls) > 0 && len(tools) == 0 {
		return errors.New("model provider returned tool calls when no tools were supplied")
	}
	for _, call := range response.ToolCalls {
		if call.Name == "" || len(call.Name) > 128 || strings.ContainsAny(call.Name, "\r\n\x00") {
			return errors.New("model provider returned an invalid tool call name")
		}
		definition, ok := tools[call.Name]
		if !ok {
			return errors.New("model provider returned an unrequested tool call")
		}
		if err := validateToolArguments(call.Arguments, definition.Parameters); err != nil {
			return err
		}
	}
	return nil
}

func validateToolArguments(arguments, rawSchema json.RawMessage) error {
	if len(arguments) == 0 || len(arguments) > 1<<20 {
		return errors.New("model provider tool arguments have invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	var actual map[string]any
	if err := decoder.Decode(&actual); err != nil || actual == nil {
		return errors.New("model provider tool arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("model provider returned trailing tool argument data")
	}
	if len(actual) > 256 {
		return errors.New("model provider returned too many tool arguments")
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return errors.New("validated tool schema could not be decoded")
	}
	properties, _ := schema["properties"].(map[string]any)
	for key, value := range actual {
		property, ok := properties[key]
		if !ok {
			return errors.New("model tool arguments contain an unrecognized field")
		}
		if err := validateValue(value, property); err != nil {
			return err
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, rawName := range required {
			name, ok := rawName.(string)
			if !ok {
				return errors.New("validated tool schema required field is invalid")
			}
			if _, exists := actual[name]; !exists {
				return errors.New("model tool arguments omit a required field")
			}
		}
	}
	return nil
}

func validateValue(value any, rawSchema any) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return errors.New("tool property schema is invalid")
	}
	propertyType, _ := schema["type"].(string)
	valid := false
	switch propertyType {
	case "string":
		text, ok := value.(string)
		valid = ok
		if valid {
			if enum, ok := schema["enum"].([]any); ok {
				valid = false
				for _, option := range enum {
					if option == text {
						valid = true
						break
					}
				}
			}
		}
	case "boolean":
		_, valid = value.(bool)
	case "integer":
		number, ok := value.(float64)
		valid = ok && math.Trunc(number) == number
	case "number":
		_, valid = value.(float64)
	case "array":
		values, ok := value.([]any)
		valid = ok
		if valid {
			itemSchema := schema["items"]
			for _, item := range values {
				if validateValue(item, itemSchema) != nil {
					valid = false
					break
				}
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		valid = ok
		if valid {
			nested, _ := schema["properties"].(map[string]any)
			for key, item := range object {
				field, exists := nested[key]
				if !exists || validateValue(item, field) != nil {
					valid = false
					break
				}
			}
			if required, ok := schema["required"].([]any); ok {
				for _, raw := range required {
					name, _ := raw.(string)
					if _, exists := object[name]; !exists {
						valid = false
						break
					}
				}
			}
		}
	}
	if !valid {
		return errors.New("model tool argument does not match declared schema")
	}
	return nil
}

var _ Gateway = Default{}
