package observability

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const spanEnvelopeLimit = 32 * 1024
const snapshotSDKVersion = "1.47.0"
const maxValueDepth = 64

var errEnvelope = errors.New("telemetry envelope rejected")

// This is an aggregate data budget with conservative field/container charges,
// not a process RSS limit. SDK getters may materialize an incoming array first.
type envelopeBudget struct{ left int }

func (b *envelopeBudget) take(n int) error {
	if n < 0 || n > b.left {
		return errEnvelope
	}
	b.left -= n
	return nil
}

func (b *envelopeBudget) items(n, size int) error {
	if n < 0 || n > b.left/size {
		return errEnvelope
	}
	return b.take(n * size)
}

func (b *envelopeBudget) text(s string) (string, error) {
	if len(s) > b.left || !utf8.ValidString(s) {
		return "", errEnvelope
	}
	if err := b.take(len(s) + 64); err != nil {
		return "", err
	}
	return strings.Clone(s), nil
}

func (b *envelopeBudget) attributes(input []attribute.KeyValue) ([]attribute.KeyValue, error) {
	if err := b.items(len(input), 64); err != nil {
		return nil, err
	}
	out := make([]attribute.KeyValue, len(input))
	for i, item := range input {
		key, err := b.text(string(item.Key))
		if err != nil {
			return nil, err
		}
		value, err := b.value(item.Value)
		if err != nil {
			return nil, err
		}
		out[i] = attribute.KeyValue{Key: attribute.Key(key), Value: value}
	}
	return out, nil
}

// An explicit, budget-bounded work stack also handles the SDK's map/slice
// values without recursion or an embedded reference to the original value.
func (b *envelopeBudget) value(input attribute.Value) (attribute.Value, error) {
	var out attribute.Value
	type job struct {
		input  attribute.Value
		out    *attribute.Value
		depth  int
		values []attribute.Value
		pairs  []attribute.KeyValue
		finish bool
	}
	jobs := []job{{input: input, out: &out}}
	for len(jobs) != 0 {
		j := jobs[len(jobs)-1]
		jobs = jobs[:len(jobs)-1]
		if j.finish {
			if j.input.Type() == attribute.MAP {
				*j.out = attribute.MapValue(j.pairs...)
			} else {
				*j.out = attribute.SliceValue(j.values...)
			}
			continue
		}
		if j.depth > maxValueDepth {
			return out, errEnvelope
		}
		if err := b.take(32); err != nil {
			return out, err
		}
		switch j.input.Type() {
		case attribute.EMPTY:
			*j.out = attribute.Value{}
		case attribute.BOOL:
			*j.out = attribute.BoolValue(j.input.AsBool())
		case attribute.INT64:
			*j.out = attribute.Int64Value(j.input.AsInt64())
		case attribute.FLOAT64:
			*j.out = attribute.Float64Value(j.input.AsFloat64())
		case attribute.STRING:
			v, err := b.text(j.input.AsString())
			if err != nil {
				return out, err
			}
			*j.out = attribute.StringValue(v)
		case attribute.BOOLSLICE:
			v := j.input.AsBoolSlice()
			if err := b.items(len(v), 16); err != nil {
				return out, err
			}
			*j.out = attribute.BoolSliceValue(append([]bool(nil), v...))
		case attribute.INT64SLICE:
			v := j.input.AsInt64Slice()
			if err := b.items(len(v), 16); err != nil {
				return out, err
			}
			*j.out = attribute.Int64SliceValue(append([]int64(nil), v...))
		case attribute.FLOAT64SLICE:
			v := j.input.AsFloat64Slice()
			if err := b.items(len(v), 16); err != nil {
				return out, err
			}
			*j.out = attribute.Float64SliceValue(append([]float64(nil), v...))
		case attribute.STRINGSLICE:
			v := j.input.AsStringSlice()
			if err := b.items(len(v), 32); err != nil {
				return out, err
			}
			copied := make([]string, len(v))
			for i := range v {
				var err error
				copied[i], err = b.text(v[i])
				if err != nil {
					return out, err
				}
			}
			*j.out = attribute.StringSliceValue(copied)
		case attribute.BYTESLICE:
			v := j.input.AsByteSlice()
			if err := b.items(len(v), 1); err != nil {
				return out, err
			}
			*j.out = attribute.ByteSliceValue(append([]byte(nil), v...))
		case attribute.SLICE:
			v := j.input.AsSlice()
			if err := b.items(len(v), 64); err != nil {
				return out, err
			}
			copied := make([]attribute.Value, len(v))
			j.finish, j.values = true, copied
			jobs = append(jobs, j)
			for i := len(v) - 1; i >= 0; i-- {
				jobs = append(jobs, job{input: v[i], out: &copied[i], depth: j.depth + 1})
			}
		case attribute.MAP:
			v := j.input.AsMap()
			if err := b.items(len(v), 64); err != nil {
				return out, err
			}
			copied := make([]attribute.KeyValue, len(v))
			for i := range v {
				key, err := b.text(string(v[i].Key))
				if err != nil {
					return out, err
				}
				copied[i].Key = attribute.Key(key)
			}
			j.finish, j.pairs = true, copied
			jobs = append(jobs, j)
			for i := len(v) - 1; i >= 0; i-- {
				jobs = append(jobs, job{input: v[i].Value, out: &copied[i].Value, depth: j.depth + 1})
			}
		default:
			return out, errEnvelope
		}
	}
	return out, nil
}

