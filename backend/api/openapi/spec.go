// Package openapi embeds the OpenAPI 3.1 contract for the client-facing routes
// used by the native TV clients and provides helpers that validate HTTP
// requests and responses against it. Contract tests in package handlers use
// the Validator; no production code path depends on this package.
package openapi

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

//go:embed openapi.yaml
var specYAML []byte

// SpecYAML returns the raw embedded spec.
func SpecYAML() []byte {
	return bytes.Clone(specYAML)
}

// Load parses and validates the embedded spec.
func Load(ctx context.Context) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(specYAML)
	if err != nil {
		return nil, fmt.Errorf("load openapi spec: %w", err)
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate openapi spec: %w", err)
	}
	return doc, nil
}

// Validator checks requests and responses against the spec.
type Validator struct {
	Doc    *openapi3.T
	router routers.Router

	mu      sync.Mutex
	covered map[string]bool
}

// NewValidator loads the spec and builds a path router for it.
func NewValidator(ctx context.Context) (*Validator, error) {
	doc, err := Load(ctx)
	if err != nil {
		return nil, err
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build openapi router: %w", err)
	}
	return &Validator{Doc: doc, router: router, covered: make(map[string]bool)}, nil
}

// OperationIDs lists every operationId in the spec, sorted.
func (v *Validator) OperationIDs() []string {
	var ids []string
	for _, item := range v.Doc.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, op.OperationID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Covered reports which operations have had a response validated.
func (v *Validator) Covered() map[string]bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string]bool, len(v.covered))
	for id := range v.covered {
		out[id] = true
	}
	return out
}

func (v *Validator) options() *openapi3filter.Options {
	return &openapi3filter.Options{
		AuthenticationFunc:    openapi3filter.NoopAuthenticationFunc,
		IncludeResponseStatus: true,
		MultiError:            true,
	}
}

func (v *Validator) input(req *http.Request, body []byte) (*openapi3filter.RequestValidationInput, error) {
	// The spec's server URL is relative, so match on path only.
	matchReq := req.Clone(req.Context())
	matchReq.Host = ""
	matchReq.URL.Host = ""
	matchReq.URL.Scheme = ""
	route, pathParams, err := v.router.FindRoute(matchReq)
	if err != nil {
		return nil, fmt.Errorf("no spec route for %s %s: %w", req.Method, req.URL.Path, err)
	}
	validationReq := req.Clone(req.Context())
	validationReq.Body = io.NopCloser(bytes.NewReader(body))
	return &openapi3filter.RequestValidationInput{
		Request:    validationReq,
		PathParams: pathParams,
		Route:      route,
		Options:    v.options(),
	}, nil
}

// ValidateRequest checks a request (and its body) against the spec.
func (v *Validator) ValidateRequest(req *http.Request, body []byte) error {
	in, err := v.input(req, body)
	if err != nil {
		return err
	}
	return openapi3filter.ValidateRequest(req.Context(), in)
}

// ValidateResponse checks a response against the spec: status code, content
// type and body schema. JSON bodies are additionally checked for fields the
// spec does not document, so new model fields fail the contract tests until
// the spec is updated.
func (v *Validator) ValidateResponse(req *http.Request, status int, header http.Header, body []byte) error {
	in, err := v.input(req, nil)
	if err != nil {
		return err
	}
	respIn := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 status,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                v.options(),
	}
	if err := openapi3filter.ValidateResponse(req.Context(), respIn); err != nil {
		return err
	}
	v.mu.Lock()
	v.covered[in.Route.Operation.OperationID] = true
	v.mu.Unlock()
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	schema := responseSchema(in.Route.Operation, status)
	if schema == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	var undocumented []string
	findUndocumented(schema, value, "$", &undocumented)
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		return fmt.Errorf("response has fields not documented in the spec: %s", strings.Join(undocumented, ", "))
	}
	return nil
}

func responseSchema(op *openapi3.Operation, status int) *openapi3.SchemaRef {
	if op == nil || op.Responses == nil {
		return nil
	}
	resp := op.Responses.Status(status)
	if resp == nil {
		resp = op.Responses.Default()
	}
	if resp == nil || resp.Value == nil {
		return nil
	}
	media := resp.Value.Content.Get("application/json")
	if media == nil {
		return nil
	}
	return media.Schema
}

// branches flattens allOf/anyOf/oneOf into the list of concrete schemas a
// value may be described by.
func branches(ref *openapi3.SchemaRef) []*openapi3.Schema {
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	out := []*openapi3.Schema{s}
	for _, group := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
		for _, member := range group {
			out = append(out, branches(member)...)
		}
	}
	return out
}

// findUndocumented walks value alongside the schema and records object keys
// that no applicable schema declares. Objects whose schemas declare no
// properties, or allow additionalProperties, are treated as open maps.
func findUndocumented(ref *openapi3.SchemaRef, value any, path string, out *[]string) {
	candidates := branches(ref)
	if len(candidates) == 0 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		declared := false
		var mapSchema *openapi3.SchemaRef
		openMap := false
		for _, s := range candidates {
			if len(s.Properties) > 0 {
				declared = true
			}
			if s.AdditionalProperties.Schema != nil {
				mapSchema = s.AdditionalProperties.Schema
			}
			if s.AdditionalProperties.Has != nil && *s.AdditionalProperties.Has {
				openMap = true
			}
		}
		for key, child := range typed {
			childPath := path + "." + key
			var propSchema *openapi3.SchemaRef
			for _, s := range candidates {
				if p, ok := s.Properties[key]; ok {
					propSchema = p
					break
				}
			}
			switch {
			case propSchema != nil:
				findUndocumented(propSchema, child, childPath, out)
			case mapSchema != nil:
				findUndocumented(mapSchema, child, childPath, out)
			case declared && !openMap:
				*out = append(*out, childPath)
			}
		}
	case []any:
		var items *openapi3.SchemaRef
		for _, s := range candidates {
			if s.Items != nil {
				items = s.Items
				break
			}
		}
		if items == nil {
			return
		}
		for i, child := range typed {
			findUndocumented(items, child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}
