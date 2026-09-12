package agentprotocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/message"
)

const maxUTF8BytesVocabularyURL = "https://dayorder.local/schemas/agent/vocab/max-utf8-bytes"

// NewSchemaCompiler returns the shared compiler configuration for canonical
// protocol DTOs and dynamic Tool input/output schemas.
func NewSchemaCompiler() (*jsonschema.Compiler, error) {
	vocabulary, err := maxUTF8BytesVocabulary()
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.AssertVocabs()
	compiler.RegisterVocabulary(vocabulary)
	return compiler, nil
}

type maxUTF8BytesSchema struct {
	limit int
}

func (schema *maxUTF8BytesSchema) Validate(context *jsonschema.ValidatorContext, value any) {
	text, ok := value.(string)
	if !ok {
		return
	}
	if len(text) > schema.limit {
		context.AddError(&maxUTF8BytesError{got: len(text), want: schema.limit})
	}
}

type maxUTF8BytesError struct {
	got  int
	want int
}

func (*maxUTF8BytesError) KeywordPath() []string { return []string{"maxUtf8Bytes"} }

func (failure *maxUTF8BytesError) LocalizedString(printer *message.Printer) string {
	return printer.Sprintf("maxUtf8Bytes: got %d, want at most %d", failure.got, failure.want)
}

func maxUTF8BytesVocabulary() (*jsonschema.Vocabulary, error) {
	metaSchema, err := jsonschema.UnmarshalJSON(strings.NewReader(`{
		"properties": {
			"maxUtf8Bytes": { "type": "integer", "minimum": 0 }
		}
	}`))
	if err != nil {
		return nil, fmt.Errorf("decode maxUtf8Bytes vocabulary: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(maxUTF8BytesVocabularyURL, metaSchema); err != nil {
		return nil, fmt.Errorf("add maxUtf8Bytes vocabulary: %w", err)
	}
	compiled, err := compiler.Compile(maxUTF8BytesVocabularyURL)
	if err != nil {
		return nil, fmt.Errorf("compile maxUtf8Bytes vocabulary: %w", err)
	}
	return &jsonschema.Vocabulary{
		URL:     maxUTF8BytesVocabularyURL,
		Schema:  compiled,
		Compile: compileMaxUTF8Bytes,
	}, nil
}

func compileMaxUTF8Bytes(_ *jsonschema.CompilerContext, object map[string]any) (jsonschema.SchemaExt, error) {
	value, exists := object["maxUtf8Bytes"]
	if !exists {
		return nil, nil
	}
	limit, err := schemaInteger(value)
	if err != nil {
		return nil, fmt.Errorf("maxUtf8Bytes: %w", err)
	}
	return &maxUTF8BytesSchema{limit: limit}, nil
}

func schemaInteger(value any) (int, error) {
	switch number := value.(type) {
	case int:
		return number, nil
	case int8:
		return int(number), nil
	case int16:
		return int(number), nil
	case int32:
		return int(number), nil
	case int64:
		return int(number), nil
	case uint:
		return int(number), nil
	case uint8:
		return int(number), nil
	case uint16:
		return int(number), nil
	case uint32:
		return int(number), nil
	case uint64:
		if uint64(int(number)) != number {
			return 0, fmt.Errorf("value exceeds int range")
		}
		return int(number), nil
	case float64:
		if number != float64(int(number)) {
			return 0, fmt.Errorf("value is not an integer")
		}
		return int(number), nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(number), 10, 0)
		if err != nil {
			return 0, err
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("value is not an integer")
	}
}