func (b *envelopeBudget) spanContext(input trace.SpanContext) (trace.SpanContext, error) {
	if err := b.take(64); err != nil {
		return trace.SpanContext{}, err
	}
	var state trace.TraceState
	var failure error
	// Insert prepends; collect in order, then rebuild backwards.
	var entries [][2]string
	input.TraceState().Walk(func(key, value string) bool {
		if len(entries) == 32 || len(key) > 256 || len(value) > 256 || strings.ContainsAny(key+value, "\r\n") {
			failure = errEnvelope
			return false
		}
		k, err := b.text(key)
		if err != nil {
			failure = err
			return false
		}
		v, err := b.text(value)
		if err != nil {
			failure = err
			return false
		}
		entries = append(entries, [2]string{k, v})
		return true
	})
	if failure != nil {
		return trace.SpanContext{}, failure
	}
	for i := len(entries) - 1; i >= 0; i-- {
		var err error
		state, err = state.Insert(entries[i][0], entries[i][1])
		if err != nil {
			return trace.SpanContext{}, errEnvelope
		}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: input.TraceID(), SpanID: input.SpanID(), TraceFlags: input.TraceFlags(), TraceState: state, Remote: input.IsRemote()}), nil
}

func (b *envelopeBudget) resource(input *resource.Resource) (*resource.Resource, error) {
	if input == nil {
		return resource.Empty(), nil
	}
	if err := b.take(128); err != nil {
		return nil, err
	}
	if input.Len() > b.left/64 {
		return nil, errEnvelope
	}
	schema, err := b.text(input.SchemaURL())
	if err != nil {
		return nil, err
	}
	attrs, err := b.attributes(input.Attributes())
	if err != nil {
		return nil, err
	}
	return resource.NewWithAttributes(schema, attrs...), nil
}

func (b *envelopeBudget) scope(input instrumentation.Scope) (instrumentation.Scope, error) {
	var out instrumentation.Scope
	if err := b.take(128); err != nil {
		return out, err
	}
	var err error
	if out.Name, err = b.text(input.Name); err != nil {
		return out, err
	}
	if out.Version, err = b.text(input.Version); err != nil {
		return out, err
	}
	if out.SchemaURL, err = b.text(input.SchemaURL); err != nil {
		return out, err
	}
	if input.Attributes.Len() > b.left/64 {
		return out, errEnvelope
	}
	attrs, err := b.attributes(input.Attributes.ToSlice())
	if err != nil {
		return out, err
	}
	out.Attributes = attribute.NewSet(attrs...)
	return out, nil
}

func validateSnapshotSDK(version string) error {
	if version != snapshotSDKVersion {
		return errors.New("unsupported telemetry snapshot SDK contract")
	}
	return nil
}

func detachedSpan(input sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, int, error) {
	b := envelopeBudget{left: spanEnvelopeLimit}
	if err := b.take(512); err != nil {
		return nil, 0, err
	}
	var err error
	var value tracetest.SpanStub
	if value.Name, err = b.text(input.Name()); err != nil {
		return nil, 0, err
	}
	if value.SpanContext, err = b.spanContext(input.SpanContext()); err != nil {
		return nil, 0, err
	}
	if value.Parent, err = b.spanContext(input.Parent()); err != nil {
		return nil, 0, err
	}
	value.SpanKind, value.StartTime, value.EndTime = input.SpanKind(), input.StartTime().UTC(), input.EndTime().UTC()
	if value.Attributes, err = b.attributes(input.Attributes()); err != nil {
		return nil, 0, err
	}
	if value.Resource, err = b.resource(input.Resource()); err != nil {
		return nil, 0, err
	}
	if value.InstrumentationScope, err = b.scope(input.InstrumentationScope()); err != nil {
		return nil, 0, err
	}
	value.Status = input.Status()
	if value.Status.Description, err = b.text(value.Status.Description); err != nil {
		return nil, 0, err
	}
	events := input.Events()
	if err = b.items(len(events), 128); err != nil {
		return nil, 0, err
	}
	value.Events = make([]sdktrace.Event, len(events))
	for i, e := range events {
		name, failure := b.text(e.Name)
		if failure != nil {
			return nil, 0, failure
		}
		attrs, failure := b.attributes(e.Attributes)
		if failure != nil {
			return nil, 0, failure
		}
		value.Events[i] = sdktrace.Event{Name: name, Attributes: attrs, DroppedAttributeCount: e.DroppedAttributeCount, Time: e.Time.UTC()}
	}
	links := input.Links()
	if err = b.items(len(links), 128); err != nil {
		return nil, 0, err
	}
	value.Links = make([]sdktrace.Link, len(links))
	for i, link := range links {
		ctx, failure := b.spanContext(link.SpanContext)
		if failure != nil {
			return nil, 0, failure
		}
		attrs, failure := b.attributes(link.Attributes)
		if failure != nil {
			return nil, 0, failure
		}
		value.Links[i] = sdktrace.Link{SpanContext: ctx, Attributes: attrs, DroppedAttributeCount: link.DroppedAttributeCount}
	}
	value.DroppedAttributes, value.DroppedEvents, value.DroppedLinks, value.ChildSpanCount = input.DroppedAttributes(), input.DroppedEvents(), input.DroppedLinks(), input.ChildSpanCount()
	// Snapshot is the pinned SDK's public value constructor. Do not use
	// SpanStubFromReadOnlySpan: it would copy slices/pointers only shallowly.
	return value.Snapshot(), spanEnvelopeLimit - b.left, nil
}

type envelopeProcessor struct {
	next     sdktrace.SpanProcessor
	closed   atomic.Bool
	rejected atomic.Uint64
}

func newEnvelopeProcessor(next sdktrace.SpanProcessor) (*envelopeProcessor, error) {
	if err := validateSnapshotSDK(sdk.Version()); err != nil {
		return nil, err
	}
	return &envelopeProcessor{next: next}, nil
}

func (p *envelopeProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	if !p.closed.Load() {
		p.next.OnStart(ctx, s)
	}
}

func (p *envelopeProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if p.closed.Load() {
		return
	}
	defer func() {
		if recover() != nil {
			p.rejected.Add(1)
		}
	}()
	if !s.SpanContext().IsSampled() {
		return
	}
	copy, _, err := detachedSpan(s)
	if err != nil {
		p.rejected.Add(1)
		return
	}
	p.next.OnEnd(copy)
}

func (p *envelopeProcessor) Shutdown(ctx context.Context) error {
	if p.closed.Swap(true) {
		return nil
	}
	return p.next.Shutdown(ctx)
}

func (p *envelopeProcessor) ForceFlush(ctx context.Context) error {
	if p.closed.Load() {
		return nil
	}
	return p.next.ForceFlush(ctx)
}
